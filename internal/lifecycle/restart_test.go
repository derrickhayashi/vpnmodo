package lifecycle

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/derrickhayashi/vpn-sidecars-next/internal/operation"
	"github.com/derrickhayashi/vpn-sidecars-next/internal/operationstore"
	"github.com/derrickhayashi/vpn-sidecars-next/internal/profile"
)

func waitConnection(t *testing.T, m *Manager, name string, predicate func(operation.Connection) bool) operation.Connection {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		c, err := m.Connection(name)
		if err != nil {
			t.Fatal(err)
		}
		if predicate(c) {
			return c
		}
		time.Sleep(3 * time.Millisecond)
	}
	t.Fatal("connection did not reconcile")
	return operation.Connection{}
}
func TestRestartInterruptsMFAWithoutRetryAndPreservesHealthySession(t *testing.T) {
	root := filepath.Join(t.TempDir(), "state")
	adapter := newAdapter()
	adapter.mfa = true
	m := newManager(t, root, adapter, Options{})
	pending, err := m.Ensure(context.Background(), "pending")
	if err != nil {
		t.Fatal(err)
	}
	pending = waitState(t, m, pending.ID, operation.AwaitingMFA)
	adapter.mu.Lock()
	adapter.mfa = false
	adapter.mu.Unlock()
	healthy, err := m.Ensure(context.Background(), "healthy")
	if err != nil {
		t.Fatal(err)
	}
	waitState(t, m, healthy.ID, operation.Succeeded)
	if err = m.Close(); err != nil {
		t.Fatal(err)
	}
	reopened := newManager(t, root, adapter, Options{})
	reopened.StartReconcile()
	interrupted, err := reopened.Status(pending.ID)
	if err != nil || interrupted.State != operation.Failed || interrupted.Failure != operation.Interrupted || interrupted.Challenge != nil {
		t.Fatal("interrupted MFA resumed", err)
	}
	waitConnection(t, reopened, "healthy", func(c operation.Connection) bool {
		return c.Observed == operation.ObservedConnected && c.RecoveryEnabled
	})
	waitConnection(t, reopened, "pending", func(c operation.Connection) bool { return c.Observed == operation.ObservedDisconnected })
	if count, _, _ := adapter.counts("pending"); count != 1 {
		t.Fatal("MFA silently retried on restart")
	}
	if count, stops, connected := adapter.counts("healthy"); count != 1 || stops != 0 || !connected {
		t.Fatal("healthy session disrupted")
	}
	if _, err = reopened.Recover(context.Background(), "pending"); !errors.Is(err, ErrIntent) {
		t.Fatal("interrupted login allowed unattended recovery")
	}
}
func TestRestartEnforcesSavedDisconnectIntent(t *testing.T) {
	root := filepath.Join(t.TempDir(), "state")
	adapter := newAdapter()
	m := newManager(t, root, adapter, Options{})
	r, err := m.Ensure(context.Background(), "example")
	if err != nil {
		t.Fatal(err)
	}
	waitState(t, m, r.ID, operation.Succeeded)
	adapter.mu.Lock()
	adapter.failStop = true
	adapter.mu.Unlock()
	stopped, err := m.Disconnect(context.Background(), "example")
	if err != nil {
		t.Fatal(err)
	}
	waitState(t, m, stopped.ID, operation.Failed)
	if err = m.Close(); err != nil {
		t.Fatal(err)
	}
	adapter.mu.Lock()
	adapter.failStop = false
	adapter.mu.Unlock()
	reopened := newManager(t, root, adapter, Options{})
	reopened.StartReconcile()
	waitConnection(t, reopened, "example", func(c operation.Connection) bool {
		return c.Desired == operation.Disconnected && c.Observed == operation.ObservedDisconnected && c.ActiveOperation == ""
	})
	if count, _, connected := adapter.counts("example"); count != 1 || connected {
		t.Fatal("restart reversed disconnect intent")
	}
	if _, err = reopened.Recover(context.Background(), "example"); !errors.Is(err, ErrIntent) {
		t.Fatal("recovery reversed saved disconnect")
	}
}

type failingJournal struct {
	operationstore.Journal
	mu          sync.Mutex
	fail, after bool
}

