// Package profile defines backend-specific VPN settings, credential validation
// and managed deployment identities without transport or runtime side effects.
package profile

import (
	"encoding/json"
	"errors"
	"net/netip"
	"regexp"
	"strings"

	"github.com/derrickhayashi/vpn-sidecars-next/internal/jsonstrict"
)

const (
	ProfileVersion  = 1
	MaxProfileBytes = 64 * 1024
)

type BackendType string

const (
	L2TP  BackendType = "l2tp-ipsec"
	XAuth BackendType = "ipsec-xauth"
)

var (
	ErrProfile        = errors.New("invalid profile")
	ErrBackend        = errors.New("unsupported backend")
	ErrProfileVersion = errors.New("unsupported profile schema version")
	namePattern       = regexp.MustCompile(`^[a-z0-9]+(-[a-z0-9]+)*$`)
	proposalPattern   = regexp.MustCompile(`^[a-z0-9]+(-[a-z0-9]+)*!?$`)
)

type Profile struct {
	SchemaVersion int       `json:"schema_version"`
	Name          string    `json:"name"`
	Backend       Backend   `json:"backend"`
	Health        TCPHealth `json:"health"`
}

type TCPHealth struct {
	Host string `json:"host"`
	Port int    `json:"port"`
}

// Backend is a discriminated union. Exactly one config must match Type.
type Backend struct {
	Type  BackendType  `json:"type"`
	L2TP  *L2TPConfig  `json:"-"`
	XAuth *XAuthConfig `json:"-"`
}

type L2TPConfig struct {
	Server             string   `json:"server" required:"true" format:"hostname-or-ipv4"`
	DNS                []string `json:"dns,omitempty" format:"ipv4-list"`
	ForceEncapsulation *bool    `json:"force_encapsulation,omitempty"`
	IKEProposals       []string `json:"ike_proposals,omitempty" format:"proposal-list"`
	ESPProposals       []string `json:"esp_proposals,omitempty" format:"proposal-list"`
}

type XAuthConfig struct {
	Server       string   `json:"server" required:"true" format:"ipv4"`
	IKEProposals []string `json:"ike_proposals,omitempty" format:"proposal-list"`
	ESPProposals []string `json:"esp_proposals,omitempty" format:"proposal-list"`
}

func ValidName(name string) bool {
	return len(name) > 0 && len(name) <= 48 && namePattern.MatchString(name)
}

func DecodeProfile(data []byte) (Profile, error) {
	var p Profile
	if err := jsonstrict.Decode(data, &p, MaxProfileBytes); err != nil {
		return Profile{}, err
	}
	if err := p.Validate(); err != nil {
		return Profile{}, err
	}
	return p, nil
}

func (p Profile) Validate() error {
	if p.SchemaVersion != ProfileVersion {
		return ErrProfileVersion
	}
	if !ValidName(p.Name) || !validHost(p.Health.Host) || p.Health.Port < 1 || p.Health.Port > 65535 {
		return ErrProfile
	}
	return p.Backend.Validate()
}

func (b Backend) Validate() error {
	switch b.Type {
	case L2TP:
		if b.L2TP == nil || b.XAuth != nil {
			return ErrProfile
		}
		c := b.L2TP
		if !validHost(c.Server) || len(c.DNS) > 8 {
			return ErrProfile
		}
		for _, address := range c.DNS {
			if !ipv4(address) {
				return ErrProfile
			}
		}
		return validateProposals(c.IKEProposals, c.ESPProposals)
	case XAuth:
		if b.XAuth == nil || b.L2TP != nil {
			return ErrProfile
		}
		if !ipv4(b.XAuth.Server) {
			return ErrProfile
		}
		return validateProposals(b.XAuth.IKEProposals, b.XAuth.ESPProposals)
	default:
		return ErrBackend
	}
}

func (b Backend) MarshalJSON() ([]byte, error) {
	if err := b.Validate(); err != nil {
		return nil, err
	}
	var config any = b.L2TP
	if b.Type == XAuth {
		config = b.XAuth
	}
	return json.Marshal(struct {
		Type   BackendType `json:"type"`
		Config any         `json:"config"`
	}{b.Type, config})
}

func (b *Backend) UnmarshalJSON(data []byte) error {
	var wire struct {
		Type   BackendType     `json:"type"`
		Config json.RawMessage `json:"config"`
	}
	if err := jsonstrict.Decode(data, &wire, MaxProfileBytes); err != nil {
		return err
	}
	result := Backend{Type: wire.Type}
	switch wire.Type {
	case L2TP:
		result.L2TP = new(L2TPConfig)
		if err := jsonstrict.Decode(wire.Config, result.L2TP, MaxProfileBytes); err != nil {
			return err
		}
	case XAuth:
		result.XAuth = new(XAuthConfig)
		if err := jsonstrict.Decode(wire.Config, result.XAuth, MaxProfileBytes); err != nil {
			return err
		}
	default:
		return ErrBackend
	}
	if err := result.Validate(); err != nil {
		return err
	}
	*b = result
	return nil
}

func ipv4(host string) bool {
	address, err := netip.ParseAddr(host)
	return err == nil && address.Is4()
}

func validHost(host string) bool {
	if ipv4(host) {
		return true
	}
	if len(host) == 0 || len(host) > 253 || strings.TrimSpace(host) != host {
		return false
	}
	// Numeric dotted strings must parse as IPv4; do not reinterpret bad IPs as DNS.
	if strings.Trim(host, "0123456789.") == "" {
		return false
	}
	for _, label := range strings.Split(host, ".") {
		if len(label) == 0 || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
			return false
		}
		for _, c := range label {
			if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '-') {
				return false
			}
		}
	}
	return true
}

func validateProposals(groups ...[]string) error {
	for _, proposals := range groups {
		if len(proposals) > 32 {
			return ErrProfile
		}
		for _, proposal := range proposals {
			if len(proposal) > 256 || !proposalPattern.MatchString(proposal) {
				return ErrProfile
			}
		}
	}
	return nil
}

// EffectiveL2TPConfig returns a private copy with legacy-compatible defaults.
func EffectiveL2TPConfig(c L2TPConfig) L2TPConfig {
	if c.ForceEncapsulation == nil {
		v := true
		c.ForceEncapsulation = &v
	} else {
		v := *c.ForceEncapsulation
		c.ForceEncapsulation = &v
	}
	c.DNS = append([]string(nil), c.DNS...)
	c.IKEProposals = proposalsOr(c.IKEProposals, []string{"aes256-sha256-modp2048", "aes256-sha1-modp2048", "aes256-sha1-modp1024", "aes128-sha1-modp1024", "3des-sha1-modp1024"})
	c.ESPProposals = proposalsOr(c.ESPProposals, []string{"aes256-sha256", "aes256-sha1", "aes128-sha1", "3des-sha1"})
	return c
}

func EffectiveXAuthConfig(c XAuthConfig) XAuthConfig {
	c.IKEProposals = proposalsOr(c.IKEProposals, []string{"aes128-sha1-modp1536", "aes256-sha256-modp1536"})
	c.ESPProposals = proposalsOr(c.ESPProposals, []string{"aes128-sha1-modp1536", "aes256-sha1-modp1536"})
	return c
}

func proposalsOr(input, defaults []string) []string {
	if len(input) == 0 {
		input = defaults
	}
	return append([]string(nil), input...)
}
