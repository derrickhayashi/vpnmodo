package managedvpn

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/derrickhayashi/vpnmodo/internal/lifecycle"
	"github.com/derrickhayashi/vpnmodo/internal/operation"
	"github.com/derrickhayashi/vpnmodo/internal/profile"
	"github.com/derrickhayashi/vpnmodo/internal/secret"
)

func setupGlobalProtect(t *testing.T) (*Adapter, *fakeDocker, profile.Profile) {
	t.Helper()
	a, docker, store, p := setup(t)
	if _, err := store.Delete(context.Background(), p.Name, 1); err != nil {
		t.Fatal(err)
	}
	ca, err := os.ReadFile("../profile/testdata/ikev2-ca.txt")
	if err != nil {
		t.Fatal(err)
	}
	p.Backend = profile.Backend{Type: profile.GlobalProtect, GlobalProtect: &profile.GlobalProtectConfig{Server: "203.0.113.1", DNS: []string{"198.51.100.53"}}}
	c := profile.Credentials{Username: secret.New("synthetic-user"), Password: secret.New("synthetic-password"), CACert: secret.New(string(ca))}
	if _, err := store.Create(context.Background(), p, c); err != nil {
		t.Fatal(err)
	}
	if a.Supports(profile.GlobalProtect) {
		t.Fatal("backend enabled without operator image")
	}
	if err := a.EnableGlobalProtect(""); err != nil {
		t.Fatal(err)
	}
	return a, docker, p
}

func TestGlobalProtectManifestAndLifecycle(t *testing.T) {
	a, docker, p := setupGlobalProtect(t)
	ctx := context.Background()
	if !a.Supports(profile.GlobalProtect) || !a.CanRecover(p) {
		t.Fatal("backend disabled")
	}
	if err := a.Connect(ctx, p, func(context.Context) (secret.Value, error) { t.Fatal("unexpected MFA"); return secret.Value{}, nil }); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(filepath.Join(a.dir(p), "compose.json"))
	if err != nil {
		t.Fatal(err)
	}
	var model struct {
		Services map[string]map[string]any
		Secrets  map[string]any
	}
	if json.Unmarshal(raw, &model) != nil {
		t.Fatal("invalid manifest")
	}
	vpn := model.Services["vpn"]
	env := vpn["environment"].(map[string]any)
	if env["VPN_SERVER"] != "203.0.113.1" || env["VPN_GATEWAY_HOST"] != "203.0.113.1" || env["VPN_DNS"] != "198.51.100.53" || env["VPN_PSK_FILE"] != nil {
		t.Fatal("incorrect gateway or secrets")
	}
	if len(model.Secrets) != 3 || model.Secrets["vpn_ca_cert"] == nil || model.Secrets["vpn_psk"] != nil || strings.Contains(string(raw), "synthetic-password") || strings.Contains(string(raw), "BEGIN CERTIFICATE") {
		t.Fatal("unsafe credentials")
	}
	if vpn["devices"].([]any)[0] != "/dev/net/tun:/dev/net/tun" || vpn["read_only"] != true || vpn["restart"] != "no" || vpn["entrypoint"].([]any)[0] != "/usr/local/bin/vpn-managed-globalprotect" || model.Services["application"]["user"] != "65534:65534" {
		t.Fatal("unsafe namespace")
	}
	docker.service = false
	if a.RepairForwarding(ctx, p) == nil || len(docker.containers) != 1 {
		t.Fatal("application attached without private health")
	}
	docker.service = true
	if err = a.RepairForwarding(ctx, p); err != nil {
		t.Fatal(err)
	}
	if h, _, err := a.Access(ctx, p); err != nil || h != operation.HealthHealthy {
		t.Fatal("unhealthy application")
	}
	if err = a.Disconnect(ctx, p); err != nil {
		t.Fatal(err)
	}
	dirs, err := os.ReadDir(filepath.Join(a.dir(p), "credentials"))
	if err != nil || len(dirs) != 0 || len(docker.containers) != 0 {
		t.Fatal("cleanup incomplete")
	}
	if err = a.Forget(ctx, p.Name); err != nil {
		t.Fatal(err)
	}
}

