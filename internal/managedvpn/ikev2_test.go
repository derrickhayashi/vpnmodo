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

func setupIKEv2(t *testing.T) (*Adapter, *fakeDocker, profile.Profile) {
	t.Helper()
	a, docker, store, p := setup(t)
	// Delete only our own synthetic inactive fixture before replacing its backend.
	if _, err := store.Delete(context.Background(), p.Name, 1); err != nil {
		t.Fatal(err)
	}
	ca, err := os.ReadFile("../profile/testdata/ikev2-ca.txt")
	if err != nil {
		t.Fatal(err)
	}
	p.Backend = profile.Backend{Type: profile.IKEv2, IKEv2: &profile.IKEv2Config{Server: "203.0.113.1", ServerIdentity: "vpn.example.test", ClientIdentity: "client@example.test", EAPIdentity: "synthetic-user", DNS: []string{"198.51.100.1"}, IKEProposals: []string{"aes128-sha256-modp2048", "aes256-sha256-modp2048"}, ESPProposals: []string{"aes128-sha256"}}}
	c := profile.Credentials{Username: secret.New("synthetic-user"), Password: secret.New("synthetic-password"), CACert: secret.New(string(ca))}
	if _, err := store.Create(context.Background(), p, c); err != nil {
		t.Fatal(err)
	}
	if err := a.EnableIKEv2(""); err != nil {
		t.Fatal(err)
	}
	return a, docker, p
}

func TestIKEv2ManifestVaultAndApplicationGate(t *testing.T) {
	a, docker, p := setupIKEv2(t)
	ctx := context.Background()
	if !a.Supports(profile.IKEv2) || !a.CanRecover(p) {
		t.Fatal("IKEv2 not enabled")
	}
	if err := a.Connect(ctx, p, func(context.Context) (secret.Value, error) { t.Fatal("EAP invoked MFA"); return secret.Value{}, nil }); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(filepath.Join(a.dir(p), "compose.json"))
	if err != nil {
		t.Fatal(err)
	}
	var m struct {
		Services map[string]map[string]any
		Secrets  map[string]any
	}
	if json.Unmarshal(raw, &m) != nil {
		t.Fatal("manifest decode")
	}
	vpn := m.Services["vpn"]
	env := vpn["environment"].(map[string]any)
	if env["VPN_SERVER"] != "203.0.113.1" || env["VPN_IKE_PROPOSALS"] != "aes128-sha256-modp2048,aes256-sha256-modp2048" || env["VPN_SERVER_IDENTITY"] != "vpn.example.test" || env["VPN_EAP_IDENTITY"] != "synthetic-user" {
		t.Fatal("IKEv2 policy lost")
	}
	if len(m.Secrets) != 3 || m.Secrets["vpn_ca_cert"] == nil || m.Secrets["vpn_psk"] != nil || vpn["devices"] != nil || vpn["restart"] != "no" || vpn["read_only"] != true {
		t.Fatal("unsafe IKEv2 manifest")
	}
	if strings.Contains(string(raw), "synthetic-password") || strings.Contains(string(raw), "BEGIN CERTIFICATE") {
		t.Fatal("credentials escaped vault")
	}
	if m.Services["application"]["user"] != "65534:65534" {
		t.Fatal("workload may bypass IKE firewall")
	}
	docker.service = false
	if a.RepairForwarding(ctx, p) == nil {
		t.Fatal("application attached without health probe")
	}
	if len(docker.containers) != 1 {
		t.Fatal("application present before health")
	}
	docker.service = true
	if err := a.RepairForwarding(ctx, p); err != nil {
		t.Fatal(err)
	}
	if h, _, err := a.Access(ctx, p); err != nil || h != operation.HealthHealthy {
		t.Fatal("unhealthy attachment")
	}
	docker.tunnel = false
	c := docker.containers[strings.Repeat("a", 64)]
	c.State.StartedAt = time.Now().Add(-151 * time.Second)
	docker.containers[c.ID] = c
	if h, _, err := a.Access(ctx, p); err != nil || h != operation.HealthTunnelDown {
		t.Fatal("loss hidden")
	}
	if err := a.Disconnect(ctx, p); err != nil {
		t.Fatal(err)
	}
	if len(docker.containers) != 0 {
		t.Fatal("disconnect retained namespace")
	}
	dirs, _ := os.ReadDir(filepath.Join(a.dir(p), "credentials"))
	if len(dirs) != 0 {
		t.Fatal("trust or credentials retained in runtime")
	}
	// Unattended reconnect never asks for an interactive challenge.
	docker.tunnel = true
	if err := a.Connect(ctx, p, nil); err != nil {
		t.Fatal(err)
	}
}

