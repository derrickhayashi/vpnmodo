package profile

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const xauthProfile = `{"schema_version":1,"name":"example","backend":{"type":"ipsec-xauth","config":{"server":"203.0.113.10"}},"health":{"host":"192.0.2.10","port":22}}`
const l2tpProfile = `{"schema_version":1,"name":"example","backend":{"type":"l2tp-ipsec","config":{"server":"vpn.example.com","dns":["192.0.2.53"]}},"health":{"host":"health.example.com","port":443}}`

func TestShippedProfileExamples(t *testing.T) {
	paths, err := filepath.Glob("../../examples/profiles/v1/*.json")
	if err != nil || len(paths) != 2 {
		t.Fatal("expected both v1 backend examples", err)
	}
	for _, path := range paths {
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := DecodeProfile(data); err != nil {
			t.Fatalf("invalid shipped example %s: %v", path, err)
		}
	}
}

func TestProfileRoundTripAndBackendIsolation(t *testing.T) {
	for _, input := range []string{xauthProfile, l2tpProfile} {
		p, err := DecodeProfile([]byte(input))
		if err != nil {
			t.Fatal(err)
		}
		encoded, err := json.Marshal(p)
		if err != nil {
			t.Fatal(err)
		}
		again, err := DecodeProfile(encoded)
		if err != nil || again.Backend.Type != p.Backend.Type {
			t.Fatal("round trip failed", err)
		}
	}
	for name, input := range map[string]string{
		"future schema":                   strings.Replace(xauthProfile, `"schema_version":1`, `"schema_version":2`, 1),
		"missing schema":                  strings.Replace(xauthProfile, `"schema_version":1,`, ``, 1),
		"wrong case":                      strings.Replace(xauthProfile, `"schema_version"`, `"SCHEMA_VERSION"`, 1),
		"hostname xauth":                  strings.Replace(xauthProfile, "203.0.113.10", "vpn.example.com", 1),
		"bad IPv4":                        strings.Replace(xauthProfile, "203.0.113.10", "999.1.1.1", 1),
		"leading-zero IPv4":               strings.Replace(xauthProfile, "203.0.113.10", "203.0.113.010", 1),
		"IPv6 gateway":                    strings.Replace(xauthProfile, "203.0.113.10", "2001:db8::1", 1),
		"wrong backend attributes":        strings.Replace(xauthProfile, `"server":"203.0.113.10"`, `"server":"203.0.113.10","force_encapsulation":true`, 1),
		"hostname health without VPN DNS": strings.Replace(xauthProfile, `"host":"192.0.2.10"`, `"host":"health.example.com"`, 1),
		"failclosed optout":               strings.Replace(xauthProfile, `"server":"203.0.113.10"`, `"server":"203.0.113.10","fail_closed":false`, 1),
		"secret in profile":               strings.Replace(xauthProfile, `"name":"example"`, `"name":"example","password":"synthetic-sensitive"`, 1),
		"duplicate field":                 strings.Replace(xauthProfile, `"name":"example"`, `"name":"example","name":"other"`, 1),
		"null config":                     strings.Replace(xauthProfile, `{"server":"203.0.113.10"}`, `null`, 1),
		"null port":                       strings.Replace(xauthProfile, `"port":22`, `"port":null`, 1),
		"bad port":                        strings.Replace(xauthProfile, `"port":22`, `"port":65536`, 1),
		"unsafe name":                     strings.Replace(xauthProfile, `"name":"example"`, `"name":"../example"`, 1),
		"unsafe host":                     strings.Replace(l2tpProfile, "vpn.example.com", "vpn.example.com;id", 1),
		"invalid numeric host":            strings.Replace(l2tpProfile, "vpn.example.com", "999.1.1.1", 1),
		"unknown backend":                 strings.Replace(l2tpProfile, "l2tp-ipsec", "unsupported", 1),
		"proposal command":                strings.Replace(xauthProfile, `"server":"203.0.113.10"`, `"server":"203.0.113.10","ike_proposals":["aes256;id"]`, 1),
	} {
		t.Run(name, func(t *testing.T) {
			_, err := DecodeProfile([]byte(input))
			if err == nil {
				t.Fatal("accepted invalid profile")
			}
			if strings.Contains(err.Error(), "synthetic-sensitive") {
				t.Fatal("secret disclosed")
			}
		})
	}
	if _, err := DecodeProfile([]byte(strings.Replace(xauthProfile, `"schema_version":1`, `"schema_version":9`, 1))); !errors.Is(err, ErrProfileVersion) {
		t.Fatal("schema error not classified", err)
	}
}

