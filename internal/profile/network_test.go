package profile

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestNetworkMTUValidationAndRoundTrip(t *testing.T) {
	for _, base := range []string{xauthProfile, l2tpProfile} {
		for _, config := range []string{`{"mtu":576}`, `{"mtu":1300}`, `{"mtu":1500}`} {
			input := strings.TrimSuffix(base, "}") + `,"network":` + config + `}`
			p, err := DecodeProfile([]byte(input))
			if err != nil || p.Network == nil || p.Network.MSS() != p.Network.MTU-40 {
				t.Fatal("network configuration rejected", err)
			}
			data, err := json.Marshal(p)
			if err != nil {
				t.Fatal(err)
			}
			again, err := DecodeProfile(data)
			if err != nil || *again.Network != *p.Network {
				t.Fatal("network round trip", err)
			}
		}
		for _, config := range []string{`null`, `{}`, `{"mtu":0}`, `{"mtu":575}`, `{"mtu":1501}`, `{"mtu":-1}`, `{"mtu":1300.5}`, `{"mtu":"1300"}`, `{"mtu":null}`, `{"mtu":1300,"mtu":1400}`, `{"mtu":1300,"mss":1260}`, `{"routes":[]}`} {
			input := strings.TrimSuffix(base, "}") + `,"network":` + config + `}`
			if _, err := DecodeProfile([]byte(input)); err == nil {
				t.Fatalf("accepted invalid network configuration: %s", config)
			}
		}
	}
}
