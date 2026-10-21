package profile

import "regexp"

// IKEv2Config deliberately supports a bounded subset of swanctl syntax. Values
// are data, never snippets of strongSwan configuration.
type IKEv2Config struct {
	Server         string   `json:"server" required:"true" format:"hostname-or-ipv4"`
	ServerIdentity string   `json:"server_identity" required:"true" format:"ike-identity"`
	ClientIdentity string   `json:"client_identity,omitempty" format:"ike-identity"`
	EAPIdentity    string   `json:"eap_identity,omitempty" format:"ike-identity"`
	DNS            []string `json:"dns,omitempty" format:"ipv4-list"`
	IKEProposals   []string `json:"ike_proposals,omitempty" format:"ikev2-proposal-list"`
	ESPProposals   []string `json:"esp_proposals,omitempty" format:"ikev2-esp-proposal-list"`
}

var identityPattern = regexp.MustCompile(`^[a-zA-Z0-9_@][a-zA-Z0-9_.@:+/-]{0,252}$`)
var ikev2IKE = regexp.MustCompile(`^(aes(128|256)-sha(256|384|512)(-prfsha(256|384|512))?|aes(128|256)gcm16-prfsha(256|384|512))-(modp(2048|3072|4096)|ecp(256|384|521)|curve25519)$`)
var ikev2ESP = regexp.MustCompile(`^(aes(128|256)-sha(256|384|512)|aes(128|256)gcm16)(-(modp(2048|3072|4096)|ecp(256|384|521)|curve25519))?$`)

func (c IKEv2Config) validate() error {
	if !validHost(c.Server) || !identityPattern.MatchString(c.ServerIdentity) || len(c.DNS) > 8 {
		return ErrProfile
	}
	for _, id := range []string{c.ClientIdentity, c.EAPIdentity} {
		if id != "" && !identityPattern.MatchString(id) {
			return ErrProfile
		}
	}
	for _, dns := range c.DNS {
		if !ipv4(dns) {
			return ErrProfile
		}
	}
	for i, list := range [][]string{c.IKEProposals, c.ESPProposals} {
		if len(list) > 32 {
			return ErrProfile
		}
		pattern := ikev2IKE
		if i == 1 {
			pattern = ikev2ESP
		}
		for _, proposal := range list {
			if !pattern.MatchString(proposal) {
				return ErrProfile
			}
		}
	}
	return nil
}

func EffectiveIKEv2Config(c IKEv2Config) IKEv2Config {
	c.DNS = append([]string(nil), c.DNS...)
	c.IKEProposals = proposalsOr(c.IKEProposals, []string{"aes256-sha256-modp2048"})
	c.ESPProposals = proposalsOr(c.ESPProposals, []string{"aes256-sha256"})
	return c
}
