package operation

// NetworkStatus contains packet-size evidence only; route destinations never
// enter diagnostics. Effective values are present only after live verification.
type NetworkStatus struct {
	ConfiguredMTU int    `json:"configured_mtu"`
	DerivedMSS    int    `json:"derived_mss"`
	EffectiveMTU  int    `json:"effective_mtu,omitempty"`
	EffectiveMSS  int    `json:"effective_mss,omitempty"`
	Verified      bool   `json:"verified"`
	Code          string `json:"code"`
}

// RuntimeView is a fresh, read-only observation, not authority to start a login.
// ApplicationID is exposed only for a healthy, desired-connected deployment.
type RuntimeView struct {
	Network                   *NetworkStatus `json:"network,omitempty"`
	ProfileRevision           uint64         `json:"profile_revision"`
	CredentialsPresent        bool           `json:"credentials_present"`
	PrerequisitesReady        bool           `json:"prerequisites_ready"`
	TunnelReady               bool           `json:"tunnel_ready"`
	PrivateEndpointConfigured bool           `json:"private_endpoint_configured"`
	PrivateEndpointReady      bool           `json:"private_endpoint_ready"`
	Connection                Connection     `json:"connection"`
	Health                    HealthState    `json:"health"`
	ApplicationID             string         `json:"application_id,omitempty"`
}
