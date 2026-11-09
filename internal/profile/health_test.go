package profile

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestOptionalHealthForEveryBackend(t *testing.T) {
	paths, err := filepath.Glob("../../examples/profiles/v1/*.json")
	if err != nil || len(paths) != 4 {
		t.Fatal("backend fixtures", err)
	}
	for _, path := range paths {
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		p, err := DecodeProfile(raw)
		if err != nil {
			t.Fatal(err)
		}
		t.Run(string(p.Backend.Type), func(t *testing.T) {
			p.Health = TCPHealth{}
			switch p.Backend.Type {
			case L2TP:
				p.Backend.L2TP.DNS = nil
			case XAuth:
				p.Backend.XAuth.DNS = nil
			case IKEv2:
				p.Backend.IKEv2.DNS = nil
			case GlobalProtect:
				p.Backend.GlobalProtect.DNS = nil
			}
			if p.Validate() != nil {
				t.Fatal("endpoint still required")
			}
			encoded, err := json.Marshal(p)
			if err != nil || strings.Contains(string(encoded), `"health"`) {
				t.Fatal("disabled health not omitted")
			}
			if _, err = DecodeProfile(encoded); err != nil {
				t.Fatal(err)
			}
			var wire map[string]json.RawMessage
			if json.Unmarshal(encoded, &wire) != nil {
				t.Fatal("fixture decode")
			}
			for _, invalid := range []string{`null`, `{}`, `{"host":""}`, `{"port":0}`, `{"host":"192.0.2.10"}`, `{"port":22}`, `{"host":"192.0.2.10","port":0}`} {
				wire["health"] = json.RawMessage(invalid)
				bad, err := json.Marshal(wire)
				if err != nil {
					t.Fatal(err)
				}
				if _, err := DecodeProfile(bad); err == nil {
					t.Fatal("invalid supplied health object accepted")
				}
			}
			for _, health := range []TCPHealth{{Host: "192.0.2.10"}, {Port: 22}, {Host: "192.0.2.10", Port: -1}, {Host: "192.0.2.10", Port: 65536}, {Host: "bad;host", Port: 22}} {
				p.Health = health
				if p.Validate() == nil {
					t.Fatal("invalid configured endpoint accepted")
				}
			}
			p.Health = TCPHealth{Host: "192.0.2.10", Port: 22}
			if p.Validate() != nil {
				t.Fatal("valid endpoint refused")
			}
		})
	}
}
