package lifecycle

import (
	"context"
	"path/filepath"
	"sync"
	"testing"

	"github.com/derrickhayashi/vpnmodo/internal/operation"
	"github.com/derrickhayashi/vpnmodo/internal/operationstore"
	"github.com/derrickhayashi/vpnmodo/internal/profile"
	"github.com/derrickhayashi/vpnmodo/internal/secret"
)

type eapAdapter struct {
	*recoveryAdapter
	authMu sync.Mutex
	reject bool
}

func (a *eapAdapter) Connect(ctx context.Context, p profile.Profile, challenge func(context.Context) (secret.Value, error)) error {
	a.authMu.Lock()
	reject := a.reject
	a.authMu.Unlock()
	if reject {
		return ErrAuthentication
	}
	// This backend never calls the interactive challenge callback.
	return a.recoveryAdapter.Connect(ctx, p, nil)
}

func TestIKEv2RestartRecoveryAndAuthenticationStop(t *testing.T) {
	root := filepath.Join(t.TempDir(), "state")
	adapter := &eapAdapter{recoveryAdapter: newRecoveryAdapter()}
	open := func() *Manager {
		journal, err := operationstore.Open(root)
		if err != nil {
			t.Fatal(err)
		}
		options := recoveryOptions()
		options.Adapter = adapter
		options.WithProfile = func(_ context.Context, name string, fn func(profile.Snapshot) error) error {
			p := fixture(name)
			p.Profile.Backend = profile.Backend{Type: profile.IKEv2, IKEv2: &profile.IKEv2Config{Server: "203.0.113.1", ServerIdentity: "vpn.example.test"}}
			return fn(p)
		}
		m, err := Open(context.Background(), journal, options)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { m.Close() })
		return m
	}
	m := open()
	ensureRecovery(t, m, "example")
	if err := m.Close(); err != nil {
		t.Fatal(err)
	}
	m = open()
	m.StartReconcile()
	waitConnection(t, m, "example", func(c operation.Connection) bool {
		return c.Observed == operation.ObservedConnected && c.RecoveryEnabled
	})
	if starts, stops, connected := adapter.counts("example"); starts != 1 || stops != 0 || !connected {
		t.Fatal("restart disrupted authenticated EAP session")
	}
	expireCooldown(t, m, "example")
	adapter.setHealth("example", operation.HealthTunnelDown)
	r, err := m.Recover(context.Background(), "example")
	if err != nil {
		t.Fatal(err)
	}
	waitState(t, m, r.ID, operation.Succeeded)
	if starts, _, _ := adapter.counts("example"); starts != 2 {
		t.Fatal("EAP unattended recovery failed")
	}
	expireCooldown(t, m, "example")
	adapter.setHealth("example", operation.HealthTunnelDown)
	adapter.authMu.Lock()
	adapter.reject = true
	adapter.authMu.Unlock()
	r, err = m.Recover(context.Background(), "example")
	if err != nil {
		t.Fatal(err)
	}
	failed := waitState(t, m, r.ID, operation.Failed)
	if failed.Failure != operation.AuthenticationFailed || failed.Challenge != nil {
		t.Fatal("EAP failure entered interactive/retriable lifecycle")
	}
	c, err := m.Connection("example")
	if err != nil || c.RecoveryEnabled {
		t.Fatal("authentication failure did not stop unattended attempts")
	}
}
