package managedvpn

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/derrickhayashi/vpnmodo/internal/operation"
	"github.com/derrickhayashi/vpnmodo/internal/profile"
	"github.com/derrickhayashi/vpnmodo/internal/secret"
)

func TestOptionalHealthKeepsTunnelAndNamespaceGatesForEveryBackend(t *testing.T) {
	for _, kind := range []profile.BackendType{profile.L2TP, profile.XAuth, profile.IKEv2, profile.GlobalProtect} {
		t.Run(string(kind), func(t *testing.T) {
			var a *Adapter
			var f *fakeDocker
			var p profile.Profile
			switch kind {
			case profile.IKEv2:
				a, f, p = setupIKEv2(t)
			case profile.GlobalProtect:
				a, f, p = setupGlobalProtect(t)
			default:
				a, f, _, p = setup(t)
				if kind == profile.XAuth {
					if _, err := a.store.Delete(context.Background(), p.Name, 1); err != nil {
						t.Fatal(err)
					}
					p.Backend = profile.Backend{Type: kind, XAuth: &profile.XAuthConfig{Server: "203.0.113.1"}}
					if _, err := a.store.Create(context.Background(), p, profile.Credentials{Username: secret.New("synthetic-user"), Password: secret.New("synthetic-password"), PSK: secret.New("synthetic-shared")}); err != nil {
						t.Fatal(err)
					}
					if err := a.EnableXAuth(""); err != nil {
						t.Fatal(err)
					}
					f.authState = "established"
				}
			}
			ctx := context.Background()
			p.Health = profile.TCPHealth{}
			if _, err := a.store.Update(ctx, p, 1); err != nil {
				t.Fatal(err)
			}
			f.service = false
			if err := a.Connect(ctx, p, nil); err != nil {
				t.Fatal(err)
			}
			if err := a.Verify(ctx, p); err != nil {
				t.Fatal("optional endpoint blocked tunnel", err)
			}
			if err := a.RepairForwarding(ctx, p); err != nil {
				t.Fatal(err)
			}
			if h, id, err := a.Access(ctx, p); err != nil || h != operation.HealthHealthy || id == "" {
				t.Fatal("application not attached", h, err)
			}
			for _, call := range f.calls {
				if strings.Contains(strings.Join(call, " "), "vpn-servicecheck") {
					t.Fatal("unconfigured endpoint probed")
				}
			}
			raw, err := os.ReadFile(filepath.Join(a.dir(p), "compose.json"))
			if err != nil {
				t.Fatal(err)
			}
			var model struct {
				Services map[string]struct{ Environment map[string]string }
			}
			if json.Unmarshal(raw, &model) != nil {
				t.Fatal("manifest decode")
			}
			for _, key := range []string{"VPN_HEALTH_HOST", "VPN_HEALTH_PORT"} {
				if _, ok := model.Services["vpn"].Environment[key]; ok {
					t.Fatal("empty endpoint exported")
				}
			}
			f.detached = true
			if h, id, err := a.Access(ctx, p); err != nil || h != operation.HealthDetached || id != "" {
				t.Fatal("namespace check bypassed")
			}
			f.detached = false
			f.tunnel = false
			c := f.containers[strings.Repeat("a", 64)]
			c.State.StartedAt = time.Now().Add(-151 * time.Second)
			f.containers[c.ID] = c
			if a.Verify(ctx, p) == nil {
				t.Fatal("lost tunnel passed verification")
			}
			if h, id, err := a.Access(ctx, p); err != nil || h != operation.HealthTunnelDown || id != "" {
				t.Fatal("lost tunnel exposed application")
			}
			if err := a.Disconnect(ctx, p); err != nil {
				t.Fatal(err)
			}
			// Explicitly configured endpoints remain mandatory and failure still blocks attachment.
			p.Health = profile.TCPHealth{Host: "192.0.2.10", Port: 22}
			if _, err := a.store.Update(ctx, p, 2); err != nil {
				t.Fatal(err)
			}
			f.tunnel = true
			if err := a.Connect(ctx, p, nil); err != nil {
				t.Fatal(err)
			}
			if a.Verify(ctx, p) == nil || a.RepairForwarding(ctx, p) == nil {
				t.Fatal("configured endpoint failure ignored")
			}
			if err := a.Disconnect(ctx, p); err != nil {
				t.Fatal(err)
			}
		})
	}
}
