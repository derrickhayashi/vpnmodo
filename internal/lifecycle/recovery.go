package lifecycle

import (
	"context"
	"sort"
	"time"

	"github.com/derrickhayashi/vpn-sidecars-next/internal/operation"
	"github.com/derrickhayashi/vpn-sidecars-next/internal/profile"
	"github.com/derrickhayashi/vpn-sidecars-next/internal/secret"
)

// RecoveryAdapter opts a backend into monitoring. Check is read-only and must
// distinguish tunnel failure from endpoint failure. Every method independently
// verifies ownership and refuses competing recovery owners. Quiesce stops only
// this profile's application; RepairForwarding rechecks tunnel and service health
// before attaching an application, and leaves it stopped on failure. No method
// may adopt legacy containers, log credentials, or ignore context cancellation.
type RecoveryAdapter interface {
	Adapter
	Check(context.Context, profile.Profile) (operation.HealthState, error)
	Quiesce(context.Context, profile.Profile) error
	RepairForwarding(context.Context, profile.Profile) error
}

type RecoveryPolicy struct {
	Interval, ProbeTimeout, Cooldown, MaxBackoff time.Duration
}

func (p RecoveryPolicy) defaults() RecoveryPolicy {
	if p.Interval <= 0 {
		p.Interval = 30 * time.Second
	}
	if p.ProbeTimeout <= 0 {
		p.ProbeTimeout = 10 * time.Second
	}
	if p.Cooldown <= 0 {
		p.Cooldown = 5 * time.Minute
	}
	if p.MaxBackoff <= 0 {
		p.MaxBackoff = 30 * time.Minute
	}
	if p.MaxBackoff < p.Cooldown {
		p.MaxBackoff = p.Cooldown
	}
	return p
}
func (p RecoveryPolicy) backoff(attempt uint8) time.Duration {
	d := p.Cooldown
	for i := uint8(0); i < attempt; i++ {
		if d >= p.MaxBackoff/2 {
			return p.MaxBackoff
		}
		d *= 2
	}
	return d
}

