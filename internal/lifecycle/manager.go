// Package lifecycle owns service-lived operations independently of SSH requests.
package lifecycle

import (
	"context"
	"errors"
	"math"
	"sort"
	"sync"
	"time"

	"github.com/derrickhayashi/vpn-sidecars-next/internal/operation"
	"github.com/derrickhayashi/vpn-sidecars-next/internal/operationstore"
	"github.com/derrickhayashi/vpn-sidecars-next/internal/privatefile"
	"github.com/derrickhayashi/vpn-sidecars-next/internal/profile"
	"github.com/derrickhayashi/vpn-sidecars-next/internal/secret"
)

var (
	ErrUnavailable    = errors.New("connection backend unavailable")
	ErrCredentials    = errors.New("credentials unavailable or invalid")
	ErrNotFound       = errors.New("operation not found")
	ErrBusy           = errors.New("operation in progress")
	ErrIntent         = errors.New("connection intent does not allow recovery")
	ErrChallenge      = errors.New("invalid or consumed MFA challenge")
	ErrExpired        = errors.New("MFA challenge expired")
	ErrAuthentication = errors.New("authentication failed")
	ErrPersistence    = operationstore.ErrStorage
	ErrClosed         = errors.New("operation service stopping")
)

// Adapter implementations must honor context cancellation, bound subprocesses,
// make Disconnect idempotent, and keep errors/output free of credential data.
// Observe distinguishes an established tunnel from private-service verification.
// CanRecover must be a pure, fast policy check.
type Adapter interface {
	Observe(context.Context, profile.Profile) (bool, error)
	Connect(context.Context, profile.Profile, func(context.Context) (secret.Value, error)) error
	Verify(context.Context, profile.Profile) error
	Disconnect(context.Context, profile.Profile) error
	CanRecover(profile.Profile) bool
}

// WithProfile holds the profile store lock while invoking fn. Lock order is
// profile transaction -> manager mutex -> journal transaction, never reversed.
type WithProfile func(context.Context, string, func(profile.Snapshot) error) error
type Options struct {
	Adapter                                      Adapter
	WithProfile                                  WithProfile
	OperationTimeout, MFATimeout, CleanupTimeout time.Duration
}
type slot struct{ gate chan struct{} }
type worker struct {
	id            string
	generation    uint64
	p             profile.Profile
	kind          operation.Kind
	ctx           context.Context
	cancel        context.CancelFunc
	code          chan secret.Value
	challengeUsed bool
}
type Manager struct {
	mu                            sync.Mutex
	journal                       operationstore.Journal
	state                         operationstore.State
	options                       Options
	ctx                           context.Context
	cancel                        context.CancelFunc
	closed, unhealthy, reconciled bool
	recoveryWanted                map[string]bool
	slots                         map[string]*slot
	workers                       map[string]*worker
	subscribers                   map[string]map[chan operation.Record]bool
	wait                          sync.WaitGroup
	closeOnce                     sync.Once
	closeDone                     chan struct{}
	closeErr                      error
}

