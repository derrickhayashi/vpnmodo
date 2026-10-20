package credentialinput

import (
	"encoding/base64"
	"encoding/json"
	"strings"

	"github.com/derrickhayashi/vpn-sidecars-next/internal/secret"
)

// RegistryConfig encodes supplied registry credentials without prompting.
func RegistryConfig(username, token secret.Value) ([]byte, error) {
	if !username.Check(func(s string) bool { return s != "" && len(s) <= 100 && !strings.ContainsAny(s, ": \t\r\n") }) ||
		!token.Check(func(s string) bool { return s != "" && len(s) <= 4096 && !strings.ContainsAny(s, " \t\r\n") }) {
		return nil, ErrInput
	}
	credentials := username.BytesForProtectedFile()
	credentials = append(credentials, ':')
	password := token.BytesForProtectedFile()
	credentials = append(credentials, password...)
	defer clear(credentials)
	defer clear(password)
	return json.Marshal(map[string]any{"auths": map[string]any{"ghcr.io": map[string]string{
		"auth": base64.StdEncoding.EncodeToString(credentials),
	}}})
}
