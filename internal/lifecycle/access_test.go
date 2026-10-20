package lifecycle

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/derrickhayashi/vpnmodo/internal/operation"
	"github.com/derrickhayashi/vpnmodo/internal/profile"
	"github.com/derrickhayashi/vpnmodo/internal/secret"
)

func (f *fakeAdapter) Access(ctx context.Context, p profile.Profile) (operation.HealthState, string, error) {
	ok, err := f.Observe(ctx, p)
	if !ok {
		return operation.HealthStopped, "", err
	}
	return operation.HealthHealthy, strings.Repeat("a", 64), err
}
func TestRuntimeDoesNotStartOrResumeAuthentication(t *testing.T) {
	ctx := context.Background()
	adapter := newAdapter()
	m := newManager(t, filepath.Join(t.TempDir(), "state"), adapter, Options{})
	view, err := m.Runtime(ctx, "example")
	if err != nil || view.ApplicationID != "" || view.TunnelReady {
		t.Fatal("inactive runtime", err)
	}
	adapter.mu.Lock()
	adapter.mfa = true
	adapter.mu.Unlock()
	op, err := m.Ensure(ctx, "example")
	if err != nil {
		t.Fatal(err)
	}
	pending := waitState(t, m, op.ID, operation.AwaitingMFA)
	if _, err = m.Runtime(ctx, "example"); !errors.Is(err, ErrBusy) {
		t.Fatal("MFA inspection bypassed mutation gate", err)
	}
	if err = m.SubmitMFA(op.ID, pending.Challenge.ID, secret.New("9876543210")); err != nil {
		t.Fatal(err)
	}
	waitState(t, m, op.ID, operation.Succeeded)
	view, err = runtimeAfterWorker(m, ctx, "example")
	if err != nil || len(view.ApplicationID) != 64 || !view.TunnelReady || !view.PrivateEndpointReady {
		t.Fatal("healthy runtime", view, err)
	}
	down, err := m.Disconnect(ctx, "example")
	if err != nil {
		t.Fatal(err)
	}
	waitState(t, m, down.ID, operation.Succeeded)
	view, err = runtimeAfterWorker(m, ctx, "example")
	if err != nil || view.ApplicationID != "" || view.TunnelReady {
		t.Fatal("disconnected runtime", err)
	}
	if connects, _, _ := adapter.counts("example"); connects != 1 {
		t.Fatal("inspection initiated login")
	}
}

func runtimeAfterWorker(m *Manager, ctx context.Context, name string) (operation.RuntimeView, error) {
	deadline := time.Now().Add(time.Second)
	for {
		v, e := m.Runtime(ctx, name)
		if !errors.Is(e, ErrBusy) || time.Now().After(deadline) {
			return v, e
		}
		time.Sleep(time.Millisecond)
	}
}
