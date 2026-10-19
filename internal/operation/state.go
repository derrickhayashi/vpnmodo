// Package operation defines lifecycle states, transitions and challenge identities.
package operation

import (
	"errors"
	"regexp"
	"time"
)

type Kind string

const (
	EnsureConnection     Kind = "connection.ensure"
	DisconnectConnection Kind = "connection.disconnect"
	RecoverConnection    Kind = "connection.recover"
	ChangeProfile        Kind = "profile.change"
)

type State string

const (
	Queued      State = "queued"
	Running     State = "running"
	AwaitingMFA State = "awaiting_mfa"
	Verifying   State = "verifying"
	Succeeded   State = "succeeded"
	Failed      State = "failed"
	Cancelled   State = "cancelled"
)

var ErrOperation = errors.New("invalid operation or state transition")
var operationID = regexp.MustCompile(`^op-[a-f0-9]{32}$`)
var challengeID = regexp.MustCompile(`^mfa-[a-f0-9]{32}$`)

func (s State) Terminal() bool { return s == Succeeded || s == Failed || s == Cancelled }
func (k Kind) Valid() bool {
	return k == EnsureConnection || k == DisconnectConnection || k == RecoverConnection || k == ChangeProfile
}

// CanTransition describes allowed transitions. Persistence and worker ownership
// are implemented later; an HTTP/SSH request context must not own the worker.
func CanTransition(kind Kind, from, to State) bool {
	if !kind.Valid() || from.Terminal() || from == to {
		return false
	}
	switch from {
	case Queued:
		return to == Running || to == Failed || to == Cancelled
	case Running:
		return to == Failed || to == Cancelled ||
			(to == AwaitingMFA && kind == EnsureConnection) ||
			(to == Verifying && (kind == EnsureConnection || kind == RecoverConnection)) ||
			(to == Succeeded && (kind == DisconnectConnection || kind == ChangeProfile))
	case AwaitingMFA:
		return kind == EnsureConnection && (to == Running || to == Failed || to == Cancelled)
	case Verifying:
		return (kind == EnsureConnection || kind == RecoverConnection) && (to == Succeeded || to == Failed || to == Cancelled)
	default:
		return false
	}
}

type MFAChallenge struct {
	ID        string    `json:"id"`
	ExpiresAt time.Time `json:"expires_at"`
}

func ValidID(id string) bool          { return operationID.MatchString(id) }
func ValidChallengeID(id string) bool { return challengeID.MatchString(id) }
