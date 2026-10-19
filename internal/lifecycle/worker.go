package lifecycle

import (
	"context"
	"errors"
	"time"

	"github.com/derrickhayashi/vpn-sidecars-next/internal/operation"
	"github.com/derrickhayashi/vpn-sidecars-next/internal/profile"
	"github.com/derrickhayashi/vpn-sidecars-next/internal/secret"
)

func (m *Manager) run(w *worker, entry *slot) {
	defer m.wait.Done()
	defer w.cancel()
	defer func() {
		m.mu.Lock()
		delete(m.workers, w.id)
		if _, exists := m.state.Connections[w.p.Name]; !exists {
			delete(m.slots, w.p.Name)
			delete(m.recoveryWanted, w.p.Name)
		}
		m.mu.Unlock()
		select {
		case <-w.code:
		default:
		}
	}()
	select {
	case entry.gate <- struct{}{}:
	case <-w.ctx.Done():
		m.fail(w, w.ctx.Err(), operation.Unknown)
		return
	}
	defer func() { <-entry.gate }()
	if w.ctx.Err() != nil {
		m.fail(w, w.ctx.Err(), operation.Unknown)
		return
	}
	if err := m.transition(w, operation.Running, "", operation.Unknown); err != nil {
		m.fail(w, err, operation.Unknown)
		return
	}
	if w.kind == operation.DisconnectConnection {
		err := m.options.Adapter.Disconnect(w.ctx, w.p)
		if err != nil || w.ctx.Err() != nil {
			clean := m.cleanup(w)
			observed := operation.Unknown
			if clean {
				observed = operation.ObservedDisconnected
			}
			if err == nil {
				err = w.ctx.Err()
			}
			m.fail(w, err, observed)
			m.settleCancelled(w, observed)
			return
		}
		if err := m.transition(w, operation.Succeeded, "", operation.ObservedDisconnected); err != nil {
			m.fail(w, err, operation.ObservedDisconnected)
			m.settleCancelled(w, operation.ObservedDisconnected)
		}
		return
	}
	established, err := m.options.Adapter.Observe(w.ctx, w.p)
	if err != nil {
		m.fail(w, err, operation.Unknown)
		return
	}
	fresh := !established
	if fresh {
		if w.ctx.Err() != nil {
			m.fail(w, w.ctx.Err(), operation.Unknown)
			return
		}
		err = m.options.Adapter.Connect(w.ctx, w.p, func(ctx context.Context) (secret.Value, error) { return m.challenge(w, ctx) })
		if err != nil {
			observed := operation.Unknown
			if m.cleanup(w) {
				observed = operation.ObservedDisconnected
			}
			m.fail(w, err, observed)
			m.settleCancelled(w, observed)
			return
		}
	}
	if err = m.transition(w, operation.Verifying, "", operation.Unknown); err == nil {
		err = m.options.Adapter.Verify(w.ctx, w.p)
	}
	if err == nil {
		err = m.transition(w, operation.Succeeded, "", operation.ObservedConnected)
	}
	if err != nil {
		observed := operation.Unknown
		if fresh && m.cleanup(w) {
			observed = operation.ObservedDisconnected
		}
		m.fail(w, err, observed)
		m.settleCancelled(w, observed)
	}
}
func (m *Manager) cleanup(w *worker) bool {
	ctx, cancel := context.WithTimeout(context.Background(), m.options.CleanupTimeout)
	defer cancel()
	return m.options.Adapter.Disconnect(ctx, w.p) == nil
}
func (m *Manager) fail(w *worker, err error, observed operation.Observed) {
	failure := operation.BackendFailed
	switch {
	case errors.Is(err, ErrExpired):
		failure = operation.MFAExpired
	case errors.Is(err, ErrAuthentication):
		failure = operation.AuthenticationFailed
	case errors.Is(err, context.DeadlineExceeded) || errors.Is(w.ctx.Err(), context.DeadlineExceeded):
		failure = operation.TimedOut
	}
	_ = m.transition(w, operation.Failed, failure, observed)
}
func (m *Manager) settleCancelled(w *worker, observed operation.Observed) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.checkLocked() != nil {
		return
	}
	r := m.state.Operations[w.id]
	c := m.state.Connections[w.p.Name]
	if r.State != operation.Cancelled || c.Generation != w.generation || c.ActiveOperation != "" || c.LastOperation != w.id {
		return
	}
	next := m.state.Clone()
	c.Observed = observed
	c.Revision++
	next.Connections[c.Profile] = c
	_ = m.commitLocked(next)
}