func TestBackendMetadataAndDefaults(t *testing.T) {
	for _, kind := range []BackendType{L2TP, XAuth} {
		d, err := DescribeBackend(kind)
		if err != nil || len(d.ConfigFields) == 0 || len(d.CredentialFields) != 3 || !d.Capabilities.RequireFailClosed {
			t.Fatal("incomplete descriptor", err)
		}
		if d.Capabilities.MFA != (kind == XAuth) || d.Capabilities.AutomaticRecovery != (kind == L2TP) {
			t.Fatal("wrong capabilities")
		}
		for _, f := range d.CredentialFields {
			if !f.Secret || !f.Required {
				t.Fatal("credential metadata unsafe")
			}
		}
		for _, f := range d.ConfigFields {
			if kind == XAuth && f.Name == "force_encapsulation" {
				t.Fatal("wrong backend fields")
			}
		}
	}
	if _, err := DescribeBackend("unknown"); !errors.Is(err, ErrBackend) {
		t.Fatal(err)
	}
	c := EffectiveL2TPConfig(L2TPConfig{})
	if !*c.ForceEncapsulation || len(c.IKEProposals) == 0 || len(c.ESPProposals) == 0 {
		t.Fatal("defaults missing")
	}
	disabled := false
	input := L2TPConfig{ForceEncapsulation: &disabled, DNS: []string{"192.0.2.53"}, IKEProposals: []string{"aes256-sha256-modp2048!"}}
	copy := EffectiveL2TPConfig(input)
	*copy.ForceEncapsulation = true
	copy.DNS[0] = "192.0.2.54"
	copy.IKEProposals[0] = "changed"
	if disabled || input.DNS[0] != "192.0.2.53" || input.IKEProposals[0] != "aes256-sha256-modp2048!" {
		t.Fatal("config aliasing")
	}
	x := EffectiveXAuthConfig(XAuthConfig{})
	if len(x.IKEProposals) != 2 || len(x.ESPProposals) != 2 {
		t.Fatal("XAuth defaults changed")
	}
	if (Backend{Type: XAuth, XAuth: &XAuthConfig{Server: "203.0.113.10"}, L2TP: &L2TPConfig{Server: "vpn.example.com"}}).Validate() == nil {
		t.Fatal("mixed backend accepted")
	}
}

func TestLegacyIdentityNeverImplicitlyAdopted(t *testing.T) {
	d, err := Identity("example")
	if err != nil {
		t.Fatal(err)
	}
	if !d.Matches("vpnmodo-example", map[string]string{ManagerLabel: ManagerValue, ProfileLabel: "example"}) {
		t.Fatal("managed identity rejected")
	}
	for _, labels := range []map[string]string{nil, {ProfileLabel: "example"}, {ManagerLabel: ManagerValue}, {ManagerLabel: "legacy", ProfileLabel: "example"}, {ManagerLabel: ManagerValue, ProfileLabel: "other"}} {
		if d.Matches(d.Project, labels) {
			t.Fatal("unowned container matched")
		}
	}
	if d.Matches("vpn-example", d.Labels) {
		t.Fatal("legacy project matched")
	}
	if _, err := Identity("Example"); err == nil {
		t.Fatal("invalid identity")
	}
	if (DeploymentIdentity{}).Matches("", nil) {
		t.Fatal("zero identity matched")
	}
}

func FuzzDecodeProfile(f *testing.F) {
	for _, seed := range []string{xauthProfile, l2tpProfile, `null`, `{"backend":{"type":"unknown"}}`} {
		f.Add([]byte(seed))
	}
	f.Fuzz(func(t *testing.T, data []byte) {
		p, err := DecodeProfile(data)
		if err != nil {
			return
		}
		encoded, err := json.Marshal(p)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := DecodeProfile(encoded); err != nil {
			t.Fatal("accepted profile cannot round trip", err)
		}
	})
}

func TestXAuthHostnameHealthRequiresExplicitVPNResolver(t *testing.T) {
	input := strings.Replace(xauthProfile, `"host":"192.0.2.10"`, `"host":"health.example.com"`, 1)
	input = strings.Replace(input, `"server":"203.0.113.10"`, `"server":"203.0.113.10","dns":["192.0.2.53"]`, 1)
	if _, err := DecodeProfile([]byte(input)); err != nil {
		t.Fatal(err)
	}
}
