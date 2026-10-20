package main

import (
	"errors"

	"github.com/derrickhayashi/vpn-sidecars-next/internal/jsonstrict"
	"github.com/derrickhayashi/vpn-sidecars-next/internal/privatefile"
	"github.com/derrickhayashi/vpn-sidecars-next/internal/profile"
)

// The input file includes secrets; the controller profile and local reference do not.
type profileFile struct {
	SchemaVersion int                  `json:"schema_version"`
	Name          string               `json:"name"`
	Backend       profile.Backend      `json:"backend"`
	Health        profile.TCPHealth    `json:"health"`
	Credentials   *profile.Credentials `json:"credentials,omitempty"`
}

func (f profileFile) configuration() profile.Profile {
	return profile.Profile{SchemaVersion: f.SchemaVersion, Name: f.Name, Backend: f.Backend, Health: f.Health}
}

func readProfileFile(path string) (profileFile, error) {
	var result profileFile
	data, err := privatefile.ReadInput(path, profile.MaxProfileBytes)
	if err != nil {
		return result, errors.New("profile file unavailable; use a readable regular JSON file")
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

	return result, nil
}
