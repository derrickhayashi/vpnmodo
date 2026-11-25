package lifecycle

import (
	"context"
	"errors"
	"path/filepath"
	"sync"
	"testing"

	"github.com/derrickhayashi/vpnmodo/internal/operation"
	"github.com/derrickhayashi/vpnmodo/internal/operationstore"
	"github.com/derrickhayashi/vpnmodo/internal/profile"
	"github.com/derrickhayashi/vpnmodo/internal/secret"
)

// A stale session keeps reporting local SAs even though endpoint verification
// fails. Only replacing it can restore traffic; time passing changes nothing.
type staleXAuthAdapter struct {
	*recoveryAdapter
	verifyError, freshVerifyError error
	verifyStarted                 chan struct{}
	verifyRelease                 <-chan struct{}
	once                          sync.Once
}

func (a *staleXAuthAdapter) Verify(ctx context.Context, p profile.Profile) error {
	if a.verifyStarted != nil {
		a.once.Do(func() { close(a.verifyStarted) })
	}
	if a.verifyRelease != nil {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-a.verifyRelease:
		}
	}
	if starts, _, _ := a.counts(p.Name); starts > 0 {
		return a.freshVerifyError
	}
	return a.verifyError
}

func openStaleXAuth(t *testing.T, a *staleXAuthAdapter) *Manager {
	t.Helper()
	a.sessions["example"] = true
	a.forwarding["example"] = true
	j, err := operationstore.Open(filepath.Join(t.TempDir(), "state"))
	if err != nil {
		t.Fatal(err)
	}
	options := recoveryOptions()
	options.Adapter = a
	options.WithProfile = func(_ context.Context, name string, fn func(profile.Snapshot) error) error {
		p := fixture(name)
		p.Profile.Backend = profile.Backend{Type: profile.XAuth, XAuth: &profile.XAuthConfig{Server: "203.0.113.1"}}
		return fn(p)
	}
	m, err := Open(context.Background(), j, options)
	if err != nil {
		j.Close()
		t.Fatal(err)
	}
	t.Cleanup(func() { m.Close() })
	return m
}

func TestExplicitEnsureReplacesUnusableXAuthWithOneFreshChallenge(t *testing.T) {
	a := &staleXAuthAdapter{recoveryAdapter: newRecoveryAdapter(), verifyError: ErrSessionUnusable}
	a.mfa = true
	m := openStaleXAuth(t, a)
	r, err := m.Ensure(context.Background(), "example")
	if err != nil {
		t.Fatal(err)
	}
	challenge := waitState(t, m, r.ID, operation.AwaitingMFA)
	if forwarding, _ := a.snapshot("example"); forwarding {
		t.Fatal("old application still forwarding during fresh login")
	}
	joined, err := m.Ensure(context.Background(), "example")
	if err != nil || joined.ID != r.ID {
		t.Fatal("concurrent ensure did not join the fresh login", err)
	}
	if err := m.SubmitMFA(r.ID, challenge.Challenge.ID, secret.New("9876543210")); err != nil {
		t.Fatal(err)
	}
	waitState(t, m, r.ID, operation.Succeeded)
	waitIdle(t, m)
	if starts, stops, connected := a.counts("example"); starts != 1 || stops != 1 || !connected {
		t.Fatal("stale session was not replaced exactly once", starts, stops, connected)
	}
	if forwarding, events := a.snapshot("example"); !forwarding || len(events) != 3 || events[0] != "disconnect" || events[1] != "connect" || events[2] != "repair" {
		t.Fatal("unexpected replacement order", events)
	}
}

func TestXAuthReuseDoesNotReconnectHealthyOrUncertainSessions(t *testing.T) {
	for _, failure := range []error{nil, ErrNetworkSettings, ErrUnavailable, errors.New("synthetic-owner-conflict")} {
		name := "healthy"
		if failure != nil {
			name = failure.Error()
		}
		t.Run(name, func(t *testing.T) {
			a := &staleXAuthAdapter{recoveryAdapter: newRecoveryAdapter(), verifyError: failure}
			m := openStaleXAuth(t, a)
			r, err := m.Ensure(context.Background(), "example")
			if err != nil {
				t.Fatal(err)
			}
			state := operation.Succeeded
			if failure != nil {
				state = operation.Failed
			}
			waitState(t, m, r.ID, state)
			waitIdle(t, m)
			if starts, stops, connected := a.counts("example"); starts != 0 || stops != 0 || !connected {
				t.Fatal("replaced a healthy or unclassified session", starts, stops, connected)
			}
		})
	}
}

func TestXAuthReplacementFailureNeverStartsAnotherLogin(t *testing.T) {
	for _, stage := range []string{"disconnect", "authentication", "verification"} {
		t.Run(stage, func(t *testing.T) {
			a := &staleXAuthAdapter{recoveryAdapter: newRecoveryAdapter(), verifyError: ErrSessionUnusable}
			switch stage {
			case "disconnect":
				a.failStop = true
			case "authentication":
				a.connectError = ErrAuthentication
			case "verification":
				a.freshVerifyError = ErrSessionUnusable
			}
			m := openStaleXAuth(t, a)
			r, err := m.Ensure(context.Background(), "example")
			if err != nil {
				t.Fatal(err)
			}
			waitState(t, m, r.ID, operation.Failed)
			waitIdle(t, m)
			starts, _, connected := a.counts("example")
			want := 1
			if stage == "disconnect" {
				want = 0
			}
			if starts != want || stage != "disconnect" && connected {
				t.Fatal("replacement retried or left a failed login connected", starts, connected)
			}
		})
	}
}

func TestDisconnectSupersedesXAuthReuseVerification(t *testing.T) {
	a := &staleXAuthAdapter{recoveryAdapter: newRecoveryAdapter(), verifyError: ErrSessionUnusable, verifyStarted: make(chan struct{}), verifyRelease: make(chan struct{})}
	m := openStaleXAuth(t, a)
	r, err := m.Ensure(context.Background(), "example")
	if err != nil {
		t.Fatal(err)
	}
	<-a.verifyStarted
	stop, err := m.Disconnect(context.Background(), "example")
	if err != nil {
		t.Fatal(err)
	}
	waitState(t, m, stop.ID, operation.Succeeded)
	waitIdle(t, m)
	previous, _ := m.Status(r.ID)
	if previous.State != operation.Cancelled {
		t.Fatal("superseded ensure was not cancelled")
	}
	if starts, _, connected := a.counts("example"); starts != 0 || connected {
		t.Fatal("disconnect allowed a replacement login")
	}
}