func Open(parent context.Context, journal operationstore.Journal, options Options) (*Manager, error) {
	if options.OperationTimeout <= 0 {
		options.OperationTimeout = 3 * time.Minute
	}
	if options.MFATimeout <= 0 {
		options.MFATimeout = 90 * time.Second
	}
	if options.CleanupTimeout <= 0 {
		options.CleanupTimeout = 5 * time.Second
	}
	state, err := journal.Load()
	if err != nil {
		return nil, ErrPersistence
	}
	ctx, cancel := context.WithCancel(parent)
	m := &Manager{journal: journal, state: state, options: options, ctx: ctx, cancel: cancel, slots: map[string]*slot{}, workers: map[string]*worker{}, subscribers: map[string]map[chan operation.Record]bool{}, recoveryWanted: map[string]bool{}, closeDone: make(chan struct{})}
	next := state.Clone()
	now := time.Now().UTC()
	for id, r := range next.Operations {
		if !r.State.Terminal() {
			r.State = operation.Failed
			r.Failure = operation.Interrupted
			r.Challenge = nil
			r.Revision++
			r.UpdatedAt = now
			next.Operations[id] = r
		}
	}
	for name, c := range next.Connections {
		m.recoveryWanted[name] = c.RecoveryEnabled
		c.ActiveOperation = ""
		c.Observed = operation.Unknown
		c.RecoveryEnabled = false
		c.Revision++
		next.Connections[name] = c
	}
	if err = m.commitLocked(next); err != nil {
		cancel()
		return nil, err
	}
	return m, nil
}
func (m *Manager) Ready() bool { m.mu.Lock(); defer m.mu.Unlock(); return m.checkLocked() == nil }
func (m *Manager) Close() error {
	m.closeOnce.Do(func() {
		m.mu.Lock()
		m.closed = true
		m.cancel()
		for _, w := range m.workers {
			w.cancel()
		}
		for _, subs := range m.subscribers {
			for ch := range subs {
				close(ch)
			}
		}
		m.subscribers = map[string]map[chan operation.Record]bool{}
		m.mu.Unlock()
		m.wait.Wait()
		m.closeErr = m.journal.Close()
		close(m.closeDone)
	})
	<-m.closeDone
	return m.closeErr
}

func (m *Manager) checkLocked() error {
	if m.unhealthy {
		return ErrPersistence
	}
	if m.closed || m.ctx.Err() != nil {
		return ErrClosed
	}
	return nil
}
func (m *Manager) commitLocked(next operationstore.State) error {
	if m.unhealthy {
		return ErrPersistence
	}
	m.prune(&next)
	if next.Validate() != nil || m.journal.Save(next) != nil {
		m.unhealthy = true
		for _, w := range m.workers {
			w.cancel()
		}
		return ErrPersistence
	}
	old := m.state
	m.state = next
	for id, r := range next.Operations {
		if before, ok := old.Operations[id]; ok && before.Revision == r.Revision {
			continue
		}
		for ch := range m.subscribers[id] {
			copy := copyRecord(r)
			select {
			case ch <- copy:
			default:
				select {
				case <-ch:
				default:
				}
				select {
				case ch <- copy:
				default:
				}
			}
		}
	}
	return nil
}
func (m *Manager) prune(state *operationstore.State) {
	if len(state.Operations) <= operationstore.MaxOperations {
		return
	}
	pinned := map[string]bool{}
	for _, c := range state.Connections {
		pinned[c.ActiveOperation] = true
		pinned[c.LastOperation] = true
	}
	candidates := []operation.Record{}
	for _, r := range state.Operations {
		if r.State.Terminal() && !pinned[r.ID] && len(m.subscribers[r.ID]) == 0 {
			candidates = append(candidates, r)
		}
	}
	sort.Slice(candidates, func(i, j int) bool {
		a, b := candidates[i], candidates[j]
		if a.UpdatedAt.Equal(b.UpdatedAt) {
			return a.ID < b.ID
		}
		return a.UpdatedAt.Before(b.UpdatedAt)
	})
	for _, r := range candidates {
		if len(state.Operations) <= operationstore.MaxOperations {
			break
		}
		delete(state.Operations, r.ID)
	}
}
func copyRecord(r operation.Record) operation.Record {
	if r.Challenge != nil {
		c := *r.Challenge
		r.Challenge = &c
	}
	return r
}
func (m *Manager) Status(id string) (operation.Record, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := m.checkLocked(); err != nil {
		return operation.Record{}, err
	}
	r, ok := m.state.Operations[id]
	if !ok {
		return r, ErrNotFound
	}
	return copyRecord(r), nil
}
func (m *Manager) Connection(name string) (operation.Connection, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := m.checkLocked(); err != nil {
		return operation.Connection{}, err
	}
	if !profile.ValidName(name) {
		return operation.Connection{}, operation.ErrOperation
	}
	c, ok := m.state.Connections[name]
	if !ok {
		return operation.Connection{Profile: name, Generation: 1, Revision: 1, Desired: operation.Disconnected, Observed: operation.Unknown}, nil
	}
	return c, nil
}

