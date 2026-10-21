package managedvpn

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/derrickhayashi/vpnmodo/internal/lifecycle"
	"github.com/derrickhayashi/vpnmodo/internal/operation"
	"github.com/derrickhayashi/vpnmodo/internal/profile"
	"github.com/derrickhayashi/vpnmodo/internal/profilestore"
	"github.com/derrickhayashi/vpnmodo/internal/secret"
)

type fakeDocker struct {
	exitDuringProbe int
	authState       string
	codeAccepted    bool
	repeatChallenge bool
	failApplication bool
	containers      map[string]container
	calls           [][]string
	tunnel, service bool
	network         string
	networkLabels   map[string]string
	detached        bool
}

func (f *fakeDocker) Run(ctx context.Context, args ...string) ([]byte, error) {
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	f.calls = append(f.calls, append([]string(nil), args...))
	switch args[0] {
	case "ps":
		var ids []string
		for id := range f.containers {
			ids = append(ids, id)
		}
		return []byte(strings.Join(ids, "\n")), nil
	case "inspect":
		return json.Marshal([]container{f.containers[args[1]]})
	case "image":
		return []byte("[]"), nil
	case "network":
		if args[1] == "ls" {
			return []byte(f.network), nil
		}
		if args[1] == "inspect" {
			return json.Marshal([]map[string]any{{"Labels": f.networkLabels}})
		}
		return nil, nil
	case "stop":
		c := f.containers[args[len(args)-1]]
		c.State.Running = false
		f.containers[c.ID] = c
		return nil, nil
	case "rm":
		delete(f.containers, args[len(args)-1])
		return nil, nil
	case "exec":
		cmd := args[4:]
		switch cmd[0] {
		case "/usr/local/bin/vpn-auth-state":
			return []byte(f.authState), nil
		case "/usr/local/bin/vpn-tunnelcheck":
			if f.codeAccepted && f.exitDuringProbe != 0 {
				c := f.containers[args[1]]
				c.State.Running = false
				c.State.ExitCode = f.exitDuringProbe
				f.containers[c.ID] = c
				return nil, commandError{137}
			}
			if !f.tunnel {
				return nil, commandError{1}
			}
		case "/usr/local/bin/vpn-servicecheck":
			if !f.service {
				return nil, commandError{1}
			}
		case "test":
			return nil, commandError{1}
		case "readlink":
			if f.detached && f.containers[args[1]].Config.Labels["com.docker.compose.service"] == "application" {
				return []byte("net:[2]\n"), nil
			}
			return []byte("net:[1]\n"), nil
		}
		return nil, nil
	case "compose":
		service := args[len(args)-1]
		var path string
		for i, v := range args {
			if v == "-f" {
				path = args[i+1]
			}
		}
		var model struct {
			Name     string
			Services map[string]struct {
				Image  string
				Labels map[string]string
			}
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return nil, err
		}
		if json.Unmarshal(data, &model) != nil {
			return nil, ErrRuntime
		}
		var c container
		c.ID = strings.Repeat("a", 64)
		if service == "application" {
			c.ID = strings.Repeat("b", 64)
			f.detached = false
		}
		c.Config.Image = model.Services[service].Image
		c.Config.Labels = model.Services[service].Labels
		c.Config.Labels["com.docker.compose.project"] = model.Name
		c.Config.Labels["com.docker.compose.service"] = service
		c.State.Running = true
		c.State.Status = "running"
		c.State.StartedAt = time.Now().UTC()
		f.containers[c.ID] = c
		if service == "application" && f.failApplication {
			return nil, commandError{1}
		}
		return nil, nil
	}
	return nil, ErrRuntime
}
func setup(t *testing.T) (*Adapter, *fakeDocker, *profilestore.Store, profile.Profile) {
	t.Helper()
	root := filepath.Join(t.TempDir(), "state")
	store, err := profilestore.Open(root, func(context.Context, string) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	p := profile.Profile{SchemaVersion: 1, Name: "example", Backend: profile.Backend{Type: profile.L2TP, L2TP: &profile.L2TPConfig{Server: "vpn.example.com"}}, Health: profile.TCPHealth{Host: "198.51.100.1", Port: 22}}
	_, err = store.Create(context.Background(), p, profile.Credentials{Username: secret.New("synthetic-user"), Password: secret.New("synthetic-password"), PSK: secret.New("synthetic-shared")})
	if err != nil {
		t.Fatal(err)
	}
	docker := &fakeDocker{containers: map[string]container{}, tunnel: true, service: true}
	a, err := New(filepath.Join(root, "deployments"), "", "", store, docker)
	if err != nil {
		t.Fatal(err)
	}
	return a, docker, store, p
}
func mutationCount(f *fakeDocker) int {
	n := 0
	for _, args := range f.calls {
		if args[0] == "rm" || args[0] == "stop" || args[0] == "compose" || args[0] == "network" && args[1] == "rm" {
			n++
		}
	}
	return n
}
func TestManagedLifecycleAndCredentialDisposal(t *testing.T) {
	a, f, store, p := setup(t)
	ctx := context.Background()
	if err := a.Connect(ctx, p, nil); err != nil {
		t.Fatal(err)
	}
	if err := a.Verify(ctx, p); err != nil {
		t.Fatal(err)
	}
	if err := a.RepairForwarding(ctx, p); err != nil {
		t.Fatal(err)
	}
	if health, err := a.Check(ctx, p); err != nil || health != operation.HealthHealthy {
		t.Fatal(health, err)
	}
	before := mutationCount(f)
	if ok, err := a.Observe(ctx, p); !ok || err != nil {
		t.Fatal(err)
	}
	if err := a.Verify(ctx, p); err != nil {
		t.Fatal(err)
	}
	if err := a.RepairForwarding(ctx, p); err != nil {
		t.Fatal(err)
	}
	if mutationCount(f) != before {
		t.Fatal("healthy session recreated")
	}
	d, err := a.saved(p)
	if err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"username", "password", "psk"} {
		path := filepath.Join(a.dir(p), "credentials", d.Token, key)
		info, err := os.Stat(path)
		if err != nil || info.Mode().Perm() != 0600 {
			t.Fatal("unsafe credential", err)
		}
	}
	manifestData, _ := os.ReadFile(filepath.Join(a.dir(p), "compose.json"))
	for _, value := range []string{"synthetic-user", "synthetic-password", "synthetic-shared"} {
		if strings.Contains(string(manifestData), value) {
			t.Fatal("secret in manifest")
		}
		for _, args := range f.calls {
			if strings.Contains(strings.Join(args, " "), value) {
				t.Fatal("secret in argv")
			}
		}
	}
	if err = a.Disconnect(ctx, p); err != nil {
		t.Fatal(err)
	}
	if len(f.containers) != 0 {
		t.Fatal("containers retained")
	}
	entries, _ := os.ReadDir(filepath.Join(a.dir(p), "credentials"))
	if len(entries) != 0 {
		t.Fatal("runtime credentials retained")
	}
	if snapshot, err := store.Inspect(ctx, p.Name); err != nil || !snapshot.CredentialsPresent {
		t.Fatal("disconnect disposed vault", err)
	}
	if err = a.Forget(ctx, p.Name); err != nil {
		t.Fatal(err)
	}
	if _, err = os.Stat(a.dir(p)); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("deleted deployment metadata retained")
	}
}
func TestOwnershipConflictsHaveNoSideEffects(t *testing.T) {
	for _, scenario := range []string{"unknown-service", "foreign-label", "wrong-project", "wrong-image", "missing-record", "foreign-network"} {
		t.Run(scenario, func(t *testing.T) {
			a, f, _, p := setup(t)
			ctx := context.Background()
			if err := a.Connect(ctx, p, nil); err != nil {
				t.Fatal(err)
			}
			var original container
			for _, c := range f.containers {
				original = c
			}
			switch scenario {
			case "unknown-service":
				c := original
				c.ID = strings.Repeat("c", 64)
				c.Config.Labels = map[string]string{"com.docker.compose.project": "vpnmodo-example", "com.docker.compose.service": "unknown-service"}
				f.containers[c.ID] = c
			case "foreign-label":
				original.Config.Labels[profile.ManagerLabel] = "foreign"
			case "wrong-project":
				original.Config.Labels["com.docker.compose.project"] = "foreign"
			case "wrong-image":
				original.Config.Image = "foreign:latest"
				f.containers[original.ID] = original
			case "missing-record":
				if err := os.Remove(filepath.Join(a.dir(p), "deployment.json")); err != nil {
					t.Fatal(err)
				}
			case "foreign-network":
				f.network = "vpnmodo-example_default"
				f.networkLabels = map[string]string{}
			}
			f.calls = nil
			if h, err := a.Check(ctx, p); err != nil || h != operation.HealthOwnerConflict {
				t.Fatal(h, err)
			}
			for _, call := range []func() error{func() error { _, err := a.Observe(ctx, p); return err }, func() error { return a.Connect(ctx, p, nil) }, func() error { return a.Verify(ctx, p) }, func() error { return a.Quiesce(ctx, p) }, func() error { return a.Disconnect(ctx, p) }, func() error { return a.RepairForwarding(ctx, p) }} {
				if !errors.Is(call(), ErrOwnership) {
					t.Fatal("ownership not enforced")
				}
			}
			if mutationCount(f) != 0 {
				t.Fatal("mutated conflicting deployment")
			}
		})
	}
}
func TestHealthSeparatesEndpointNamespaceAndIntent(t *testing.T) {
	a, f, _, p := setup(t)
	ctx := context.Background()
	if err := a.Connect(ctx, p, nil); err != nil {
		t.Fatal(err)
	}
	if err := a.RepairForwarding(ctx, p); err != nil {
		t.Fatal(err)
	}
	f.service = false
	if ok, err := a.Observe(ctx, p); err != nil || !ok {
		t.Fatal("endpoint outage became lost tunnel")
	}
	if h, err := a.Check(ctx, p); err != nil || h != operation.HealthServiceUnavailable {
		t.Fatal(h, err)
	}
	f.service = true
	f.detached = true
	if h, err := a.Check(ctx, p); err != nil || h != operation.HealthDetached {
		t.Fatal(h, err)
	}
	if err := a.RepairForwarding(ctx, p); err != nil {
		t.Fatal(err)
	}
	f.tunnel = false
	if h, err := a.Check(ctx, p); err != nil || h != operation.HealthStarting {
		t.Fatal(h, err)
	}
	for id, c := range f.containers {
		if c.Config.Labels["com.docker.compose.service"] == "vpn" {
			c.State.Running = false
			c.State.ExitCode = 0
			f.containers[id] = c
		}
	}
	if h, err := a.Check(ctx, p); err != nil || h != operation.HealthStopped {
		t.Fatal(h, err)
	}
	if err := a.Disconnect(ctx, p); err != nil {
		t.Fatal(err)
	}
	if h, err := a.Check(ctx, p); err != nil || h != operation.HealthTunnelDown {
		t.Fatal("lost deployment marker", h, err)
	}
}
func TestManifestEscapesInterpolationAndConstrainsRuntime(t *testing.T) {
	a, _, _, p := setup(t)
	ctx := context.Background()
	d, err := a.prepare(ctx, p)
	if err != nil {
		t.Fatal(err)
	}
	data, err := manifest(*d, "/private/example $HOME")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "$$HOME") {
		t.Fatal("Compose interpolation exposed paths")
	}
	var model struct{ Services map[string]map[string]any }
	if json.Unmarshal(data, &model) != nil {
		t.Fatal("manifest")
	}
	if model.Services["vpn"]["restart"] != "no" || model.Services["application"]["network_mode"] != "service:vpn" {
		t.Fatal("competing recovery/namespace model")
	}
	if _, ok := model.Services["vpn"]["ports"]; ok {
		t.Fatal("unexpected published ports")
	}
	if len(model.Services) != 2 {
		t.Fatal("unexpected service")
	}
}
func TestUnsupportedBackendAndCancelledCallsCannotMutate(t *testing.T) {
	a, f, _, p := setup(t)
	p.Backend = profile.Backend{Type: profile.XAuth, XAuth: &profile.XAuthConfig{Server: "192.0.2.1"}}
	if _, err := a.Observe(context.Background(), p); err == nil {
		t.Fatal("unsupported backend")
	}
	p.Backend = profile.Backend{Type: profile.L2TP, L2TP: &profile.L2TPConfig{Server: "vpn.example.com"}}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := a.Connect(ctx, p, nil); err == nil {
		t.Fatal("ignored cancellation")
	}
	if mutationCount(f) != 0 {
		t.Fatal("mutated on cancelled/unsupported call")
	}
}

