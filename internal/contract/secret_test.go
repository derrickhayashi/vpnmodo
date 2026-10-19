package contract

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

func TestCredentialsRequireExplicitTransportAndNeverAppearInFormatting(t *testing.T) {
	p, err := DecodeProfile([]byte(xauthProfile))
	if err != nil {
		t.Fatal(err)
	}
	input := ProfileCreateParams{Profile: p, Credentials: Credentials{NewSecret("synthetic-user"), NewSecret("synthetic-pass"), NewSecret("synthetic-shared")}}
	for _, value := range []any{input, input.Credentials, input.Credentials.Password} {
		for _, format := range []string{"%v", "%+v", "%#v", "%s", "%q", "%x"} {
			printed := fmt.Sprintf(format, value)
			if strings.Contains(printed, "synthetic-") {
				t.Fatalf("formatting disclosed credential using %s", format)
			}
		}
		if data, err := json.Marshal(value); err == nil || strings.Contains(string(data), "synthetic-") {
			t.Fatal("ordinary serialization released secret")
		}
	}
	encoded, err := input.EncodeForTransport()
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := DecodeProfileCreate(encoded)
	if err != nil {
		t.Fatal(err)
	}
	if decoded.Credentials.Password.value != "synthetic-pass" {
		t.Fatal("secret transport lost value")
	}
	for _, bad := range []string{"", "line\nbreak", "line\rbreak", "zero\x00byte", strings.Repeat("x", 4097)} {
		input.Credentials.Password = NewSecret(bad)
		if _, err := input.EncodeForTransport(); err == nil {
			t.Fatal("invalid credentials accepted")
		}
	}
	if _, err := DecodeProfileCreate([]byte(`{"profile":` + xauthProfile + `,"credentials":{"username":"synthetic-user","password":"synthetic-pass","psk":"synthetic-shared","extra":"synthetic-sensitive"}}`)); err == nil || strings.Contains(err.Error(), "synthetic-sensitive") {
		t.Fatal("unknown credential field accepted or disclosed")
	}
	oversize := NewSecret(strings.Repeat("<", 4096))
	input.Credentials = Credentials{oversize, oversize, oversize}
	if _, err := input.EncodeForTransport(); err == nil {
		t.Fatal("escaped credential payload exceeded input bounds")
	}
}
