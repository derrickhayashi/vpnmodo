package contract

import (
	"errors"
	"regexp"
	"time"
)

type OperationKind string

const (
	EnsureConnection     OperationKind = "connection.ensure"
	DisconnectConnection OperationKind = "connection.disconnect"
	RecoverConnection    OperationKind = "connection.recover"
	ChangeProfile        OperationKind = "profile.change"
)

type OperationState string

const (
	Queued      OperationState = "queued"
	Running     OperationState = "running"
	AwaitingMFA OperationState = "awaiting_mfa"
	Verifying   OperationState = "verifying"
	Succeeded   OperationState = "succeeded"
	Failed      OperationState = "failed"
	Cancelled   OperationState = "cancelled"
)

var ErrOperation = errors.New("invalid operation or state transition")
var operationID = regexp.MustCompile(`^op-[a-f0-9]{32}$`)
var challengeID = regexp.MustCompile(`^mfa-[a-f0-9]{32}$`)

func (s OperationState) Terminal() bool { return s == Succeeded || s == Failed || s == Cancelled }
func (k OperationKind) Valid() bool {
	return k == EnsureConnection || k == DisconnectConnection || k == RecoverConnection || k == ChangeProfile
}

// CanTransition describes allowed transitions. Persistence and worker ownership
// are implemented later; an HTTP/SSH request context must not own the worker.
func CanTransition(kind OperationKind, from, to OperationState) bool {
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

// Operation contains only safe, typed metadata. No raw params, transcripts,
// credential values, MFA code, or free-form backend error are allowed here.
type Operation struct {
	ID        string         `json:"id"`
	Profile   string         `json:"profile"`
	Kind      OperationKind  `json:"kind"`
	State     OperationState `json:"state"`
	Revision  uint64         `json:"revision"`
	Challenge *MFAChallenge  `json:"challenge,omitempty"`
	Failure   *RPCError      `json:"failure,omitempty"`
}

func (o Operation) Validate() error {
	if !operationID.MatchString(o.ID) || !ValidName(o.Profile) || !o.Kind.Valid() || o.Revision == 0 {
		return ErrOperation
	}
	switch o.State {
	case Queued, Running, AwaitingMFA, Verifying, Succeeded, Failed, Cancelled:
	default:
		return ErrOperation
	}
	if o.State == AwaitingMFA {
		if o.Kind != EnsureConnection || o.Challenge == nil || !challengeID.MatchString(o.Challenge.ID) || o.Challenge.ExpiresAt.IsZero() {
			return ErrOperation
		}
	} else if o.Challenge != nil {
		return ErrOperation
	}
	if o.State == Verifying && o.Kind != EnsureConnection && o.Kind != RecoverConnection {
		return ErrOperation
	}
	if (o.State == Failed) != (o.Failure != nil) {
		return ErrOperation
	}
	if o.Failure != nil && !o.Failure.Valid() {
		return ErrOperation
	}
	return nil
}

// CheckChallenge binds code submission to one operation, challenge, and expiry.
// Revision matching/atomic consumption must be enforced by the operation manager.
func (o Operation) CheckChallenge(operation, challenge string, now time.Time) error {
	if o.Validate() != nil || o.State != AwaitingMFA || operation != o.ID || challenge != o.Challenge.ID {
		return ErrOperation
	}
	if !now.Before(o.Challenge.ExpiresAt) {
		return NewRPCError(CodeMFAExpired)
	}
	return nil
}

type MFASubmitParams struct {
	OperationID string `json:"operation_id"`
	ChallengeID string `json:"challenge_id"`
	Code        Secret `json:"code"`
}

func (p MFASubmitParams) Validate() error {
	if !operationID.MatchString(p.OperationID) || !challengeID.MatchString(p.ChallengeID) || len(p.Code.value) < 4 || len(p.Code.value) > 10 {
		return ErrOperation
	}
	for _, c := range p.Code.value {
		if c < '0' || c > '9' {
			return ErrOperation
		}
	}
	return nil
}

func DecodeMFASubmit(data []byte) (MFASubmitParams, error) {
	var params MFASubmitParams
	if err := decodeParams(data, &params); err != nil {
		return MFASubmitParams{}, err
	}
	if err := params.Validate(); err != nil {
		return MFASubmitParams{}, err
	}
	return params, nil
}

func (p MFASubmitParams) EncodeForTransport() ([]byte, error) {
	if err := p.Validate(); err != nil {
		return nil, err
	}
	return encode(struct {
		OperationID string `json:"operation_id"`
		ChallengeID string `json:"challenge_id"`
		Code        string `json:"code"`
	}{p.OperationID, p.ChallengeID, p.Code.value})
}
