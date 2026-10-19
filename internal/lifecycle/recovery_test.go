package lifecycle

import (
	"context"
	"errors"
	"path/filepath"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/derrickhayashi/vpn-sidecars-next/internal/operation"
	"github.com/derrickhayashi/vpn-sidecars-next/internal/operationstore"
	"github.com/derrickhayashi/vpn-sidecars-next/internal/profile"
	"github.com/derrickhayashi/vpn-sidecars-next/internal/secret"
)

type recoveryAdapter struct {
	*fakeAdapter
	mu                                    sync.Mutex
	health                                map[string]operation.HealthState
	forwarding                            map[string]bool
	events                                []string
	probeBlock                            <-chan struct{}
	probeStarted                          chan struct{}
	probeError, repairError, quiesceError error
}

func newRecoveryAdapter() *recoveryAdapter {
	return &recoveryAdapter{fakeAdapter: newAdapter(), health: map[string]operation.HealthState{}, forwarding: map[string]bool{}}
}
func (a *recoveryAdapter) Check(ctx context.Context, p profile.Profile) (operation.HealthState, error) {
	a.mu.Lock()
	block, started, err := a.probeBlock, a.probeStarted, a.probeError
	health, forwarding := a.health[p.Name], a.forwarding[p.Name]
	a.mu.Unlock()
	if started != nil {
		select {
		case started <- struct{}{}:
		default:
		}
	}
	if block != nil {
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		case <-block:
		}
	}
	if err != nil {
		return "", err
	}
	if health != "" {
		return health, nil
	}
	_, _, connected := a.counts(p.Name)
	if !connected {
		return operation.HealthStopped, nil
	}
	if !forwarding {
		return operation.HealthDetached, nil
	}
	return operation.HealthHealthy, nil
}
func (a *recoveryAdapter) Quiesce(ctx context.Context, p profile.Profile) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if a.quiesceError != nil {
		return a.quiesceError
	}
	a.events = append(a.events, "quiesce")
	a.forwarding[p.Name] = false
	return nil
}
func (a *recoveryAdapter) RepairForwarding(ctx context.Context, p profile.Profile) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	if ctx.Err() != nil {
		return ctx.Err()
	}
	a.events = append(a.events, "repair")
	if a.repairError != nil {
		a.forwarding[p.Name] = false
		return a.repairError
	}
	a.forwarding[p.Name] = true
	return nil
}
func (a *recoveryAdapter) Connect(ctx context.Context, p profile.Profile, challenge func(context.Context) (secret.Value, error)) error {
	a.mu.Lock()
	a.events = append(a.events, "connect")
	a.mu.Unlock()
	if err := a.fakeAdapter.Connect(ctx, p, challenge); err != nil {
		return err
	}
	a.mu.Lock()
	delete(a.health, p.Name)
	a.mu.Unlock()
	return nil
}
func (a *recoveryAdapter) Disconnect(ctx context.Context, p profile.Profile) error {
	a.mu.Lock()
	a.events = append(a.events, "disconnect")
	a.forwarding[p.Name] = false
	a.mu.Unlock()
	return a.fakeAdapter.Disconnect(ctx, p)
}
func (a *recoveryAdapter) setHealth(name string, health operation.HealthState) {
	a.mu.Lock()
	a.health[name] = health
	a.events = nil
	a.mu.Unlock()
	if health == operation.HealthTunnelDown {
		a.fakeAdapter.mu.Lock()
		a.sessions[name] = false
		a.fakeAdapter.mu.Unlock()
	}
}
func (a *recoveryAdapter) snapshot(name string) (bool, []string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.forwarding[name], append([]string(nil), a.events...)
}
func recoveryOptions() Options {
	return Options{Recovery: RecoveryPolicy{Interval: time.Hour, ProbeTimeout: time.Second, Cooldown: time.Hour, MaxBackoff: 4 * time.Hour}}
}
func openRecoveryManager(t *testing.T, root string, a *recoveryAdapter, options Options) *Manager {
	t.Helper()
	j, err := operationstore.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	options.Adapter = a
	options.WithProfile = func(ctx context.Context, name string, fn func(profile.Snapshot) error) error {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return fn(fixture(name))
	}
	m, err := Open(context.Background(), j, options)
	if err != nil {
		j.Close()
		t.Fatal(err)
	}
	t.Cleanup(func() { m.Close() })
	return m
}
func ensureRecovery(t *testing.T, m *Manager, name string) operation.Connection {
	t.Helper()
	r, err := m.Ensure(context.Background(), name)
	if err != nil {
		t.Fatal(err)
	}
	waitState(t, m, r.ID, operation.Succeeded)
	if err = m.WaitIdle(context.Background()); err != nil {
		t.Fatal(err)
	}
	c, err := m.Connection(name)
	if err != nil {
		t.Fatal(err)
	}
	return c
}
func expireCooldown(t *testing.T, m *Manager, name string) {
	t.Helper()
	m.mu.Lock()
	defer m.mu.Unlock()
	next := m.state.Clone()
	c := next.Connections[name]
	c.Recovery.NotBefore = time.Now().Add(-time.Second)
	next.Connections[name] = c
	if err := m.commitLocked(next); err != nil {
		t.Fatal(err)
	}
}
func probeRecovery(t *testing.T, m *Manager, name string) operation.Connection {
	t.Helper()
	c, err := m.Connection(name)
	if err != nil {
		t.Fatal(err)
	}
	m.wait.Add(1)
	m.monitor(c)
	if err := m.WaitIdle(context.Background()); err != nil {
		t.Fatal(err)
	}
	c, err = m.Connection(name)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func TestRecoveryClassifiesHealthWithoutUnnecessaryLogins(t *testing.T) {
	for _, health := range []operation.HealthState{operation.HealthHealthy, operation.HealthStarting, operation.HealthServiceUnavailable, operation.HealthUnavailable, operation.HealthStopped, operation.HealthOwnerConflict, "untrusted-backend-output"} {
		t.Run(string(health), func(t *testing.T) {
			a := newRecoveryAdapter()
			m := openRecoveryManager(t, filepath.Join(t.TempDir(), "state"), a, recoveryOptions())
			ensureRecovery(t, m, "example")
			expireCooldown(t, m, "example")
			a.setHealth("example", health)
			c := probeRecovery(t, m, "example")
			if n, stops, _ := a.counts("example"); n != 1 || stops != 0 {
				t.Fatal("probe initiated reauthentication")
			}
			if _, events := a.snapshot("example"); len(events) != 0 {
				t.Fatal("probe changed runtime", events)
			}
			want := health
			if !health.Valid() {
				want = operation.HealthUnavailable
			}
			if c.Recovery.Health != want || c.Recovery.CheckedAt.IsZero() {
				t.Fatal("health classification lost")
			}
			if (health == operation.HealthStopped || health == operation.HealthOwnerConflict) && c.RecoveryEnabled {
				t.Fatal("conflicting/stopped runtime allowed recovery")
			}
		})
	}
}
func TestRecoveryRestartsTunnelOnlyAfterQuiescingAndReattachesNamespace(t *testing.T) {
	for _, health := range []operation.HealthState{operation.HealthTunnelDown, operation.HealthDetached} {
		t.Run(string(health), func(t *testing.T) {
			a := newRecoveryAdapter()
			m := openRecoveryManager(t, filepath.Join(t.TempDir(), "state"), a, recoveryOptions())
			ensureRecovery(t, m, "example")
			expireCooldown(t, m, "example")
			a.setHealth("example", health)
			c := probeRecovery(t, m, "example")
			if c.Observed != operation.ObservedConnected || !c.RecoveryEnabled || c.Recovery.Attempts != 0 || !time.Now().Before(c.Recovery.NotBefore) {
				t.Fatal("recovery did not settle with cooldown", c)
			}
			r, _ := m.Status(c.LastOperation)
			if r.Kind != operation.RecoverConnection || r.State != operation.Succeeded {
				t.Fatal("repair missing from operation history", r)
			}
			forwarding, events := a.snapshot("example")
			want := []string{"quiesce", "repair"}
			if health == operation.HealthTunnelDown {
				want = []string{"quiesce", "disconnect", "connect", "repair"}
			}
			if !forwarding || !reflect.DeepEqual(events, want) {
				t.Fatal("unsafe repair order", events)
			}
		})
	}
}
func TestRecoveryBudgetBackoffAndCooldownSurviveRestart(t *testing.T) {
	root := filepath.Join(t.TempDir(), "state")
	a := newRecoveryAdapter()
	m := openRecoveryManager(t, root, a, recoveryOptions())
	ensureRecovery(t, m, "example")
	a.setHealth("example", operation.HealthTunnelDown)
	before, _ := m.Connection("example")
	c := probeRecovery(t, m, "example")
	if c.LastOperation != before.LastOperation {
		t.Fatal("ignored cooldown")
	}
	a.fakeAdapter.mu.Lock()
	a.connectError = errors.New("synthetic-sensitive-error")
	a.fakeAdapter.mu.Unlock()
	for attempt := uint8(1); attempt <= operation.MaxRecoveryAttempts; attempt++ {
		expireCooldown(t, m, "example")
		start := time.Now()
		c = probeRecovery(t, m, "example")
		r, _ := m.Status(c.LastOperation)
		if r.State != operation.Failed || c.Recovery.Attempts != attempt || c.Recovery.NotBefore.Before(start.Add(m.options.Recovery.backoff(attempt-1))) {
			t.Fatal("unbounded or unjournaled retry", c, r)
		}
		if forwarding, _ := a.snapshot("example"); forwarding {
			t.Fatal("failed recovery left forwarding on")
		}
		id := c.LastOperation
		probeRecovery(t, m, "example")
		c, _ = m.Connection("example")
		if c.LastOperation != id {
			t.Fatal("retried inside cooldown")
		}
	}
	if c.RecoveryEnabled {
		t.Fatal("failure budget exhausted but recovery enabled")
	}
	if n, _, _ := a.counts("example"); n != 4 {
		t.Fatal("wrong bounded login count", n)
	}
	if err := m.Close(); err != nil {
		t.Fatal(err)
	}
	reopened := openRecoveryManager(t, root, a, recoveryOptions())
	persisted, _ := reopened.Connection("example")
	if persisted.Recovery.Attempts != c.Recovery.Attempts || !persisted.Recovery.NotBefore.Equal(c.Recovery.NotBefore) {
		t.Fatal("restart erased budget/cooldown")
	}
	reopened.StartReconcile()
	waitConnection(t, reopened, "example", func(c operation.Connection) bool { return c.Observed == operation.ObservedDisconnected })
	if _, err := reopened.Recover(context.Background(), "example"); !errors.Is(err, ErrIntent) {
		t.Fatal("restart granted recovery permission")
	}
	// A later explicit ensure is the operator's fresh decision and resets budget.
	a.fakeAdapter.mu.Lock()
	a.connectError = nil
	a.fakeAdapter.mu.Unlock()
	c = ensureRecovery(t, reopened, "example")
	if c.Recovery.Attempts != 0 || !c.RecoveryEnabled {
		t.Fatal("explicit ensure did not rearm")
	}
}
func TestRecoveryProbeCannotReverseDisconnectOrNewEnsure(t *testing.T) {
	for _, nextEnsure := range []bool{false, true} {
		t.Run(map[bool]string{false: "disconnect", true: "ensure"}[nextEnsure], func(t *testing.T) {
			a := newRecoveryAdapter()
			m := openRecoveryManager(t, filepath.Join(t.TempDir(), "state"), a, recoveryOptions())
			c := ensureRecovery(t, m, "example")
			expireCooldown(t, m, "example")
			c, _ = m.Connection("example")
			a.setHealth("example", operation.HealthTunnelDown)
			block, started := make(chan struct{}), make(chan struct{}, 1)
			a.mu.Lock()
			a.probeBlock = block
			a.probeStarted = started
			a.mu.Unlock()
			m.wait.Add(1)
			done := make(chan struct{})
			go func() { m.monitor(c); close(done) }()
			select {
			case <-started:
			case <-time.After(time.Second):
				t.Fatal("probe never started")
			}
			var r operation.Record
			var err error
			if nextEnsure {
				r, err = m.Ensure(context.Background(), "example")
			} else {
				r, err = m.Disconnect(context.Background(), "example")
			}
			if err != nil {
				t.Fatal(err)
			}
			close(block)
			<-done
			waitState(t, m, r.ID, operation.Succeeded)
			if n, _, _ := a.counts("example"); n != 1+map[bool]int{false: 0, true: 1}[nextEnsure] {
				t.Fatal("stale probe created login", n)
			}
			current, _ := m.Connection("example")
			if !nextEnsure && (current.Desired != operation.Disconnected || current.RecoveryEnabled) {
				t.Fatal("probe reversed stop")
			}
		})
	}
}
func TestRecoveryNeverRequestsMFAAndAuthenticationFailureStopsRetries(t *testing.T) {
	for _, failure := range []error{nil, ErrAuthentication} {
		a := newRecoveryAdapter()
		m := openRecoveryManager(t, filepath.Join(t.TempDir(), "state"), a, recoveryOptions())
		ensureRecovery(t, m, "example")
		expireCooldown(t, m, "example")
		a.setHealth("example", operation.HealthTunnelDown)
		a.fakeAdapter.mu.Lock()
		a.mfa = failure == nil
		a.connectError = failure
		a.fakeAdapter.mu.Unlock()
		c := probeRecovery(t, m, "example")
		r, _ := m.Status(c.LastOperation)
		if r.State != operation.Failed || r.Challenge != nil {
			t.Fatal("automatic recovery issued challenge")
		}
		if failure != nil && c.RecoveryEnabled {
			t.Fatal("authentication failure retried")
		}
	}
}
func TestRecoveryQuiesceFailurePreventsTunnelMutation(t *testing.T) {
	a := newRecoveryAdapter()
	m := openRecoveryManager(t, filepath.Join(t.TempDir(), "state"), a, recoveryOptions())
	ensureRecovery(t, m, "example")
	expireCooldown(t, m, "example")
	a.setHealth("example", operation.HealthTunnelDown)
	a.mu.Lock()
	a.quiesceError = errors.New("synthetic-owner-conflict")
	a.mu.Unlock()
	c := probeRecovery(t, m, "example")
	r, _ := m.Status(c.LastOperation)
	if r.State != operation.Failed {
		t.Fatal("ignored failure to stop application")
	}
	if n, stops, _ := a.counts("example"); n != 1 || stops != 0 {
		t.Fatal("mutated tunnel before stopping forwarding")
	}
}
func TestRecoveryMonitorStartsOnceAndHonorsStop(t *testing.T) {
	a := newRecoveryAdapter()
	options := recoveryOptions()
	options.Recovery.Interval = 5 * time.Millisecond
	options.Recovery.Cooldown = time.Nanosecond
	m := openRecoveryManager(t, filepath.Join(t.TempDir(), "state"), a, options)
	m.StartReconcile()
	ensureRecovery(t, m, "example")
	a.setHealth("example", operation.HealthTunnelDown)
	m.StartReconcile()
	m.StartReconcile()
	waitConnection(t, m, "example", func(c operation.Connection) bool {
		r, _ := m.Status(c.LastOperation)
		return r.Kind == operation.RecoverConnection && r.State == operation.Succeeded
	})
	r, err := m.Disconnect(context.Background(), "example")
	if err != nil {
		t.Fatal(err)
	}
	waitState(t, m, r.ID, operation.Succeeded)
	a.setHealth("example", operation.HealthTunnelDown)
	time.Sleep(30 * time.Millisecond)
	if n, _, _ := a.counts("example"); n != 2 {
		t.Fatal("background loop reversed intentional disconnect", n)
	}
}

func TestRecoveryCooldownPersistsBesideEstablishedTunnel(t *testing.T) {
	root := filepath.Join(t.TempDir(), "state")
	a := newRecoveryAdapter()
	m := openRecoveryManager(t, root, a, recoveryOptions())
	ensureRecovery(t, m, "example")
	expireCooldown(t, m, "example")
	a.setHealth("example", operation.HealthDetached)
	a.mu.Lock()
	a.repairError = errors.New("synthetic-sensitive-repair-output")
	a.mu.Unlock()
	before := probeRecovery(t, m, "example")
	if before.Recovery.Attempts != 1 || !before.RecoveryEnabled {
		t.Fatal("repair failure lost bounded retry permission")
	}
	if forwarding, _ := a.snapshot("example"); forwarding {
		t.Fatal("failed namespace repair left forwarding on")
	}
	if n, stops, established := a.counts("example"); n != 1 || stops != 0 || !established {
		t.Fatal("namespace failure reauthenticated tunnel")
	}
	if err := m.Close(); err != nil {
		t.Fatal(err)
	}
	reopened := openRecoveryManager(t, root, a, recoveryOptions())
	reopened.StartReconcile()
	c := waitConnection(t, reopened, "example", func(c operation.Connection) bool { return c.RecoveryEnabled })
	if c.Recovery.Attempts != 1 || !c.Recovery.NotBefore.Equal(before.Recovery.NotBefore) {
		t.Fatal("restart bypassed cooldown")
	}
	a.mu.Lock()
	a.repairError = nil
	a.events = nil
	a.mu.Unlock()
	probeRecovery(t, reopened, "example")
	if _, events := a.snapshot("example"); len(events) != 0 {
		t.Fatal("repair ran during saved cooldown", events)
	}
}
func TestRecoveryPersistenceFailurePreventsExternalActions(t *testing.T) {
	for _, after := range []bool{false, true} {
		for _, failurePoint := range []string{"probe", "submission"} {
			t.Run(map[bool]string{false: "before", true: "uncertain"}[after]+"-"+failurePoint, func(t *testing.T) {
				root := filepath.Join(t.TempDir(), "state")
				a := newRecoveryAdapter()
				j, err := operationstore.Open(root)
				if err != nil {
					t.Fatal(err)
				}
				failing := &failingJournal{Journal: j}
				options := recoveryOptions()
				options.Adapter = a
				options.WithProfile = func(_ context.Context, name string, fn func(profile.Snapshot) error) error { return fn(fixture(name)) }
				m, err := Open(context.Background(), failing, options)
				if err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { m.Close() })
				ensureRecovery(t, m, "example")
				expireCooldown(t, m, "example")
				a.setHealth("example", operation.HealthTunnelDown)
				failing.arm(after)
				if failurePoint == "probe" {
					c, _ := m.Connection("example")
					m.wait.Add(1)
					m.monitor(c)
				} else {
					if _, err = m.Recover(context.Background(), "example"); !errors.Is(err, ErrPersistence) {
						t.Fatal("lost persistence failure", err)
					}
				}
				if m.Ready() {
					t.Fatal("continued after journal failure")
				}
				if _, events := a.snapshot("example"); len(events) != 0 {
					t.Fatal("external action without durable permission", events)
				}
				if err = m.Close(); err != nil {
					t.Fatal(err)
				}
				reopened := openRecoveryManager(t, root, a, recoveryOptions())
				reopened.StartReconcile()
				waitConnection(t, reopened, "example", func(c operation.Connection) bool { return c.Observed == operation.ObservedDisconnected })
				if n, _, _ := a.counts("example"); n != 1 {
					t.Fatal("restart retried uncertain recovery")
				}
			})
		}
	}
}
func TestRecoveryCancellationAndDisconnectStopPendingAttempt(t *testing.T) {
	for _, disconnect := range []bool{false, true} {
		t.Run(map[bool]string{false: "cancel", true: "disconnect"}[disconnect], func(t *testing.T) {
			a := newRecoveryAdapter()
			m := openRecoveryManager(t, filepath.Join(t.TempDir(), "state"), a, recoveryOptions())
			ensureRecovery(t, m, "example")
			expireCooldown(t, m, "example")
			a.setHealth("example", operation.HealthTunnelDown)
			a.fakeAdapter.mu.Lock()
			a.block = make(chan struct{})
			a.fakeAdapter.mu.Unlock()
			r, err := m.Recover(context.Background(), "example")
			if err != nil {
				t.Fatal(err)
			}
			deadline := time.Now().Add(time.Second)
			for {
				n, _, _ := a.counts("example")
				if n == 2 {
					break
				}
				if time.Now().After(deadline) {
					t.Fatal("attempt did not start")
				}
				time.Sleep(time.Millisecond)
			}
			if disconnect {
				stopped, err := m.Disconnect(context.Background(), "example")
				if err != nil {
					t.Fatal(err)
				}
				waitState(t, m, stopped.ID, operation.Succeeded)
			} else {
				if _, err = m.Cancel(r.ID); err != nil {
					t.Fatal(err)
				}
			}
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			if err = m.WaitIdle(ctx); err != nil {
				t.Fatal(err)
			}
			c, _ := m.Connection("example")
			if c.RecoveryEnabled {
				t.Fatal("stale attempt reenabled recovery")
			}
			if forwarding, _ := a.snapshot("example"); forwarding {
				t.Fatal("cancelled recovery left forwarding on")
			}
			if _, _, established := a.counts("example"); established {
				t.Fatal("cancelled new login remained usable")
			}
		})
	}
}
func TestRecoveryProbeConcurrencyAndShutdownAreBounded(t *testing.T) {
	a := newRecoveryAdapter()
	opts := recoveryOptions()
	opts.Recovery.Interval = 5 * time.Millisecond
	m := openRecoveryManager(t, filepath.Join(t.TempDir(), "state"), a, opts)
	m.StartReconcile()
	for _, name := range []string{"one", "two", "three", "four", "five", "six"} {
		ensureRecovery(t, m, name)
	}
	a.mu.Lock()
	a.probeBlock = make(chan struct{})
	a.probeStarted = make(chan struct{}, 16)
	started := a.probeStarted
	a.mu.Unlock()
	m.StartReconcile()
	// Startup's read-only Observe/Verify checks do not use Check.
	for range 4 {
		select {
		case <-started:
		case <-time.After(time.Second):
			t.Fatal("probes did not start")
		}
	}
	select {
	case <-started:
		t.Fatal("probe concurrency exceeded four")
	case <-time.After(30 * time.Millisecond):
	}
	done := make(chan struct{})
	go func() { m.Close(); close(done) }()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("shutdown did not cancel bounded probes")
	}
}
