package profile

// GlobalProtectConfig connects directly to one gateway. Portal discovery and
// server-supplied routing/DNS are deliberately outside this bounded contract.
type GlobalProtectConfig struct {
	Server                string   `json:"server" required:"true" format:"hostname-or-ipv4"`
	DNS                   []string `json:"dns,omitempty" format:"ipv4-list"`
	InsecureSkipTLSVerify bool     `json:"insecure_skip_tls_verify,omitempty"`
}

func (c GlobalProtectConfig) validate() error {
	if !validHost(c.Server) || len(c.DNS) > 8 {
		return ErrProfile
	}
	for _, address := range c.DNS {
		if !ipv4(address) {
			return ErrProfile
		}
	}
	return nil
}
