package protocol

import (
	"encoding/json"
	"time"

	"github.com/derrickhayashi/vpn-sidecars-next/internal/operation"
	"github.com/derrickhayashi/vpn-sidecars-next/internal/profile"
	"github.com/derrickhayashi/vpn-sidecars-next/internal/secret"
)

// Operation is a wire snapshot containing only safe, typed metadata. No raw params, transcripts,
// credential values, MFA code, or free-form backend error are allowed here.
type Operation struct {
	ID        string                  `json:"id"`
	Profile   string                  `json:"profile"`
	Kind      operation.Kind          `json:"kind"`
	State     operation.State         `json:"state"`
	Revision  uint64                  `json:"revision"`
	Challenge *operation.MFAChallenge `json:"challenge,omitempty"`
	Failure   *RPCError               `json:"failure,omitempty"`
}

func (o Operation) Validate() error {
	if !operation.ValidID(o.ID) || !profile.ValidName(o.Profile) || !o.Kind.Valid() || o.Revision == 0 {
		return operation.ErrOperation
	}
	switch o.State {
	case operation.Queued, operation.Running, operation.AwaitingMFA, operation.Verifying, operation.Succeeded, operation.Failed, operation.Cancelled:
	default:
		return operation.ErrOperation
	}
	if o.State == operation.AwaitingMFA {
		if o.Kind != operation.EnsureConnection || o.Challenge == nil || !operation.ValidChallengeID(o.Challenge.ID) || o.Challenge.ExpiresAt.IsZero() {
			return operation.ErrOperation
		}
	} else if o.Challenge != nil {
		return operation.ErrOperation
	}
	if o.State == operation.Verifying && o.Kind != operation.EnsureConnection && o.Kind != operation.RecoverConnection {
		return operation.ErrOperation
	}
	if (o.State == operation.Failed) != (o.Failure != nil) {
		return operation.ErrOperation
	}
	if o.Failure != nil && !o.Failure.Valid() {
		return operation.ErrOperation
	}
	return nil
}

// CheckChallenge binds code submission to one operation, challenge, and expiry.
// Revision matching/atomic consumption must be enforced by the operation manager.
func (o Operation) CheckChallenge(operationID, challenge string, now time.Time) error {
	if o.Validate() != nil || o.State != operation.AwaitingMFA || operationID != o.ID || challenge != o.Challenge.ID {
		return operation.ErrOperation
	}
	if !now.Before(o.Challenge.ExpiresAt) {
		return NewRPCError(CodeMFAExpired)
	}
	return nil
}

type MFASubmitParams struct {
	OperationID string       `json:"operation_id"`
	ChallengeID string       `json:"challenge_id"`
	Code        secret.Value `json:"code"`
}

func (p MFASubmitParams) Validate() error {
	if !operation.ValidID(p.OperationID) || !operation.ValidChallengeID(p.ChallengeID) || !p.Code.Check(func(value string) bool {
		if len(value) < 4 || len(value) > 10 {
			return false
		}
		for _, c := range value {
			if c < '0' || c > '9' {
				return false
			}
		}
		return true
	}) {
		return operation.ErrOperation
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
	code, err := p.Code.EncodeForTransport()
	if err != nil {
		return nil, err
	}
	return encode(struct {
		OperationID string          `json:"operation_id"`
		ChallengeID string          `json:"challenge_id"`
		Code        json.RawMessage `json:"code"`
	}{p.OperationID, p.ChallengeID, code})
}