// StartReconcile never initiates a login. Saved disconnect intent is enforced;
// desired-connected sessions are observed and verified before recovery is enabled.
func (m *Manager) StartReconcile() {
	m.mu.Lock()
	if m.reconciled || m.checkLocked() != nil || m.options.Adapter == nil {
		m.mu.Unlock()
		return
	}
	m.reconciled = true
	connections := []operation.Connection{}
	for _, c := range m.state.Connections {
		connections = append(connections, c)
	}
	for _, c := range connections {
		m.wait.Add(1)
		go func() {
			defer m.wait.Done()
			if c.Desired == operation.Disconnected {
				_, _ = m.submit(m.ctx, c.Profile, operation.DisconnectConnection, c.Generation)
				return
			}
			m.reconcileConnected(c)
		}()
	}
	m.mu.Unlock()
}
func (m *Manager) reconcileConnected(previous operation.Connection) {
	var snapshot profile.Snapshot
	if m.options.WithProfile == nil || m.options.WithProfile(m.ctx, previous.Profile, func(p profile.Snapshot) error { snapshot = p; return nil }) != nil {
		return
	}
	m.mu.Lock()
	if m.checkLocked() != nil {
		m.mu.Unlock()
		return
	}
	entry := m.slots[previous.Profile]
	if entry == nil {
		entry = &slot{gate: make(chan struct{}, 1)}
		m.slots[previous.Profile] = entry
	}
	m.mu.Unlock()
	ctx, cancel := context.WithTimeout(m.ctx, m.options.CleanupTimeout)
	defer cancel()
	select {
	case entry.gate <- struct{}{}:
	case <-ctx.Done():
		return
	}
	defer func() { <-entry.gate }()
	m.mu.Lock()
	c := m.state.Connections[previous.Profile]
	current := m.checkLocked() == nil && c.Generation == previous.Generation && c.ActiveOperation == "" && c.Desired == operation.Connected
	m.mu.Unlock()
	if !current {
		return
	}
	established, err := m.options.Adapter.Observe(ctx, snapshot.Profile)
	observed := operation.Unknown
	if err == nil {
		observed = operation.ObservedDisconnected
		if established && m.options.Adapter.Verify(ctx, snapshot.Profile) == nil {
			observed = operation.ObservedConnected
		}
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	c = m.state.Connections[previous.Profile]
	if m.checkLocked() != nil || c.Generation != previous.Generation || c.ActiveOperation != "" || c.Desired != operation.Connected {
		return
	}
	next := m.state.Clone()
	c.Observed = observed
	c.Revision++
	c.RecoveryEnabled = observed == operation.ObservedConnected && m.recoveryWanted[c.Profile] && m.options.Adapter.CanRecover(snapshot.Profile)
	next.Connections[c.Profile] = c
	_ = m.commitLocked(next)
}

// Wait is useful to service shutdown/tests, not tied to an RPC request lifetime.
func (m *Manager) WaitIdle(ctx context.Context) error {
	tick := time.NewTicker(5 * time.Millisecond)
	defer tick.Stop()
	for {
		m.mu.Lock()
		idle := len(m.workers) == 0
		err := m.checkLocked()
		m.mu.Unlock()
		if idle {
			return err
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-tick.C:
		}
	}
}