func TestFailedPartialApplicationStartIsQuiesced(t *testing.T) {
	a, f, _, p := setup(t)
	ctx := context.Background()
	if err := a.Connect(ctx, p, nil); err != nil {
		t.Fatal(err)
	}
	f.failApplication = true
	if err := a.RepairForwarding(ctx, p); err == nil {
		t.Fatal("partial application failure claimed success")
	}
	for _, c := range f.containers {
		if c.Config.Labels["com.docker.compose.service"] == "application" {
			t.Fatal("partial application remained attached")
		}
	}
	if len(f.containers) != 1 {
		t.Fatal("application failure disrupted VPN")
	}
}

func (f *fakeDocker) RunInput(ctx context.Context, input []byte, args ...string) ([]byte, error) {
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	if len(args) != 4 || args[0] != "exec" || args[1] != "-i" || args[3] != "/usr/local/bin/vpn-submit-code" {
		return nil, ErrRuntime
	}
	f.calls = append(f.calls, append([]string(nil), args...))
	f.codeAccepted = string(input) == "9876543210"
	if !f.codeAccepted {
		return nil, ErrRuntime
	}
	if f.repeatChallenge {
		f.authState = "failed"
	} else {
		f.authState = "established"
		f.tunnel = true
	}
	return nil, nil
}
func TestXAuthPrivateChallengeAndManifest(t *testing.T) {
	for _, scenario := range []string{"success", "expiry", "repeat"} {
		t.Run(scenario, func(t *testing.T) {
			a, f, store, p := setup(t)
			if err := a.EnableXAuth(""); err != nil {
				t.Fatal(err)
			}
			p.Name = "xauth-example"
			p.Backend = profile.Backend{Type: profile.XAuth, XAuth: &profile.XAuthConfig{Server: "192.0.2.1", DNS: []string{"198.51.100.1"}}}
			_, err := store.Create(context.Background(), p, profile.Credentials{Username: secret.New("synthetic-user"), Password: secret.New("synthetic-password"), PSK: secret.New("synthetic-shared")})
			if err != nil {
				t.Fatal(err)
			}
			f.authState = "mfa"
			f.tunnel = false
			f.repeatChallenge = scenario == "repeat"
			count := 0
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			err = a.Connect(ctx, p, func(context.Context) (secret.Value, error) {
				count++
				if scenario == "expiry" {
					return secret.Value{}, lifecycle.ErrExpired
				}
				return secret.New("9876543210"), nil
			})
			if scenario == "success" && err != nil {
				t.Fatal(err)
			}
			if scenario == "expiry" && !errors.Is(err, lifecycle.ErrExpired) {
				t.Fatal("expiry lost", err)
			}
			if scenario == "repeat" && !errors.Is(err, lifecycle.ErrAuthentication) {
				t.Fatal("repeat accepted", err)
			}
			if count != 1 {
				t.Fatal("challenge count", count)
			}
			data, err := os.ReadFile(filepath.Join(a.dir(p), "compose.json"))
			if err != nil {
				t.Fatal(err)
			}
			var manifest map[string]any
			if json.Unmarshal(data, &manifest) != nil {
				t.Fatal("manifest")
			}
			services := manifest["services"].(map[string]any)
			vpn := services["vpn"].(map[string]any)
			app := services["application"].(map[string]any)
			if app["user"] != "65534:65534" || vpn["read_only"] != true || vpn["devices"] != nil {
				t.Fatal("XAuth isolation")
			}
			if vpn["image"] != DefaultXAuthImage {
				t.Fatal("wrong backend image")
			}
			for _, call := range f.calls {
				for _, arg := range call {
					if strings.Contains(arg, "9876543210") || strings.Contains(arg, "synthetic-password") {
						t.Fatal("secret in argv")
					}
				}
			}
		})
	}
}

