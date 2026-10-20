package operation

import (
	"github.com/derrickhayashi/vpn-sidecars-next/internal/profile"
	"time"
)

type Failure string

const (
	Interrupted          Failure = "interrupted"
	TimedOut             Failure = "timeout"
	BackendFailed        Failure = "backend"
	CredentialsFailed    Failure = "credentials"
	MFAExpired           Failure = "mfa_expired"
	AuthenticationFailed Failure = "authentication"
)

func (f Failure) Valid() bool {
	return f == Interrupted || f == TimedOut || f == BackendFailed || f == CredentialsFailed || f == MFAExpired || f == AuthenticationFailed
}

// Record contains only allowlisted non-secret metadata, never adapter errors,
// request parameters, credentials or submitted MFA values.
type Record struct {
	ID              string        `json:"id"`
	Profile         string        `json:"profile"`
	ProfileRevision uint64        `json:"profile_revision"`
	Kind            Kind          `json:"kind"`
	State           State         `json:"state"`
	Revision        uint64        `json:"revision"`
	Challenge       *MFAChallenge `json:"challenge,omitempty"`
	Failure         Failure       `json:"failure,omitempty"`
	CreatedAt       time.Time     `json:"created_at"`
	UpdatedAt       time.Time     `json:"updated_at"`
	Deadline        time.Time     `json:"deadline"`
}

func (r Record) Validate() error {
	if !ValidID(r.ID) || !profile.ValidName(r.Profile) || r.ProfileRevision == 0 || !r.Kind.Valid() || r.Revision == 0 || r.CreatedAt.IsZero() || r.UpdatedAt.Before(r.CreatedAt) || !r.Deadline.After(r.CreatedAt) {
		return ErrOperation
	}
	switch r.State {
	case Queued, Running, AwaitingMFA, Verifying, Succeeded, Failed, Cancelled:
	default:
		return ErrOperation
	}
	if (r.State == Failed) != (r.Failure != "") || r.Failure != "" && !r.Failure.Valid() {
		return ErrOperation
	}
	if r.State == AwaitingMFA {
		if r.Kind != EnsureConnection || r.Challenge == nil || !ValidChallengeID(r.Challenge.ID) || !r.Challenge.ExpiresAt.After(r.CreatedAt) || r.Challenge.ExpiresAt.After(r.Deadline) {
			return ErrOperation
		}
	} else if r.Challenge != nil {
		return ErrOperation
	}
	if r.State == Verifying && r.Kind != EnsureConnection && r.Kind != RecoverConnection {
		return ErrOperation
	}
	return nil
}

type Intent string
type Observed string

const (
	Connected            Intent   = "connected"
	Disconnected         Intent   = "disconnected"
	ObservedConnected    Observed = "connected"
	ObservedDisconnected Observed = "disconnected"
	Unknown              Observed = "unknown"
)

type Connection struct {
	Backend         profile.BackendType `json:"backend,omitempty"`
	Profile         string              `json:"profile"`
	Generation      uint64              `json:"generation"`
	Revision        uint64              `json:"revision"`
	Desired         Intent              `json:"desired"`
	Observed        Observed            `json:"observed"`
	RecoveryEnabled bool                `json:"recovery_enabled"`
	Recovery        *Recovery           `json:"recovery,omitempty"`
	ActiveOperation string              `json:"active_operation,omitempty"`
	LastOperation   string              `json:"last_operation,omitempty"`
}

func (c Connection) Validate() error {
	if c.Backend != "" && c.Backend != profile.L2TP && c.Backend != profile.XAuth {
		return ErrOperation
	}
	if c.Recovery != nil && c.Recovery.Validate() != nil {
		return ErrOperation
	}
	if !profile.ValidName(c.Profile) || c.Generation == 0 || c.Revision == 0 || c.Desired != Connected && c.Desired != Disconnected || c.Observed != Unknown && c.Observed != ObservedConnected && c.Observed != ObservedDisconnected || c.ActiveOperation != "" && !ValidID(c.ActiveOperation) || c.LastOperation != "" && !ValidID(c.LastOperation) || c.Desired == Disconnected && c.RecoveryEnabled {
		return ErrOperation
	}
	return nil
}