// CheckInactive is called from inside a profile transaction. Desired connected
// intent blocks edits even when the last health observation is unknown.
func (m *Manager) CheckInactive(_ context.Context, name string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := m.checkLocked(); err != nil {
		return err
	}
	if c, ok := m.state.Connections[name]; ok && (c.ActiveOperation != "" || c.Desired == operation.Connected || c.Observed != operation.ObservedDisconnected) {
		return ErrBusy
	}
	return nil
}
func (m *Manager) Forget(name string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := m.checkLocked(); err != nil {
		return err
	}
	c, ok := m.state.Connections[name]
	if !ok {
		return nil
	}
	if c.ActiveOperation != "" || c.Desired != operation.Disconnected || c.Observed != operation.ObservedDisconnected {
		return ErrBusy
	}
	next := m.state.Clone()
	delete(next.Connections, name)
	if err := m.commitLocked(next); err != nil {
		return err
	}
	busy := false
	for _, w := range m.workers {
		if w.p.Name == name {
			busy = true
		}
	}
	if !busy {
		delete(m.slots, name)
		delete(m.recoveryWanted, name)
	}
	return nil
}
func (m *Manager) Ensure(ctx context.Context, name string) (operation.Record, error) {
	return m.submit(ctx, name, operation.EnsureConnection, 0)
}
func (m *Manager) Disconnect(ctx context.Context, name string) (operation.Record, error) {
	return m.submit(ctx, name, operation.DisconnectConnection, 0)
}
func (m *Manager) Recover(ctx context.Context, name string) (operation.Record, error) {
	return m.submit(ctx, name, operation.RecoverConnection, 0)
}
func (m *Manager) submit(ctx context.Context, name string, kind operation.Kind, expectedGeneration uint64) (operation.Record, error) {
	if !profile.ValidName(name) {
		return operation.Record{}, operation.ErrOperation
	}
	if m.options.WithProfile == nil {
		return operation.Record{}, ErrUnavailable
	}
	var result operation.Record
	err := m.options.WithProfile(ctx, name, func(snapshot profile.Snapshot) error {
		m.mu.Lock()
		defer m.mu.Unlock()
		if err := m.checkLocked(); err != nil {
			return err
		}
		if m.options.Adapter == nil {
			return ErrUnavailable
		}
		if kind != operation.DisconnectConnection && !snapshot.CredentialsPresent {
			return ErrCredentials
		}
		c, exists := m.state.Connections[name]
		if !exists {
			if len(m.state.Connections) >= operationstore.MaxConnections {
				return ErrBusy
			}
			c = operation.Connection{Profile: name, Revision: 1, Generation: 1, Desired: operation.Disconnected, Observed: operation.Unknown}
		}
		if expectedGeneration != 0 && (c.Generation != expectedGeneration || c.Desired != operation.Disconnected || c.ActiveOperation != "") {
			return ErrIntent
		}
		if kind == operation.RecoverConnection && (c.Desired != operation.Connected || !c.RecoveryEnabled || !m.options.Adapter.CanRecover(snapshot.Profile)) {
			return ErrIntent
		}
		if c.ActiveOperation != "" {
			active := m.state.Operations[c.ActiveOperation]
			if active.Kind == kind || kind == operation.EnsureConnection && active.Kind == operation.RecoverConnection {
				result = copyRecord(active)
				return nil
			}
			if kind != operation.DisconnectConnection {
				return ErrBusy
			}
		}
		if c.Generation == math.MaxUint64 || c.Revision == math.MaxUint64 {
			return ErrPersistence
		}
		token, err := privatefile.Token()
		if err != nil {
			return ErrPersistence
		}
		now := time.Now().UTC()
		r := operation.Record{ID: "op-" + token, Profile: name, ProfileRevision: snapshot.Revision, Kind: kind, State: operation.Queued, Revision: 1, CreatedAt: now, UpdatedAt: now, Deadline: now.Add(m.options.OperationTimeout)}
		next := m.state.Clone()
		previous := c.ActiveOperation
		if previous != "" {
			old := next.Operations[previous]
			old.State = operation.Cancelled
			old.Challenge = nil
			old.Failure = ""
			old.Revision++
			old.UpdatedAt = now
			next.Operations[previous] = old
		}
		c.Generation++
		c.Revision++
		c.ActiveOperation = r.ID
		c.LastOperation = r.ID
		c.RecoveryEnabled = false
		if kind == operation.DisconnectConnection {
			c.Desired = operation.Disconnected
		} else {
			c.Desired = operation.Connected
		}
		next.Connections[name] = c
		next.Operations[r.ID] = r
		if err = m.commitLocked(next); err != nil {
			return err
		}
		if w := m.workers[previous]; w != nil {
			w.cancel()
		}
		workerCtx, cancel := context.WithDeadline(m.ctx, r.Deadline)
		w := &worker{id: r.ID, generation: c.Generation, p: snapshot.Profile, kind: kind, ctx: workerCtx, cancel: cancel, code: make(chan secret.Value, 1)}
		m.workers[r.ID] = w
		entry := m.slots[name]
		if entry == nil {
			entry = &slot{gate: make(chan struct{}, 1)}
			m.slots[name] = entry
		}
		m.wait.Add(1)
		go m.run(w, entry)
		result = copyRecord(r)
		return nil
	})
	return result, err
}
func (m *Manager) transition(w *worker, state operation.State, failure operation.Failure, observed operation.Observed) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := m.checkLocked(); err != nil {
		return err
	}
	r, ok := m.state.Operations[w.id]
	c := m.state.Connections[w.p.Name]
	if !ok || r.State.Terminal() || c.ActiveOperation != w.id || c.Generation != w.generation {
		return ErrClosed
	}
	if state != operation.Failed && state != operation.Cancelled && w.ctx.Err() != nil {
		return w.ctx.Err()
	}
	if !operation.CanTransition(r.Kind, r.State, state) {
		return operation.ErrOperation
	}
	next := m.state.Clone()
	r.State = state
	r.Failure = failure
	r.Challenge = nil
	r.Revision++
	r.UpdatedAt = time.Now().UTC()
	next.Operations[r.ID] = r
	if observed != "" {
		c.Observed = observed
	}
	c.Revision++
	if state.Terminal() {
		c.ActiveOperation = ""
		c.RecoveryEnabled = state == operation.Succeeded && c.Desired == operation.Connected && m.options.Adapter.CanRecover(w.p)
	}
	next.Connections[c.Profile] = c
	return m.commitLocked(next)
}
func (m *Manager) Subscribe(id string) (operation.Record, <-chan operation.Record, func(), error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := m.checkLocked(); err != nil {
		return operation.Record{}, nil, nil, err
	}
	r, ok := m.state.Operations[id]
	if !ok {
		return r, nil, nil, ErrNotFound
	}
	count := 0
	for _, subs := range m.subscribers {
		count += len(subs)
	}
	if count >= 128 {
		return operation.Record{}, nil, nil, ErrBusy
	}
	ch := make(chan operation.Record, 1)
	if m.subscribers[id] == nil {
		m.subscribers[id] = map[chan operation.Record]bool{}
	}
	m.subscribers[id][ch] = true
	var once sync.Once
	cancel := func() {
		once.Do(func() {
			m.mu.Lock()
			defer m.mu.Unlock()
			if m.subscribers[id][ch] {
				delete(m.subscribers[id], ch)
				close(ch)
			}
			if len(m.subscribers[id]) == 0 {
				delete(m.subscribers, id)
			}
		})
	}
	return copyRecord(r), ch, cancel, nil
}
func (m *Manager) Cancel(id string) (operation.Record, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := m.checkLocked(); err != nil {
		return operation.Record{}, err
	}
	r, ok := m.state.Operations[id]
	if !ok {
		return r, ErrNotFound
	}
	if r.State.Terminal() {
		return copyRecord(r), nil
	}
	next := m.state.Clone()
	r.State = operation.Cancelled
	r.Challenge = nil
	r.Failure = ""
	r.Revision++
	r.UpdatedAt = time.Now().UTC()
	next.Operations[id] = r
	c := next.Connections[r.Profile]
	c.ActiveOperation = ""
	c.Revision++
	c.RecoveryEnabled = false
	c.Observed = operation.Unknown
	next.Connections[r.Profile] = c
	if err := m.commitLocked(next); err != nil {
		return operation.Record{}, err
	}
	if w := m.workers[id]; w != nil {
		w.cancel()
	}
	return copyRecord(r), nil
}
func (m *Manager) SubmitMFA(id, challenge string, code secret.Value) error {
	if !code.Check(func(v string) bool {
		if len(v) < 4 || len(v) > 10 {
			return false
		}
		for _, c := range v {
			if c < '0' || c > '9' {
				return false
			}
		}
		return true
	}) {
		return ErrChallenge
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := m.checkLocked(); err != nil {
		return err
	}
	r, ok := m.state.Operations[id]
	w := m.workers[id]
	if !ok || w == nil || w.ctx.Err() != nil || r.State != operation.AwaitingMFA || r.Challenge == nil || r.Challenge.ID != challenge {
		return ErrChallenge
	}
	if !time.Now().Before(r.Challenge.ExpiresAt) {
		return ErrExpired
	}
	next := m.state.Clone()
	r.State = operation.Running
	r.Challenge = nil
	r.Revision++
	r.UpdatedAt = time.Now().UTC()
	next.Operations[id] = r
	if err := m.commitLocked(next); err != nil {
		return err
	}
	w.code <- code
	return nil
}
func (m *Manager) challenge(w *worker, ctx context.Context) (secret.Value, error) {
	m.mu.Lock()
	if err := m.checkLocked(); err != nil {
		m.mu.Unlock()
		return secret.Value{}, err
	}
	r := m.state.Operations[w.id]
	c := m.state.Connections[w.p.Name]
	if w.kind != operation.EnsureConnection || w.challengeUsed || r.State != operation.Running || c.ActiveOperation != w.id {
		m.mu.Unlock()
		return secret.Value{}, ErrAuthentication
	}
	w.challengeUsed = true
	token, err := privatefile.Token()
	if err != nil {
		m.mu.Unlock()
		return secret.Value{}, ErrPersistence
	}
	expires := time.Now().Add(m.options.MFATimeout).UTC()
	if expires.After(r.Deadline) {
		expires = r.Deadline
	}
	next := m.state.Clone()
	r.State = operation.AwaitingMFA
	r.Challenge = &operation.MFAChallenge{ID: "mfa-" + token, ExpiresAt: expires}
	r.Revision++
	r.UpdatedAt = time.Now().UTC()
	next.Operations[r.ID] = r
	if err = m.commitLocked(next); err != nil {
		m.mu.Unlock()
		return secret.Value{}, err
	}
	m.mu.Unlock()
	timer := time.NewTimer(time.Until(expires))
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return secret.Value{}, ctx.Err()
	case <-w.ctx.Done():
		return secret.Value{}, w.ctx.Err()
	case <-timer.C:
		return secret.Value{}, ErrExpired
	case code := <-w.code:
		return code, nil
	}
}
