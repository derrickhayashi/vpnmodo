package protocol

import (
	"encoding/json"

	"github.com/derrickhayashi/vpn-sidecars-next/internal/jsonstrict"
	"github.com/derrickhayashi/vpn-sidecars-next/internal/profile"
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
