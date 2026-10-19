package profile

import (
	"encoding/json"
	"errors"
	"strings"
	"unicode/utf8"

	"github.com/derrickhayashi/vpn-sidecars-next/internal/secret"
)

var ErrCredentials = errors.New("invalid credential input")

type Credentials struct {
	Username secret.Value `json:"username" required:"true" secret:"true"`
	Password secret.Value `json:"password" required:"true" secret:"true"`
	PSK      secret.Value `json:"psk" required:"true" secret:"true"`
}

func (c Credentials) Validate() error {
	valid := func(value string) bool {
		return value != "" && len(value) <= 4096 && utf8.ValidString(value) && !strings.ContainsAny(value, "\x00\r\n")
	}
	for _, value := range []secret.Value{c.Username, c.Password, c.PSK} {
		if !value.Check(valid) {
			return ErrCredentials
		}
	}
	return nil
}

// EncodeForTransport is the explicit boundary for transmitting credential
// values. It does not create a saved profile or operation record.
func (c Credentials) EncodeForTransport() ([]byte, error) { return c.encode() }

// EncodeForStorage is restricted to the protected VM credential vault.
// Never use it for profiles, operation records, diagnostics or workstation state.
func (c Credentials) EncodeForStorage() ([]byte, error) { return c.encode() }

func (c Credentials) encode() ([]byte, error) {
	if err := c.Validate(); err != nil {
		return nil, err
	}
	username, err := c.Username.EncodeForTransport()
	if err != nil {
		return nil, err
	}
	password, err := c.Password.EncodeForTransport()
	if err != nil {
		return nil, err
	}
	psk, err := c.PSK.EncodeForTransport()
	if err != nil {
		return nil, err
	}
	return json.Marshal(struct {
		Username json.RawMessage `json:"username"`
		Password json.RawMessage `json:"password"`
		PSK      json.RawMessage `json:"psk"`
	}{username, password, psk})
}

// Snapshot is safe profile inspection metadata; it never contains a vault path
// or credential value. Revision is required for explicit changes and deletion.
type Snapshot struct {
	Profile            Profile `json:"profile"`
	Revision           uint64  `json:"revision"`
	CredentialsPresent bool    `json:"credentials_present"`
}

// Summary keeps list responses bounded independently of profile config size.
type Summary struct {
	Name               string      `json:"name"`
	Backend            BackendType `json:"backend"`
	Revision           uint64      `json:"revision"`
	CredentialsPresent bool        `json:"credentials_present"`
}
