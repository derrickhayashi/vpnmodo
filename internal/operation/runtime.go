package operation

// RuntimeView is a fresh, read-only observation, not authority to start a login.
// ApplicationID is exposed only for a healthy, desired-connected deployment.
type RuntimeView struct {
	ProfileRevision      uint64      `json:"profile_revision"`
	CredentialsPresent   bool        `json:"credentials_present"`
	PrerequisitesReady   bool        `json:"prerequisites_ready"`
	TunnelReady          bool        `json:"tunnel_ready"`
	PrivateEndpointReady bool        `json:"private_endpoint_ready"`
	Connection           Connection  `json:"connection"`
	Health               HealthState `json:"health"`
	ApplicationID        string      `json:"application_id,omitempty"`
}
