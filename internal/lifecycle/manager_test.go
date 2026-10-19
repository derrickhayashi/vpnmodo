package lifecycle

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/derrickhayashi/vpn-sidecars-next/internal/operation"
	"github.com/derrickhayashi/vpn-sidecars-next/internal/operationstore"
	"github.com/derrickhayashi/vpn-sidecars-next/internal/profile"
	"github.com/derrickhayashi/vpn-sidecars-next/internal/secret"
)

type fakeAdapter struct {
	mu                    sync.Mutex
	sessions              map[string]bool
	connects, stops       map[string]int
	block, verifyBlock    <-chan struct{}
	mfa, repeat, failStop bool
	connectError          error
	connected             func()
}

func newAdapter() *fakeAdapter {
	return &fakeAdapter{sessions: map[string]bool{}, connects: map[string]int{}, stops: map[string]int{}}
}
func (f *fakeAdapter) Observe(_ context.Context, p profile.Profile) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.sessions[p.Name], nil
}
func (f *fakeAdapter) Connect(ctx context.Context, p profile.Profile, challenge func(context.Context) (secret.Value, error)) error {
	f.mu.Lock()
	f.connects[p.Name]++
	block, mfa, repeat, connectErr, connected := f.block, f.mfa, f.repeat, f.connectError, f.connected
	f.mu.Unlock()
	if mfa {
		code, err := challenge(ctx)
		if err != nil {
			return err
		}
		if !code.Check(func(v string) bool { return v == "9876543210" }) {
			return ErrAuthentication
		}
		if repeat {
			if _, err = challenge(ctx); err != nil {
				return err
			}
		}
	}
	if block != nil {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-block:
		}
	}
	if connectErr != nil {
		return connectErr
	}
	if ctx.Err() != nil {
		return ctx.Err()
	}
	f.mu.Lock()
	f.sessions[p.Name] = true
	f.mu.Unlock()
	if connected != nil {
		connected()
	}
	return nil
}
func (f *fakeAdapter) Verify(ctx context.Context, _ profile.Profile) error {
	f.mu.Lock()
	block := f.verifyBlock
	f.mu.Unlock()
	if block != nil {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-block:
		}
	}
	return ctx.Err()
}
func (f *fakeAdapter) Disconnect(ctx context.Context, p profile.Profile) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.stops[p.Name]++
	if f.failStop {
		return fmt.Errorf("synthetic-sensitive-adapter-output")
	}
	if ctx.Err() != nil {
		return ctx.Err()
	}
	f.sessions[p.Name] = false
	return nil
}
func (f *fakeAdapter) CanRecover(profile.Profile) bool { return true }
func (f *fakeAdapter) counts(name string) (int, int, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.connects[name], f.stops[name], f.sessions[name]
}
func fixture(name string) profile.Snapshot {
	return profile.Snapshot{Profile: profile.Profile{SchemaVersion: 1, Name: name, Backend: profile.Backend{Type: profile.L2TP, L2TP: &profile.L2TPConfig{Server: "vpn.example.com"}}, Health: profile.TCPHealth{Host: "192.0.2.10", Port: 22}}, Revision: 1, CredentialsPresent: true}
}
func newManager(t *testing.T, root string, adapter *fakeAdapter, options Options) *Manager {
	t.Helper()
	store, err := operationstore.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	options.Adapter = adapter
	var profileMutex sync.Mutex
	options.WithProfile = func(ctx context.Context, name string, fn func(profile.Snapshot) error) error {
		profileMutex.Lock()
		defer profileMutex.Unlock()
		return fn(fixture(name))
	}
	manager, err := Open(context.Background(), store, options)
	if err != nil {
		store.Close()
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = manager.Close() })
	return manager
}
func waitState(t *testing.T, m *Manager, id string, state operation.State) operation.Record {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		r, err := m.Status(id)
		if err != nil {
			t.Fatal(err)
		}
		if r.State == state {
			return r
		}
		time.Sleep(3 * time.Millisecond)
	}
	r, _ := m.Status(id)
	t.Fatalf("operation stayed %s, wanted %s", r.State, state)
	return r
}
func waitIdle(t *testing.T, m *Manager) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err := m.WaitIdle(ctx); err != nil {
		t.Fatal(err)
	}
}
func TestConcurrentEnsuresJoinAndReuseEstablishedSession(t *testing.T) {
	adapter := newAdapter()
	release := make(chan struct{})
	adapter.block = release
	m := newManager(t, filepath.Join(t.TempDir(), "state"), adapter, Options{})
	var tasks sync.WaitGroup
	ids := make(chan string, 32)
	for range 32 {
		tasks.Go(func() {
			r, err := m.Ensure(context.Background(), "example")
			if err != nil {
				t.Error(err)
				return
			}
			ids <- r.ID
		})
	}
	tasks.Wait()
	close(ids)
	id := ""
	for got := range ids {
		if id != "" && id != got {
			t.Fatal("duplicate operation")
		}
		id = got
	}
	waitState(t, m, id, operation.Running)
	close(release)
	waitState(t, m, id, operation.Succeeded)
	next, err := m.Ensure(context.Background(), "example")
	if err != nil {
		t.Fatal(err)
	}
	waitState(t, m, next.ID, operation.Succeeded)
	count, _, connected := adapter.counts("example")
	if count != 1 || !connected {
		t.Fatal("established session not reused")
	}
}
func TestDifferentProfilesProceedIndependently(t *testing.T) {
	adapter := newAdapter()
	release := make(chan struct{})
	adapter.block = release
	m := newManager(t, filepath.Join(t.TempDir(), "state"), adapter, Options{})
	one, err := m.Ensure(context.Background(), "one")
	if err != nil {
		t.Fatal(err)
	}
	two, err := m.Ensure(context.Background(), "two")
	if err != nil {
		t.Fatal(err)
	}
	waitState(t, m, one.ID, operation.Running)
	waitState(t, m, two.ID, operation.Running)
	deadline := time.Now().Add(time.Second)
	for {
		a, _, _ := adapter.counts("one")
		b, _, _ := adapter.counts("two")
		if a == 1 && b == 1 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("profiles serialized external work")
		}
		time.Sleep(time.Millisecond)
	}
	close(release)
	waitState(t, m, one.ID, operation.Succeeded)
	waitState(t, m, two.ID, operation.Succeeded)
}
func TestDisconnectSupersedesRecoveryAndOldEnsure(t *testing.T) {
	adapter := newAdapter()
	release := make(chan struct{})
	adapter.block = release
	m := newManager(t, filepath.Join(t.TempDir(), "state"), adapter, Options{})
	old, err := m.Ensure(context.Background(), "example")
	if err != nil {
		t.Fatal(err)
	}
	waitState(t, m, old.ID, operation.Running)
	disconnected, err := m.Disconnect(context.Background(), "example")
	if err != nil {
		t.Fatal(err)
	}
	waitState(t, m, disconnected.ID, operation.Succeeded)
	waitState(t, m, old.ID, operation.Cancelled)
	if _, err = m.Recover(context.Background(), "example"); !errors.Is(err, ErrIntent) {
		t.Fatal("recovery ignored disconnect", err)
	}
	c, err := m.Connection("example")
	if err != nil || c.Desired != operation.Disconnected || c.Observed != operation.ObservedDisconnected || c.RecoveryEnabled {
		t.Fatal("incorrect disconnected intent", err)
	}
	_, _, connected := adapter.counts("example")
	if connected {
		t.Fatal("old worker reconnected")
	}
}
func TestCancelDoesNotDisconnectExistingSession(t *testing.T) {
	adapter := newAdapter()
	m := newManager(t, filepath.Join(t.TempDir(), "state"), adapter, Options{})
	first, err := m.Ensure(context.Background(), "example")
	if err != nil {
		t.Fatal(err)
	}
	waitState(t, m, first.ID, operation.Succeeded)
	adapter.mu.Lock()
	adapter.verifyBlock = make(chan struct{})
	adapter.mu.Unlock()
	next, err := m.Ensure(context.Background(), "example")
	if err != nil {
		t.Fatal(err)
	}
	waitState(t, m, next.ID, operation.Verifying)
	if _, err = m.Cancel(next.ID); err != nil {
		t.Fatal(err)
	}
	waitIdle(t, m)
	count, stops, connected := adapter.counts("example")
	if count != 1 || stops != 0 || !connected {
		t.Fatal("cancel disconnected an established session")
	}
	c, _ := m.Connection("example")
	if c.Desired != operation.Connected || c.RecoveryEnabled {
		t.Fatal("cancel silently permitted recovery")
	}
}
func TestMFAConsumptionExpiryAndRepeatAreBounded(t *testing.T) {
	t.Run("single-use", func(t *testing.T) {
		adapter := newAdapter()
		adapter.mfa = true
		m := newManager(t, filepath.Join(t.TempDir(), "state"), adapter, Options{})
		r, err := m.Ensure(context.Background(), "example")
		if err != nil {
			t.Fatal(err)
		}
		r = waitState(t, m, r.ID, operation.AwaitingMFA)
		if err = m.SubmitMFA(r.ID, "mfa-"+strings.Repeat("0", 32), secret.New("9876543210")); !errors.Is(err, ErrChallenge) {
			t.Fatal("wrong challenge accepted")
		}
		var tasks sync.WaitGroup
		out := make(chan error, 2)
		for range 2 {
			tasks.Go(func() { out <- m.SubmitMFA(r.ID, r.Challenge.ID, secret.New("9876543210")) })
		}
		tasks.Wait()
		close(out)
		accepted := 0
		for err := range out {
			if err == nil {
				accepted++
			}
		}
		if accepted != 1 {
			t.Fatal("code not consumed atomically")
		}
		waitState(t, m, r.ID, operation.Succeeded)
		state, err := m.journal.Load()
		if err != nil {
			t.Fatal(err)
		}
		encoded, _ := json.Marshal(state)
		if strings.Contains(string(encoded), "9876543210") {
			t.Fatal("MFA code persisted")
		}
	})
	for _, test := range []struct {
		name    string
		repeat  bool
		failure operation.Failure
	}{{"expiry", false, operation.MFAExpired}, {"repeated-prompt", true, operation.AuthenticationFailed}} {
		t.Run(test.name, func(t *testing.T) {
			adapter := newAdapter()
			adapter.mfa = true
			adapter.repeat = test.repeat
			m := newManager(t, filepath.Join(t.TempDir(), "state"), adapter, Options{MFATimeout: 500 * time.Millisecond})
			r, err := m.Ensure(context.Background(), "example")
			if err != nil {
				t.Fatal(err)
			}
			r = waitState(t, m, r.ID, operation.AwaitingMFA)
			if test.repeat {
				if err = m.SubmitMFA(r.ID, r.Challenge.ID, secret.New("9876543210")); err != nil {
					t.Fatal(err)
				}
			}
			failed := waitState(t, m, r.ID, operation.Failed)
			if failed.Failure != test.failure || failed.Challenge != nil {
				t.Fatal("wrong MFA terminal outcome")
			}
			if count, _, _ := adapter.counts("example"); count != 1 {
				t.Fatal("MFA retried")
			}
		})
	}
}
func TestTimeoutAndSlowSubscriptions(t *testing.T) {
	adapter := newAdapter()
	adapter.block = make(chan struct{})
	m := newManager(t, filepath.Join(t.TempDir(), "state"), adapter, Options{OperationTimeout: 200 * time.Millisecond})
	r, err := m.Ensure(context.Background(), "example")
	if err != nil {
		t.Fatal(err)
	}
	snapshot, updates, release, err := m.Subscribe(r.ID)
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	failed := waitState(t, m, r.ID, operation.Failed)
	if failed.Failure != operation.TimedOut {
		t.Fatal("timeout unclassified")
	}
	latest := <-updates
	if latest.Revision <= snapshot.Revision || latest.State != operation.Failed {
		t.Fatal("slow subscriber blocked/lost latest snapshot")
	}
}

type lateAdapter struct{ *fakeAdapter }

func (f lateAdapter) Connect(_ context.Context, p profile.Profile, challenge func(context.Context) (secret.Value, error)) error {
	time.Sleep(80 * time.Millisecond)
	return f.fakeAdapter.Connect(context.Background(), p, challenge)
}
func TestLateAdapterCompletionCannotPublishSuccessPastDeadline(t *testing.T) {
	root := filepath.Join(t.TempDir(), "state")
	journal, err := operationstore.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	adapter := newAdapter()
	m, err := Open(context.Background(), journal, Options{Adapter: lateAdapter{adapter}, OperationTimeout: 30 * time.Millisecond, WithProfile: func(_ context.Context, name string, fn func(profile.Snapshot) error) error { return fn(fixture(name)) }})
	if err != nil {
		journal.Close()
		t.Fatal(err)
	}
	defer m.Close()
	r, err := m.Ensure(context.Background(), "example")
	if err != nil {
		t.Fatal(err)
	}
	failed := waitState(t, m, r.ID, operation.Failed)
	if failed.Failure != operation.TimedOut {
		t.Fatal("late success accepted")
	}
	if _, _, connected := adapter.counts("example"); connected {
		t.Fatal("late new attempt not cleaned up")
	}
}
