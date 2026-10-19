package contract

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"unicode/utf8"

	"github.com/derrickhayashi/vpn-sidecars-next/internal/jsonstrict"
)

var (
	ErrSecretEncoding = errors.New("secret requires explicit transport encoding")
	ErrCredentials    = errors.New("invalid credential input")
)

// Secret intentionally blocks ordinary JSON encoding and redacts fmt verbs.
// It does not claim to securely erase Go-managed memory.
type Secret struct{ value string }

func NewSecret(value string) Secret                { return Secret{value: value} }
func (s Secret) Format(state fmt.State, verb rune) { _, _ = io.WriteString(state, "[REDACTED]") }
func (s Secret) MarshalJSON() ([]byte, error)      { return nil, ErrSecretEncoding }
func (s *Secret) UnmarshalJSON(data []byte) error {
	var value string
	if string(data) == "null" || json.Unmarshal(data, &value) != nil {
		return ErrCredentials
	}
	s.value = value
	return nil
}

type Credentials struct {
	Username Secret `json:"username" required:"true" secret:"true"`
	Password Secret `json:"password" required:"true" secret:"true"`
	PSK      Secret `json:"psk" required:"true" secret:"true"`
}

func (c Credentials) Validate() error {
	for _, value := range []string{c.Username.value, c.Password.value, c.PSK.value} {
		if value == "" || len(value) > 4096 || !utf8.ValidString(value) || strings.ContainsAny(value, "\x00\r\n") {
			return ErrCredentials
		}
	}
	return nil
}

type ProfileCreateParams struct {
	Profile     Profile     `json:"profile"`
	Credentials Credentials `json:"credentials"`
}

// credentialWire is private to explicit transport encoders.
type credentialWire struct {
	Username string `json:"username"`
	Password string `json:"password"`
	PSK      string `json:"psk"`
}

func (c Credentials) transportWire() credentialWire {
	return credentialWire{c.Username.value, c.Password.value, c.PSK.value}
}

func DecodeProfileCreate(data []byte) (ProfileCreateParams, error) {
	var params ProfileCreateParams
	if err := jsonstrict.Decode(data, &params, MaxProfileBytes); err != nil {
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
	encoded, err := json.Marshal(struct {
		Profile     Profile        `json:"profile"`
		Credentials credentialWire `json:"credentials"`
	}{p.Profile, p.Credentials.transportWire()})
	if err != nil {
		return nil, err
	}
	if len(encoded) > MaxProfileBytes {
		return nil, jsonstrict.ErrLimit
	}
	return encoded, nil
}
