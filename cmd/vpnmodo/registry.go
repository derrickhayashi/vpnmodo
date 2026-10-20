package main

import (
	"errors"

	"github.com/derrickhayashi/vpnmodo/internal/credentialinput"
	"github.com/derrickhayashi/vpnmodo/internal/jsonstrict"
	"github.com/derrickhayashi/vpnmodo/internal/privatefile"
	"github.com/derrickhayashi/vpnmodo/internal/secret"
)

type registryCredentials struct {
	Username secret.Value `json:"username"`
	Token    secret.Value `json:"token"`
}

func readRegistryFile(path string) (*registryCredentials, error) {
	data, err := privatefile.ReadInput(path, 16*1024)
	if err != nil {
		return nil, errors.New("registry auth file unavailable; use a readable regular JSON file")
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
