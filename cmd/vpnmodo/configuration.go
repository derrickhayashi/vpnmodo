package main

import (
	"errors"

	"github.com/derrickhayashi/vpn-sidecars-next/internal/credentialinput"
	"github.com/derrickhayashi/vpn-sidecars-next/internal/jsonstrict"
	"github.com/derrickhayashi/vpn-sidecars-next/internal/privatefile"
	"github.com/derrickhayashi/vpn-sidecars-next/internal/profile"
	"github.com/derrickhayashi/vpn-sidecars-next/internal/secret"
)

// The input file includes secrets; the controller profile and local reference do not.
type profileFile struct {
	SchemaVersion int                  `json:"schema_version"`
	Name          string               `json:"name"`
	Backend       profile.Backend      `json:"backend"`
	Health        profile.TCPHealth    `json:"health"`
	Credentials   *profile.Credentials `json:"credentials,omitempty"`
	Registry      *registryCredentials `json:"registry,omitempty"`
}

type registryCredentials struct {
	Username secret.Value `json:"username"`
	Token    secret.Value `json:"token"`
}

func (f profileFile) configuration() profile.Profile {
	return profile.Profile{SchemaVersion: f.SchemaVersion, Name: f.Name, Backend: f.Backend, Health: f.Health}
}

func readProfileFile(path string) (profileFile, error) {
	var result profileFile
	data, err := privatefile.Read(path, profile.MaxProfileBytes)
	if err != nil {
		return result, errors.New("profile file unavailable; use an owner-only regular JSON file (chmod 600)")
	}
	defer clear(data)
	if err := jsonstrict.Decode(data, &result, profile.MaxProfileBytes); err != nil {
		return profileFile{}, err
	}
	if err := result.configuration().Validate(); err != nil {
		return profileFile{}, err
	}
	if result.Credentials != nil && result.Credentials.Validate() != nil {
		return profileFile{}, profile.ErrCredentials
	}
	if result.Registry != nil {
		auth, err := credentialinput.RegistryConfig(result.Registry.Username, result.Registry.Token)
		clear(auth)
		if err != nil {
			return profileFile{}, errors.New("invalid registry credentials in JSON")
		}
	}
	return result, nil
}
