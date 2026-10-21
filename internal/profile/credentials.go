package profile

import (
	"bytes"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"errors"
	"strings"
	"unicode/utf8"

	"github.com/derrickhayashi/vpnmodo/internal/secret"
)

var ErrCredentials = errors.New("invalid credential input")

type Credentials struct {
	Username secret.Value `json:"username" required:"true" secret:"true"`
	Password secret.Value `json:"password" required:"true" secret:"true"`
	PSK      secret.Value `json:"psk,omitempty" required:"true" secret:"true"`
	CACert   secret.Value `json:"ca_cert,omitempty" secret:"true" format:"pem-ca-bundle"`
}

func (c Credentials) Validate() error {
	valid := func(value string) bool {
		return value != "" && len(value) <= 4096 && utf8.ValidString(value) && !strings.ContainsAny(value, "\x00\r\n")
	}
	for _, value := range []secret.Value{c.Username, c.Password} {
		if !value.Check(valid) {
			return ErrCredentials
		}
	}
	psk := c.PSK.Check(valid)
	ca := c.CACert.Check(validCA)
	if !psk && !ca || !c.PSK.Check(func(s string) bool { return s == "" || valid(s) }) || !c.CACert.Check(func(s string) bool { return s == "" || validCA(s) }) {
		return ErrCredentials
	}
	return nil
}

// ValidateFor enforces each backend's authentication contract at the vault boundary.
func (c Credentials) ValidateFor(kind BackendType) error {
	if c.Validate() != nil {
		return ErrCredentials
	}
	empty := func(s string) bool { return s == "" }
	switch kind {
	case IKEv2:
		if !c.PSK.Check(empty) || !c.CACert.Check(validCA) {
			return ErrCredentials
		}
	case L2TP, XAuth:
		if c.PSK.Check(empty) || !c.CACert.Check(empty) {
			return ErrCredentials
		}
	default:
		return ErrBackend
	}
	return nil
}

// A bundle of CA certificates only; private keys and ignored PEM text are refused.
func validCA(value string) bool {
	if len(value) == 0 || len(value) > 32768 {
		return false
	}
	data := bytes.TrimSpace([]byte(value))
	if len(data) == 0 {
		return false
	}
	count := 0
	for len(data) > 0 {
		if !bytes.HasPrefix(data, []byte("-----BEGIN CERTIFICATE-----")) {
			return false
		}
		block, rest := pem.Decode(data)
		if block == nil || block.Type != "CERTIFICATE" || len(block.Headers) != 0 {
			return false
		}
		cert, err := x509.ParseCertificate(block.Bytes)
		if err != nil || !cert.IsCA || !cert.BasicConstraintsValid {
			return false
		}
		count++
		if count > 16 {
			return false
		}
		data = bytes.TrimSpace(rest)
	}
	return count > 0
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
	ca, err := c.CACert.EncodeForTransport()
	if err != nil {
		return nil, err
	}
	values := map[string]json.RawMessage{"username": username, "password": password}
	if !c.PSK.Check(func(s string) bool { return s == "" }) {
		values["psk"] = psk
	}
	if !c.CACert.Check(func(s string) bool { return s == "" }) {
		values["ca_cert"] = ca
	}
	return json.Marshal(values)
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