func TestApplicationRepairCannotReconcileVPNService(t *testing.T) {
	a, f, _, p := setup(t)
	ctx := context.Background()
	if err := a.Connect(ctx, p, nil); err != nil {
		t.Fatal(err)
	}
	before := f.containers[strings.Repeat("a", 64)].ID
	if err := a.RepairForwarding(ctx, p); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(a.dir(p), "application.compose.json"))
	if err != nil {
		t.Fatal(err)
	}
	var m struct {
		Services map[string]map[string]any
		Secrets  map[string]any
	}
	if json.Unmarshal(data, &m) != nil || len(m.Services) != 1 || len(m.Secrets) != 0 {
		t.Fatal("application manifest has other resources")
	}
	if m.Services["application"]["network_mode"] != "container:"+before || m.Services["vpn"] != nil {
		t.Fatal("VPN dependency in application repair")
	}
	if f.containers[strings.Repeat("a", 64)].ID != before {
		t.Fatal("VPN changed")
	}
	found := false
	for _, args := range f.calls {
		if args[0] == "compose" && args[len(args)-1] == "application" {
			found = true
			if !strings.Contains(strings.Join(args, " "), "--no-recreate") {
				t.Fatal("application repair may recreate dependencies")
			}
		}
	}
	if !found {
		t.Fatal("missing application create")
	}
}

func TestXAuthContainerExitDuringProbeKeepsAuthenticationClassification(t *testing.T) {
	for _, code := range []int{2, 22, 27, 99} {
		t.Run(fmt.Sprint(code), func(t *testing.T) {
			a, f, store, p := setup(t)
			if err := a.EnableXAuth(""); err != nil {
				t.Fatal(err)
			}
			p.Name = "xauth-exit-example"
			p.Backend = profile.Backend{Type: profile.XAuth, XAuth: &profile.XAuthConfig{Server: "192.0.2.1"}}
			_, err := store.Create(context.Background(), p, profile.Credentials{Username: secret.New("synthetic-user"), Password: secret.New("synthetic-password"), PSK: secret.New("synthetic-shared")})
			if err != nil {
				t.Fatal(err)
			}
			f.authState = "mfa"
			f.tunnel = false
			f.exitDuringProbe = code
			err = a.Connect(context.Background(), p, func(context.Context) (secret.Value, error) { return secret.New("9876543210"), nil })
			expected := lifecycle.ErrAuthentication
			if code == 99 {
				expected = ErrRuntime
			}
			if !errors.Is(err, expected) {
				t.Fatalf("expected %v, got %v", expected, err)
			}
		})
	}
}
