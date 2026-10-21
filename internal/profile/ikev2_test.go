package profile

import (
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/derrickhayashi/vpnmodo/internal/secret"
)

const ikev2Profile = `{"schema_version":1,"name":"example","backend":{"type":"ipsec-ikev2","config":{"server":"vpn.example.test","server_identity":"vpn.example.test","client_identity":"client@example.test","eap_identity":"synthetic-user","dns":["198.51.100.1"]}},"health":{"host":"private.example","port":8080}}`

func TestIKEv2SchemaAndProposalPolicy(t *testing.T) {
	p, err := DecodeProfile([]byte(ikev2Profile))
	if err != nil {
		t.Fatal(err)
	}
	for _, good := range []string{"aes256-sha256-modp2048", "aes128gcm16-prfsha384-ecp384", "aes256-sha384-prfsha512-curve25519"} {
		p.Backend.IKEv2.IKEProposals = []string{good}
		if p.Validate() != nil {
			t.Fatal("valid IKE policy refused")
		}
	}
	for _, bad := range []string{"default", "aes256-sha256-modp2048!", "aes256-sha1-modp1024", "aes256-sha256-modp2048,aes128-sha256-modp2048", "aes256-sha256-ecp256\ninclude other", "aes128-aes256-sha256-modp2048"} {
		p.Backend.IKEv2.IKEProposals = []string{bad}
		if p.Validate() == nil {
			t.Fatal("unsafe/unsupported IKE policy accepted")
		}
	}
	p.Backend.IKEv2.IKEProposals = []string{"aes128-sha256-modp2048", "aes256-sha384-ecp384"}
	p.Backend.IKEv2.ESPProposals = []string{"aes128gcm16", "aes256-sha512-ecp521"}
	encoded, _ := json.Marshal(p)
	round, err := DecodeProfile(encoded)
	if err != nil || strings.Join(round.Backend.IKEv2.IKEProposals, ",") != strings.Join(p.Backend.IKEv2.IKEProposals, ",") {
		t.Fatal("policy order lost")
	}
	effective := EffectiveIKEv2Config(*p.Backend.IKEv2)
	effective.IKEProposals[0] = "changed"
	if p.Backend.IKEv2.IKEProposals[0] == "changed" {
		t.Fatal("config alias")
	}
	defaults := EffectiveIKEv2Config(IKEv2Config{})
	if strings.Join(defaults.IKEProposals, ",") != "aes256-sha256-modp2048" || strings.Join(defaults.ESPProposals, ",") != "aes256-sha256" {
		t.Fatal("unexpected defaults")
	}
	for _, edit := range []func(*Profile){
		func(p *Profile) { p.Backend.IKEv2.ServerIdentity = "%any" },
		func(p *Profile) { p.Backend.IKEv2.ClientIdentity = "client\nother" },
		func(p *Profile) { p.Backend.IKEv2.DNS = nil },
		func(p *Profile) { p.Backend.IKEv2.Server = "2001:db8::1" },
		func(p *Profile) { p.Backend.IKEv2.ESPProposals = []string{"default"} },
		func(p *Profile) { p.Backend.L2TP = &L2TPConfig{} },
	} {
		p, _ := DecodeProfile([]byte(ikev2Profile))
		edit(&p)
		if p.Validate() == nil {
			t.Fatal("invalid IKEv2 config accepted")
		}
	}
}

func TestIKEv2CredentialsAndDiscovery(t *testing.T) {
	ca, err := os.ReadFile("testdata/ikev2-ca.txt")
	if err != nil {
		t.Fatal(err)
	}
	c := Credentials{Username: secret.New("synthetic-user"), Password: secret.New("synthetic-password"), CACert: secret.New(string(ca))}
	if c.ValidateFor(IKEv2) != nil {
		t.Fatal("IKEv2 credentials refused")
	}
	for _, kind := range []BackendType{L2TP, XAuth} {
		if c.ValidateFor(kind) == nil {
			t.Fatal("CA credentials accepted for PSK backend")
		}
	}
	raw, err := c.EncodeForStorage()
	if err != nil || strings.Contains(string(raw), `"psk"`) {
		t.Fatal("PSK required/encoded for EAP")
	}
	var round Credentials
	if json.Unmarshal(raw, &round) != nil || round.ValidateFor(IKEv2) != nil {
		t.Fatal("vault roundtrip failed")
	}
	for _, value := range []string{"", "not a certificate", string(ca) + "private trailing text", strings.Replace(string(ca), "CERTIFICATE", "PRIVATE KEY", -1)} {
		c.CACert = secret.New(value)
		if c.ValidateFor(IKEv2) == nil {
			t.Fatal("invalid CA material accepted")
		}
	}
	c.CACert = secret.New(string(ca))
	c.PSK = secret.New("synthetic-shared")
	if c.ValidateFor(IKEv2) == nil {
		t.Fatal("extraneous PSK accepted")
	}
	d, err := DescribeBackend(IKEv2)
	if err != nil || d.Capabilities.MFA || !d.Capabilities.AutomaticRecovery || !d.Capabilities.RequireFailClosed || len(d.CredentialFields) != 3 {
		t.Fatal("bad discovery")
	}
	for _, f := range d.CredentialFields {
		if f.Name == "psk" || !f.Required || !f.Secret {
			t.Fatal("incorrect credential contract")
		}
	}
}
