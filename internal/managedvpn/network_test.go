package managedvpn

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/derrickhayashi/vpnmodo/internal/lifecycle"
	"github.com/derrickhayashi/vpnmodo/internal/operation"
	"github.com/derrickhayashi/vpnmodo/internal/profile"
)

func TestEveryTrafficRouteMustHaveNetworkSettings(t *testing.T) {
	cases := []struct {
		name, routes string
		ppp, want    bool
	}{
		{"all routes", "default via 192.0.2.1 dev eth0 mtu 1300 advmss 1260\n198.51.100.0/24 via 192.0.2.1 dev eth0 mtu 1300 advmss 1260", false, true},
		{"one route missing metrics", "default dev eth0 mtu 1300 advmss 1260\n198.51.100.0/24 dev eth0", false, false},
		{"wrong mss", "default dev eth0 mtu 1300 advmss 1448", false, false},
		{"wrong mtu", "default dev eth0 mtu 1400 advmss 1260", false, false},
		{"empty table", "", false, false},
		{"bypass only", "throw 203.0.113.1", false, false},
		{"bypass untouched", "throw 203.0.113.1\ndefault dev eth0 mtu 1300 advmss 1260", false, true},
		{"malformed metric", "default dev eth0 mtu", false, false},
		{"locked mtu", "default dev eth0 mtu lock 1300 advmss 1260", false, false},
		{"ppp inherited", "default dev ppp0\n198.51.100.0/24 dev ppp0", true, true},
		{"ppp explicit bad metric", "default dev ppp0\n198.51.100.0/24 dev ppp0 advmss 1460", true, false},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			if got := networkRoutesMatch(test.routes, 1300, test.ppp); got != test.want {
				t.Fatalf("match=%v want %v", got, test.want)
			}
		})
	}
}

func TestProfileNetworkPersistsAcrossRecreationAndGatesApplication(t *testing.T) {
	a, docker, store, p := setup(t)
	ctx := context.Background()
	p.Network = &profile.NetworkConfig{MTU: 1300}
	if _, err := store.Update(ctx, p, 1); err != nil {
		t.Fatal(err)
	}
	for attempt := 0; attempt < 2; attempt++ {
		saved, err := store.Inspect(ctx, p.Name)
		if err != nil || saved.Profile.Network == nil || saved.Profile.Network.MTU != 1300 {
			t.Fatal("saved MTU missing", err)
		}
		p = saved.Profile
		docker.pppMTU = 1300
		docker.networkRoutes = "default dev ppp0"
		if err := a.Connect(ctx, p, nil); err != nil {
			t.Fatal(err)
		}
		raw, err := os.ReadFile(filepath.Join(a.dir(p), "compose.json"))
		if err != nil {
			t.Fatal(err)
		}
		var model struct {
			Services map[string]struct{ Environment map[string]string }
		}
		if json.Unmarshal(raw, &model) != nil || model.Services["vpn"].Environment["VPN_MTU"] != "1300" {
			t.Fatal("MTU missing from deployment")
		}
		if err := a.RepairForwarding(ctx, p); err != nil {
			t.Fatal(err)
		}
		before := mutationCount(docker)
		n := a.Network(ctx, p)
		if n == nil || !n.Verified || n.EffectiveMTU != 1300 || n.EffectiveMSS != 1260 {
			t.Fatal("network evidence", n)
		}
		if mutationCount(docker) != before {
			t.Fatal("inspection changed runtime")
		}
		docker.networkRoutes += "\n198.51.100.0/24 dev ppp0 advmss 1460"
		if err := a.Verify(ctx, p); !errors.Is(err, lifecycle.ErrNetworkSettings) {
			t.Fatal("incorrect route passed verification", err)
		}
		health, id, err := a.Access(ctx, p)
		if err != nil || health != operation.HealthNetworkMismatch || id != "" {
			t.Fatal("application exposed despite bad route", health, err)
		}
		if err := a.Disconnect(ctx, p); err != nil {
			t.Fatal(err)
		}
	}
	// A lower negotiated PPP limit remains safe and is reported accurately.
	docker.pppMTU = 1200
	docker.networkRoutes = "default dev ppp0"
	if err := a.Connect(ctx, p, nil); err != nil {
		t.Fatal(err)
	}
	if n := a.Network(ctx, p); !n.Verified || n.EffectiveMTU != 1200 || n.EffectiveMSS != 1160 {
		t.Fatal("negotiated limit", n)
	}
}