func TestIKEv2CancellationAndPermanentAuthenticationFailure(t *testing.T) {
	a, docker, p := setupIKEv2(t)
	docker.tunnel = false
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := a.Connect(ctx, p, func(context.Context) (secret.Value, error) { t.Fatal("MFA"); return secret.Value{}, nil }); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal("unbounded attempt")
	}
	if err := a.Disconnect(context.Background(), p); err != nil {
		t.Fatal(err)
	}
	docker.tunnel, docker.codeAccepted, docker.exitDuringProbe = true, true, 2
	if err := a.Connect(context.Background(), p, nil); !errors.Is(err, lifecycle.ErrAuthentication) {
		t.Fatal("permanent failure not classified")
	}
}

func TestIKEv2PermanentExitStopsHealthRecovery(t *testing.T) {
	a, docker, p := setupIKEv2(t)
	if err := a.Connect(context.Background(), p, nil); err != nil {
		t.Fatal(err)
	}
	c := docker.containers[strings.Repeat("a", 64)]
	c.State.Running = false
	c.State.ExitCode = 2
	docker.containers[c.ID] = c
	if health, _, err := a.Access(context.Background(), p); err != nil || health != operation.HealthStopped {
		t.Fatal("permanent failure remained eligible for automatic recovery")
	}
}

func TestIKEv2GatewayResolution(t *testing.T) {
	a, _, _ := setupIKEv2(t)
	if ip, err := a.resolveGateway(context.Background(), "203.0.113.10"); err != nil || ip != "203.0.113.10" {
		t.Fatal("literal gateway")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := a.resolveGateway(ctx, "gateway.invalid"); err == nil {
		t.Fatal("ignored resolution cancellation")
	}
}

type resolverDocker struct {
	*fakeDocker
	input  []byte
	args   []string
	output []byte
}

func (r *resolverDocker) RunInput(ctx context.Context, input []byte, args ...string) ([]byte, error) {
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	r.input = append([]byte(nil), input...)
	r.args = append([]string(nil), args...)
	return r.output, nil
}
func TestIKEv2HostnameUsesIsolatedBoundedResolver(t *testing.T) {
	a, docker, _ := setupIKEv2(t)
	r := &resolverDocker{fakeDocker: docker, output: []byte("203.0.113.9\n")}
	a.docker = r
	ip, err := a.resolveGateway(context.Background(), "vpn.example.test")
	if err != nil || ip != "203.0.113.9" || string(r.input) != "vpn.example.test" {
		t.Fatal("hostname resolution failed")
	}
	args := strings.Join(r.args, " ")
	if strings.Contains(args, "vpn.example.test") || strings.Contains(args, "mount") || !strings.Contains(args, "--network bridge") || !strings.Contains(args, "--user 65534:65534") || !strings.Contains(args, "--cap-drop ALL") {
		t.Fatal("unsafe bootstrap resolver")
	}
	if docker.calls[len(docker.calls)-1][0] != "rm" {
		t.Fatal("resolver not cleaned up")
	}
	for _, bad := range []string{"2001:db8::1", "not-an-address", "203.0.113.9\n203.0.113.10"} {
		r.output = []byte(bad)
		if _, err = a.resolveGateway(context.Background(), "vpn.example.test"); err == nil {
			t.Fatal("invalid resolution accepted")
		}
	}
}
