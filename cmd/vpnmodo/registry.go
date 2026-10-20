package main

import (
	"errors"

	"github.com/derrickhayashi/vpn-sidecars-next/internal/credentialinput"
	"github.com/derrickhayashi/vpn-sidecars-next/internal/jsonstrict"
	"github.com/derrickhayashi/vpn-sidecars-next/internal/privatefile"
	"github.com/derrickhayashi/vpn-sidecars-next/internal/secret"
)

type registryCredentials struct {
	Username secret.Value `json:"username"`
	Token    secret.Value `json:"token"`
}

func readRegistryFile(path string) (*registryCredentials, error) {
	data, err := privatefile.Read(path, 16*1024)
	if err != nil {
		return nil, errors.New("registry auth file unavailable; use an owner-only regular JSON file (chmod 600)")
	}
	defer clear(data)
	var result registryCredentials
	if err := jsonstrict.Decode(data, &result, 16*1024); err != nil {
		return nil, err
	}
	encoded, err := credentialinput.RegistryConfig(result.Username, result.Token)
	clear(encoded)
	if err != nil {
		return nil, errors.New("invalid registry auth; JSON needs username and token")
	}
	return &result, nil
}
