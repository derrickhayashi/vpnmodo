package main

import (
	"errors"
	"github.com/derrickhayashi/vpnmodo/internal/host"
)

func managedReference(path, name string) (host.Reference, error) {
	ref, err := host.FindReference(path, name)
	if err != nil || ref.Host != host.ManagedName {
		return host.Reference{}, errors.New("profile is not configured in this environment; run vpnmodo setup")
	}
	return ref, nil
}