func TestGlobalProtectCancellationAndPermanentFailure(t *testing.T) {
	a, docker, p := setupGlobalProtect(t)
	docker.tunnel = false
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := a.Connect(ctx, p, nil); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal("unbounded connection")
	}
	if err := a.Disconnect(context.Background(), p); err != nil {
		t.Fatal(err)
	}
	docker.tunnel, docker.codeAccepted, docker.exitDuringProbe = true, true, 2
	if err := a.Connect(context.Background(), p, nil); !errors.Is(err, lifecycle.ErrAuthentication) {
		t.Fatal("login failure not permanent")
	}
	if h, _, err := a.Access(context.Background(), p); err != nil || h != operation.HealthStopped {
		t.Fatal("automatic retry permitted after login failure")
	}
}

func TestGlobalProtectHostnameResolutionUsesItsOwnImage(t *testing.T) {
	a, docker, p := setupGlobalProtect(t)
	r := &resolverDocker{fakeDocker: docker, output: []byte("203.0.113.9\n")}
	a.docker = r
	p.Backend.GlobalProtect.Server = "gateway.example.test"
	// Resolution shares the bounded unprivileged helper, with no IKEv2 dependency.
	ip, err := a.resolveGatewayWith(context.Background(), p.Backend.GlobalProtect.Server, a.globalprotect, "/usr/bin/python3")
	if err != nil || ip != "203.0.113.9" {
		t.Fatal("resolution failed")
	}
	args := strings.Join(r.args, " ")
	if !strings.Contains(args, DefaultGlobalProtectImage+" 5 /usr/bin/python3") || strings.Contains(args, "mount") || strings.Contains(args, "gateway.example.test") {
		t.Fatal("unsafe resolver or wrong image")
	}
}

func TestGlobalProtectExplicitInsecurePolicyHasNoCAFile(t *testing.T) {
	a, _, p := setupGlobalProtect(t)
	ctx := context.Background()
	if _, err := a.store.Delete(ctx, p.Name, 1); err != nil {
		t.Fatal(err)
	}
	c := profile.Credentials{Username: secret.New("synthetic-user"), Password: secret.New("synthetic-password")}
	if _, err := a.store.Create(ctx, p, c); err == nil {
		t.Fatal("default trust accepted password-only login")
	}
	p.Backend.GlobalProtect.InsecureSkipTLSVerify = true
	if _, err := a.store.Create(ctx, p, c); err != nil {
		t.Fatal(err)
	}
	if err := a.Connect(ctx, p, nil); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(filepath.Join(a.dir(p), "compose.json"))
	if err != nil {
		t.Fatal(err)
	}
	var model struct {
		Services map[string]map[string]any
		Secrets  map[string]any
	}
	if json.Unmarshal(raw, &model) != nil {
		t.Fatal("manifest decode")
	}
	env := model.Services["vpn"]["environment"].(map[string]any)
	if env["VPN_INSECURE_SKIP_TLS_VERIFY"] != "true" || env["VPN_CA_CERT_FILE"] != nil || model.Secrets["vpn_ca_cert"] != nil || len(model.Secrets) != 2 {
		t.Fatal("unexpected insecure trust manifest")
	}
	d, err := a.saved(p)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(a.dir(p), "credentials", d.Token, "ca_cert")); !os.IsNotExist(err) {
		t.Fatal("CA runtime file retained")
	}
	if err := a.Disconnect(ctx, p); err != nil {
		t.Fatal(err)
	}
	// Turning verification back on cannot reuse password-only credentials.
	p.Backend.GlobalProtect.InsecureSkipTLSVerify = false
	snap, err := a.store.Update(ctx, p, 1)
	if err != nil || snap.CredentialsPresent {
		t.Fatal("missing trust incorrectly reported usable")
	}
	if err := a.store.WithCredentials(ctx, p.Name, func(profile.Snapshot, profile.Credentials) error {
		t.Fatal("untrusted credentials released")
		return nil
	}); !errors.Is(err, profile.ErrCredentials) {
		t.Fatal("strict trust bypass")
	}
}