// One bounded scheduler per service, at most four probes at a time. Profile gates
// serialize probes/repairs with ensure and teardown. A probe releases its gate
// before submitting work; the saved intent generation rejects stale decisions.
func (m *Manager) monitorLoop() {
	defer m.wait.Done()
	ticker := time.NewTicker(m.options.Recovery.Interval)
	defer ticker.Stop()
	for {
		select {
		case <-m.ctx.Done():
			return
		case <-ticker.C:
			m.mu.Lock()
			if m.checkLocked() != nil {
				m.mu.Unlock()
				return
			}
			candidates := make([]operation.Connection, 0, len(m.state.Connections))
			for _, c := range m.state.Connections {
				candidates = append(candidates, c)
			}
			sort.Slice(candidates, func(i, j int) bool {
				var a, b time.Time
				if candidates[i].Recovery != nil {
					a = candidates[i].Recovery.CheckedAt
				}
				if candidates[j].Recovery != nil {
					b = candidates[j].Recovery.CheckedAt
				}
				if a.Equal(b) {
					return candidates[i].Profile < candidates[j].Profile
				}
				return a.Before(b)
			})
			for _, c := range candidates {
				name := c.Profile
				if len(m.monitoring) >= 4 {
					break
				}
				if c.Desired != operation.Connected || !c.RecoveryEnabled || c.ActiveOperation != "" || m.monitoring[name] {
					continue
				}
				m.monitoring[name] = true
				m.wait.Add(1)
				go m.monitor(c)
			}
			m.mu.Unlock()
		}
	}
}
func (m *Manager) monitor(previous operation.Connection) {
	defer m.wait.Done()
	defer func() { m.mu.Lock(); delete(m.monitoring, previous.Profile); m.mu.Unlock() }()
	ctx, cancel := context.WithTimeout(m.ctx, m.options.Recovery.ProbeTimeout)
	defer cancel()
	var p profile.Snapshot
	if m.options.WithProfile(ctx, previous.Profile, func(s profile.Snapshot) error { p = s; return nil }) != nil {
		return
	}
	m.mu.Lock()
	entry := m.slots[previous.Profile]
	if entry == nil {
		entry = &slot{gate: make(chan struct{}, 1)}
		m.slots[previous.Profile] = entry
	}
	m.mu.Unlock()
	select {
	case entry.gate <- struct{}{}:
	default:
		return
	}
	act := m.probe(ctx, previous, p)
	<-entry.gate
	if act {
		_, _ = m.submit(ctx, previous.Profile, operation.RecoverConnection, previous.Generation)
	}
}
func (m *Manager) currentProbe(previous operation.Connection) bool {
	c := m.state.Connections[previous.Profile]
	return m.checkLocked() == nil && c.Generation == previous.Generation && c.Desired == operation.Connected && c.RecoveryEnabled && c.ActiveOperation == ""
}
func (m *Manager) probe(ctx context.Context, previous operation.Connection, p profile.Snapshot) bool {
	m.mu.Lock()
	current := m.currentProbe(previous)
	m.mu.Unlock()
	if !current {
		return false
	}
	a := m.options.Adapter.(RecoveryAdapter)
	health, err := a.Check(ctx, p.Profile)
	if err != nil || ctx.Err() != nil || !health.Valid() || health == operation.HealthUnknown {
		health = operation.HealthUnavailable
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if !m.currentProbe(previous) {
		return false
	}
	next := m.state.Clone()
	c := next.Connections[previous.Profile]
	if c.Recovery == nil {
		c.Recovery = &operation.Recovery{}
	}
	c.Recovery.Health = health
	c.Recovery.CheckedAt = time.Now().UTC()
	if health == operation.HealthHealthy {
		c.Recovery.Attempts = 0
		c.Observed = operation.ObservedConnected
	} else {
		c.Observed = operation.Unknown
	}
	if health == operation.HealthStopped {
		c.Observed = operation.ObservedDisconnected
	}
	if health == operation.HealthStopped || health == operation.HealthOwnerConflict || !p.CredentialsPresent {
		c.RecoveryEnabled = false
	}
	c.Revision++
	next.Connections[c.Profile] = c
	if m.commitLocked(next) != nil {
		return false
	}
	return c.RecoveryEnabled && (health == operation.HealthTunnelDown || health == operation.HealthDetached) && c.Recovery.Attempts < operation.MaxRecoveryAttempts && !time.Now().Before(c.Recovery.NotBefore)
}

func (m *Manager) runRecovery(w *worker) {
	a := m.options.Adapter.(RecoveryAdapter)
	health, err := a.Check(w.ctx, w.p)
	if err != nil || w.ctx.Err() != nil || health != operation.HealthHealthy && health != operation.HealthDetached && health != operation.HealthTunnelDown {
		m.fail(w, ErrUnavailable, operation.Unknown)
		return
	}
	w.recoveryRetry = true
	quiesced, succeeded := false, false
	defer func() {
		if quiesced && !succeeded {
			ctx, cancel := context.WithTimeout(context.Background(), m.options.CleanupTimeout)
			defer cancel()
			_ = a.Quiesce(ctx, w.p)
		}
	}()
	if health != operation.HealthHealthy {
		if err = a.Quiesce(w.ctx, w.p); err != nil {
			m.fail(w, err, operation.Unknown)
			return
		}
		quiesced = true
	}
	fresh := health == operation.HealthTunnelDown
	if fresh {
		// Application is stopped before any VPN namespace replacement.
		err = a.Disconnect(w.ctx, w.p)
		if err == nil {
			err = a.Connect(w.ctx, w.p, func(context.Context) (secret.Value, error) { w.recoveryRetry = false; return secret.Value{}, ErrIntent })
		}
	}
	if err == nil {
		err = m.transition(w, operation.Verifying, "", operation.Unknown)
	}
	if err == nil {
		err = a.Verify(w.ctx, w.p)
	}
	if err == nil {
		err = a.RepairForwarding(w.ctx, w.p)
	}
	if err == nil {
		err = m.transition(w, operation.Succeeded, "", operation.ObservedConnected)
	}
	if err == nil {
		succeeded = true
		return
	}
	// A healthy recheck may have failed verification. Stop forwarding before
	// reporting failure, without reconnecting a tunnel just for a failed TCP probe.
	if !quiesced {
		ctx, cancel := context.WithTimeout(context.Background(), m.options.CleanupTimeout)
		quiesced = a.Quiesce(ctx, w.p) == nil
		cancel()
	}
	observed := operation.Unknown
	if fresh && m.cleanup(w) {
		observed = operation.ObservedDisconnected
	}
	m.fail(w, err, observed)
	m.settleCancelled(w, observed)
}