func (j *failingJournal) Save(s operationstore.State) error {
	j.mu.Lock()
	fail, after := j.fail, j.after
	j.fail = false
	j.mu.Unlock()
	if !fail {
		return j.Journal.Save(s)
	}
	if after {
		if err := j.Journal.Save(s); err != nil {
			return err
		}
	}
	return errors.New("synthetic-sensitive-persistence-error")
}
func (j *failingJournal) arm(after bool) { j.mu.Lock(); j.fail = true; j.after = after; j.mu.Unlock() }
func TestPersistenceFailureBlocksSideEffectsAndRequiresRestart(t *testing.T) {
	for _, after := range []bool{false, true} {
		t.Run(map[bool]string{false: "before-commit", true: "uncertain-commit"}[after], func(t *testing.T) {
			root := filepath.Join(t.TempDir(), "state")
			journal, err := operationstore.Open(root)
			if err != nil {
				t.Fatal(err)
			}
			failing := &failingJournal{Journal: journal}
			adapter := newAdapter()
			options := Options{Adapter: adapter, WithProfile: func(_ context.Context, name string, fn func(profile.Snapshot) error) error { return fn(fixture(name)) }}
			m, err := Open(context.Background(), failing, options)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { m.Close() })
			failing.arm(after)
			if _, err = m.Ensure(context.Background(), "example"); !errors.Is(err, ErrPersistence) {
				t.Fatal("persistence failure concealed", err)
			}
			if m.Ready() {
				t.Fatal("failed journal reported ready")
			}
			if _, err = m.Ensure(context.Background(), "other"); !errors.Is(err, ErrPersistence) {
				t.Fatal("continued after uncertain persistence")
			}
			if count, _, _ := adapter.counts("example"); count != 0 {
				t.Fatal("side effect ran before durable intent")
			}
			if err = m.Close(); err != nil {
				t.Fatal(err)
			}
			reopened := newManager(t, root, adapter, Options{})
			reopened.StartReconcile()
			if after {
				state, err := reopened.journal.Load()
				if err != nil {
					t.Fatal(err)
				}
				for _, r := range state.Operations {
					if !r.State.Terminal() || r.Failure != operation.Interrupted {
						t.Fatal("uncertain operation not reconciled")
					}
				}
			}
			if count, _, _ := adapter.counts("example"); count != 0 {
				t.Fatal("restart initiated a fresh login")
			}
		})
	}
}
func TestFailureAfterConnectCleansNewAttemptAndNeverClaimsSuccess(t *testing.T) {
	root := filepath.Join(t.TempDir(), "state")
	journal, err := operationstore.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	failing := &failingJournal{Journal: journal}
	adapter := newAdapter()
	adapter.connected = func() { failing.arm(false) }
	m, err := Open(context.Background(), failing, Options{Adapter: adapter, WithProfile: func(_ context.Context, name string, fn func(profile.Snapshot) error) error { return fn(fixture(name)) }})
	if err != nil {
		t.Fatal(err)
	}
	defer m.Close()
	if _, err = m.Ensure(context.Background(), "example"); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		_, stops, connected := adapter.counts("example")
		if !m.Ready() && stops > 0 && !connected {
			return
		}
		time.Sleep(3 * time.Millisecond)
	}
	t.Fatal("failed persistence left a fresh attempt usable")
}
func TestHistoryBoundAndBackendErrorsAreSecretFree(t *testing.T) {
	root := filepath.Join(t.TempDir(), "state")
	adapter := newAdapter()
	adapter.connectError = errors.New("synthetic-sensitive-adapter-output")
	m := newManager(t, root, adapter, Options{})
	r, err := m.Ensure(context.Background(), "example")
	if err != nil {
		t.Fatal(err)
	}
	waitState(t, m, r.ID, operation.Failed)
	m.mu.Lock()
	next := m.state.Clone()
	now := time.Now().UTC()
	for i := range operationstore.MaxOperations + 20 {
		id := fmt.Sprintf("op-%032x", i+1)
		next.Operations[id] = operation.Record{ID: id, Profile: "history", ProfileRevision: 1, Kind: operation.DisconnectConnection, State: operation.Succeeded, Revision: 3, CreatedAt: now.Add(-time.Hour), UpdatedAt: now.Add(-time.Duration(i) * time.Second), Deadline: now.Add(time.Hour)}
	}
	err = m.commitLocked(next)
	m.mu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	state, err := m.journal.Load()
	if err != nil || len(state.Operations) != operationstore.MaxOperations {
		t.Fatal("history unbounded", err)
	}
	encoded, _ := json.Marshal(state)
	if strings.Contains(string(encoded), "synthetic-sensitive") {
		t.Fatal("adapter error persisted")
	}
	if err = m.Close(); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(filepath.Join(root, "operations.db"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "synthetic-sensitive") {
		t.Fatal("adapter error in journal pages")
	}
}

func TestInterruptedXAuthDisposesLateEstablishedNamespace(t *testing.T) {
	root := filepath.Join(t.TempDir(), "state")
	a := newAdapter()
	a.mfa = true
	open := func() *Manager {
		journal, err := operationstore.Open(root)
		if err != nil {
			t.Fatal(err)
		}
		m, err := Open(context.Background(), journal, Options{Adapter: a, WithProfile: func(_ context.Context, name string, fn func(profile.Snapshot) error) error {
			p := fixture(name)
			p.Profile.Backend = profile.Backend{Type: profile.XAuth, XAuth: &profile.XAuthConfig{Server: "192.0.2.1"}}
			return fn(p)
		}})
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { m.Close() })
		return m
	}
	m := open()
	r, err := m.Ensure(context.Background(), "interrupted")
	if err != nil {
		t.Fatal(err)
	}
	waitState(t, m, r.ID, operation.AwaitingMFA)
	m.Close()
	// A late peer response after a crash must not make an interrupted login
	// appear reusable or preserve the namespace that held its challenge.
	a.mu.Lock()
	a.sessions["interrupted"] = true
	a.mu.Unlock()
	restarted := open()
	restarted.StartReconcile()
	waitConnection(t, restarted, "interrupted", func(c operation.Connection) bool { return c.Observed == operation.ObservedDisconnected })
	_, _, connected := a.counts("interrupted")
	if connected {
		t.Fatal("late XAuth namespace retained")
	}
}