func TestIPsecNetworkChecksAllRoutesAndLeavesTransportAlone(t *testing.T) {
	a, docker, p := setupIKEv2(t)
	p.Network = &profile.NetworkConfig{MTU: 1300}
	// Test the runtime check independently of deployment creation/authentication.
	c := container{ID: strings.Repeat("a", 64)}
	c.State.Running = true
	docker.networkRoutes = "default via 192.0.2.1 dev eth0 mtu 1300 advmss 1260\n198.51.100.0/24 via 192.0.2.1 dev eth0 mtu 1300 advmss 1260"
	for _, backend := range []profile.BackendType{profile.IKEv2, profile.XAuth} {
		p.Backend.Type = backend
		if n := a.networkStatus(context.Background(), p, c); !n.Verified {
			t.Fatal("IPsec metrics not verified", n)
		}
		call := strings.Join(docker.calls[len(docker.calls)-1], " ")
		if !strings.Contains(call, "route show table 220") {
			t.Fatal("inspected outer transport routes")
		}
	}
	docker.networkRoutes += "\n192.0.2.10/32 dev eth0"
	if n := a.networkStatus(context.Background(), p, c); n.Verified {
		t.Fatal("unconfigured new route accepted")
	}
}

func TestMTUManifestForEveryBackend(t *testing.T) {
	paths, err := filepath.Glob("../../examples/profiles/v1/*.json")
	if err != nil || len(paths) != 4 {
		t.Fatal("backend fixtures", err)
	}
	for _, path := range paths {
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		p, err := profile.DecodeProfile(raw)
		if err != nil {
			t.Fatal(err)
		}
		for _, network := range []*profile.NetworkConfig{nil, {MTU: 1300}} {
			p.Network = network
			raw, err := manifest(deployment{Profile: p, GatewayIPv4: "203.0.113.10"}, "/synthetic/runtime")
			if err != nil {
				t.Fatal(err)
			}
			var model struct {
				Services map[string]struct{ Environment map[string]string }
			}
			if json.Unmarshal(raw, &model) != nil {
				t.Fatal("decode manifest")
			}
			value, present := model.Services["vpn"].Environment["VPN_MTU"]
			if network == nil && present || network != nil && value != "1300" {
				t.Fatal("incorrect backend MTU", p.Backend.Type)
			}
		}
	}
}

func TestGlobalProtectNetworkChecksTunnelRoutesInsteadOfIPsecTable(t *testing.T) {
	a, f, p := setupGlobalProtect(t)
	p.Network = &profile.NetworkConfig{MTU: 1300}
	f.pppMTU = 1300
	f.networkRoutes = "default dev vpn0 mtu 1300 advmss 1260"
	c := container{ID: strings.Repeat("a", 64)}
	c.State.Running = true
	n := a.networkStatus(context.Background(), p, c)
	if n == nil || !n.Verified || n.EffectiveMTU != 1300 || n.EffectiveMSS != 1260 {
		t.Fatal("TUN network settings unverified", n)
	}
	if !strings.Contains(strings.Join(f.calls[len(f.calls)-1], " "), "route show table all dev vpn0") {
		t.Fatal("outer transport inspected")
	}
	f.networkRoutes = "default dev vpn0 mtu 1300 advmss 1360"
	if a.networkStatus(context.Background(), p, c).Verified {
		t.Fatal("incorrect tunnel MSS accepted")
	}
	f.pppMTU = 1200
	f.networkRoutes = "default dev vpn0 mtu 1200 advmss 1160"
	if n := a.networkStatus(context.Background(), p, c); !n.Verified || n.EffectiveMTU != 1200 || n.EffectiveMSS != 1160 {
		t.Fatal("lower negotiated TUN limit", n)
	}
}
