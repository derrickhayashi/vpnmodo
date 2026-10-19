package operation

import "time"

// HealthState is allowlisted metadata, never a backend diagnostic string.
// A service failure alone is not evidence that the tunnel needs a new login.
type HealthState string

const (
	HealthUnknown            HealthState = "unknown"
	HealthHealthy            HealthState = "healthy"
	HealthStarting           HealthState = "starting"
	HealthStopped            HealthState = "stopped"
	HealthTunnelDown         HealthState = "tunnel_down"
	HealthServiceUnavailable HealthState = "service_unavailable"
	HealthDetached           HealthState = "namespace_detached"
	HealthUnavailable        HealthState = "unavailable"
	HealthOwnerConflict      HealthState = "owner_conflict"
	MaxRecoveryAttempts                  = 3
)

func (h HealthState) Valid() bool {
	switch h {
	case HealthUnknown, HealthHealthy, HealthStarting, HealthStopped, HealthTunnelDown, HealthServiceUnavailable, HealthDetached, HealthUnavailable, HealthOwnerConflict:
		return true
	}
	return false
}

// Recovery holds a bounded consecutive failure budget and a persisted cooldown.
// A zero timestamp is valid for records created before their first health probe.
type Recovery struct {
	Attempts  uint8       `json:"attempts"`
	NotBefore time.Time   `json:"not_before"`
	Health    HealthState `json:"health"`
	CheckedAt time.Time   `json:"checked_at"`
}

func (r Recovery) Validate() error {
	if r.Attempts > MaxRecoveryAttempts || !r.Health.Valid() || r.Attempts > 0 && r.NotBefore.IsZero() {
		return ErrOperation
	}
	return nil
}
