package protocol

import (
	"encoding/json"

	"github.com/derrickhayashi/vpnmodo/internal/jsonstrict"
	"github.com/derrickhayashi/vpnmodo/internal/profile"
)

type ProfileCreateParams struct {
	Profile     profile.Profile     `json:"profile"`
	Credentials profile.Credentials `json:"credentials"`
}

func DecodeProfileCreate(data []byte) (ProfileCreateParams, error) {
	var params ProfileCreateParams
	if err := jsonstrict.Decode(data, &params, profile.MaxProfileBytes); err != nil {
		return ProfileCreateParams{}, err
	}
	if err := params.Profile.Validate(); err != nil {
		return ProfileCreateParams{}, err
	}
	if err := params.Credentials.Validate(); err != nil {
		return ProfileCreateParams{}, err
	}
	return params, nil
}

// EncodeForTransport explicitly releases credential values into a bounded RPC
// parameter payload. Callers must send only over authenticated SSH, not log or
// persist the payload. Ordinary json.Marshal on ProfileCreateParams is blocked.
func (p ProfileCreateParams) EncodeForTransport() ([]byte, error) {
	if err := p.Profile.Validate(); err != nil {
		return nil, err
	}
	if err := p.Credentials.Validate(); err != nil {
		return nil, err
	}
	credentials, err := p.Credentials.EncodeForTransport()
	if err != nil {
		return nil, err
	}
	encoded, err := json.Marshal(struct {
		Profile     profile.Profile `json:"profile"`
		Credentials json.RawMessage `json:"credentials"`
	}{p.Profile, credentials})
	if err != nil {
		return nil, err
	}
	if len(encoded) > profile.MaxProfileBytes {
		return nil, jsonstrict.ErrLimit
	}
	return encoded, nil
}

type ProfileUpdateParams struct {
	Profile          profile.Profile `json:"profile"`
	ExpectedRevision uint64          `json:"expected_revision"`
}
type ProfileChangeParams struct {
	Profile          string `json:"profile"`
	ExpectedRevision uint64 `json:"expected_revision"`
}
type CredentialRotateParams struct {
	Profile          string              `json:"profile"`
	ExpectedRevision uint64              `json:"expected_revision"`
	Credentials      profile.Credentials `json:"credentials"`
}

func DecodeProfileUpdate(data []byte) (ProfileUpdateParams, error) {
	var p ProfileUpdateParams
	if jsonstrict.Decode(data, &p, profile.MaxProfileBytes) != nil || p.Profile.Validate() != nil || p.ExpectedRevision == 0 {
		return p, NewRPCError(CodeInvalidParams)
	}
	return p, nil
}
func DecodeProfileChange(data []byte) (ProfileChangeParams, error) {
	var p ProfileChangeParams
	if jsonstrict.Decode(data, &p, profile.MaxProfileBytes) != nil || !profile.ValidName(p.Profile) || p.ExpectedRevision == 0 {
		return p, NewRPCError(CodeInvalidParams)
	}
	return p, nil
}
func DecodeCredentialRotate(data []byte) (CredentialRotateParams, error) {
	var p CredentialRotateParams
	if jsonstrict.Decode(data, &p, profile.MaxProfileBytes) != nil || !profile.ValidName(p.Profile) || p.ExpectedRevision == 0 || p.Credentials.Validate() != nil {
		return CredentialRotateParams{}, NewRPCError(CodeInvalidParams)
	}
	return p, nil
}
func (p CredentialRotateParams) EncodeForTransport() ([]byte, error) {
	if !profile.ValidName(p.Profile) || p.ExpectedRevision == 0 {
		return nil, NewRPCError(CodeInvalidParams)
	}
	credentials, err := p.Credentials.EncodeForTransport()
	if err != nil {
		return nil, err
	}
	return json.Marshal(struct {
		Profile          string          `json:"profile"`
		ExpectedRevision uint64          `json:"expected_revision"`
		Credentials      json.RawMessage `json:"credentials"`
	}{p.Profile, p.ExpectedRevision, credentials})
}
