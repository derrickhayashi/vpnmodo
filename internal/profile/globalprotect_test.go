package profile

import (
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/derrickhayashi/vpnmodo/internal/secret"
)

func TestGlobalProtectProfileAndCredentialBoundary(t *testing.T) {
	raw, err := os.ReadFile("../../examples/profiles/v1/example-globalprotect.json")
	if err != nil {
		t.Fatal(err)
	}
	p, err := DecodeProfile(raw)
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(p)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = DecodeProfile(encoded); err != nil {
		t.Fatal(err)
	}
	for _, bad := range []string{
		strings.Replace(string(raw), "gateway.example.test", "https://gateway.example.test", 1),
		strings.Replace(string(raw), "gateway.example.test", "gateway.example.test:8443", 1),
		strings.Replace(string(raw), "gateway.example.test", "2001:db8::1", 1),
		strings.Replace(string(raw), "198.51.100.53", "::1", 1),
		strings.Replace(string(raw), `"server":`, `"unknown_option": true, "server":`, 1),
	} {
		if _, err = DecodeProfile([]byte(bad)); err == nil {
			t.Fatal("invalid GlobalProtect config accepted")
		}
	}
	p.Backend.GlobalProtect.DNS = nil
	p.Health.Host = "private.example.test"
	if p.Validate() == nil {
		t.Fatal("hostname health accepted without VPN DNS")
	}
	p.Health.Host = "192.0.2.10"
	p.Backend.L2TP = &L2TPConfig{Server: "203.0.113.1"}
	if p.Validate() == nil {
		t.Fatal("mixed backend accepted")
	}
	ca, err := os.ReadFile("testdata/ikev2-ca.txt")
	if err != nil {
		t.Fatal(err)
	}
	c := Credentials{Username: secret.New("synthetic-user"), Password: secret.New("synthetic-password"), CACert: secret.New(string(ca))}
	if c.ValidateFor(GlobalProtect) != nil {
		t.Fatal("valid credentials refused")
	}
	c.PSK = secret.New("synthetic-shared")
	if c.ValidateFor(GlobalProtect) == nil {
		t.Fatal("PSK accepted")
	}
	c.PSK, c.CACert = secret.Value{}, secret.Value{}
	if c.ValidateForBackend(p.Backend) == nil {
		t.Fatal("missing CA accepted")
	}
	p.Backend.L2TP = nil
	p.Backend.GlobalProtect.InsecureSkipTLSVerify = true
	if c.ValidateForBackend(p.Backend) != nil {
		t.Fatal("explicit insecure login refused")
	}
	encoded, err = json.Marshal(p.Backend)
	if err != nil || !strings.Contains(string(encoded), `"insecure_skip_tls_verify":true`) {
		t.Fatal("TLS opt-in lost")
	}
	var backend Backend
	if json.Unmarshal(encoded, &backend) != nil || !backend.GlobalProtect.InsecureSkipTLSVerify {
		t.Fatal("TLS policy round trip")
	}
	if _, err := c.EncodeForTransport(); err != nil {
		t.Fatal("password-only transport refused")
	}
	for _, kind := range []BackendType{L2TP, XAuth, IKEv2} {
		if c.ValidateFor(kind) == nil {
			t.Fatal("other backend lost authentication contract")
		}
	}
	d, err := DescribeBackend(GlobalProtect)
	if err != nil || d.Capabilities.MFA || !d.Capabilities.AutomaticRecovery || !d.Capabilities.RequireFailClosed {
		t.Fatal("capability mismatch")
	}
	if len(d.CredentialFields) != 3 {
		t.Fatal("credential schema")
	}
	for _, f := range d.CredentialFields {
		if f.Name != "ca_cert" && !f.Required || !f.Secret || f.Name == "psk" {
			t.Fatal("credential metadata")
		}
	}
}
