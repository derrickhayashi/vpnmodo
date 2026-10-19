package credentialinput

import (
	"encoding/base64"
	"encoding/json"
	"testing"

	"github.com/derrickhayashi/vpn-sidecars-next/internal/secret"
)

func TestRegistryCredentials(t *testing.T) {
	data, err := registryConfig(secret.New("synthetic-user"), secret.New("synthetic-token"))
	if err != nil {
		t.Fatal(err)
	}
	defer clear(data)
	var config struct {
		Auths map[string]struct{ Auth string } `json:"auths"`
	}
	if json.Unmarshal(data, &config) != nil {
		t.Fatal("invalid Docker auth JSON")
	}
	decoded, err := base64.StdEncoding.DecodeString(config.Auths["ghcr.io"].Auth)
	if err != nil || string(decoded) != "synthetic-user:synthetic-token" {
		t.Fatal("wrong Docker registry auth")
	}
	for _, pair := range [][2]string{{"", "token"}, {"user:name", "token"}, {"user", ""}, {"user", "token\n"}} {
		if _, err := registryConfig(secret.New(pair[0]), secret.New(pair[1])); err == nil {
			t.Fatal("accepted invalid registry credential input")
		}
	}
}
