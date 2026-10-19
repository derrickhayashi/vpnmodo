package lifecycle

import (
	"context"
	"github.com/derrickhayashi/vpn-sidecars-next/internal/operation"
	"github.com/derrickhayashi/vpn-sidecars-next/internal/profile"
	"time"
)

// Runtime serializes read-only inspection with backend changes. It never repairs,
// authenticates, or changes desired intent. Profile -> mutex -> gate is the same
// lock order as submit; gate acquisition is nonblocking to avoid worker deadlock.
func (m *Manager) Runtime(ctx context.Context, name string) (operation.RuntimeView, error) {
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	var result operation.RuntimeView
	if m.options.WithProfile == nil {
		return result, ErrUnavailable
	}
	err := m.options.WithProfile(ctx, name, func(p profile.Snapshot) error {
		m.mu.Lock()
		if err := m.checkLocked(); err != nil {
			m.mu.Unlock()
			return err
		}
		c, ok := m.state.Connections[name]
		if !ok {
			c = operation.Connection{Profile: name, Generation: 1, Revision: 1, Desired: operation.Disconnected, Observed: operation.Unknown}
		}
		if c.ActiveOperation != "" {
			m.mu.Unlock()
			return ErrBusy
		}
		adapter, ok := m.options.Adapter.(interface {
			Access(context.Context, profile.Profile) (operation.HealthState, string, error)
		})
		if !ok {
			m.mu.Unlock()
			return ErrUnavailable
		}
		entry := m.slots[name]
		if entry == nil {
			entry = &slot{gate: make(chan struct{}, 1)}
			m.slots[name] = entry
		}
		select {
		case entry.gate <- struct{}{}:
		default:
			m.mu.Unlock()
			return ErrBusy
		}
		m.mu.Unlock()
		defer func() { <-entry.gate }()
		health, id, err := adapter.Access(ctx, p.Profile)
		if err != nil {
			return err
		}
		// The profile transaction prevents a concurrent submit while this snapshot
		// is checked. Monitoring uses the same gate. Never return an app for down intent.
		if c.Desired != operation.Connected || health != operation.HealthHealthy {
			id = ""
		}
		tunnel, endpoint := false, false
		if health != operation.HealthOwnerConflict {
			tunnel, err = m.options.Adapter.Observe(ctx, p.Profile)
			if err != nil {
				return err
			}
			if tunnel {
				endpoint = m.options.Adapter.Verify(ctx, p.Profile) == nil
			}
		}
		if !tunnel || !endpoint {
			id = ""
		}
		prerequisites := false
		if checker, ok := m.options.Adapter.(interface {
			Prerequisites(context.Context, profile.Profile) bool
		}); ok {
			prerequisites = checker.Prerequisites(ctx, p.Profile)
		}
		result = operation.RuntimeView{Connection: c, Health: health, ApplicationID: id, TunnelReady: tunnel, PrivateEndpointReady: endpoint, ProfileRevision: p.Revision, CredentialsPresent: p.CredentialsPresent, PrerequisitesReady: prerequisites}
		return nil
	})
	return result, err
}
