package main

import (
	"context"
	"encoding/hex"
	"errors"
	"io"
	"strconv"
	"time"

	"github.com/derrickhayashi/vpnmodo/internal/access"
	"github.com/derrickhayashi/vpnmodo/internal/host"
	"github.com/derrickhayashi/vpnmodo/internal/protocol"
)

// A stable SSH ProxyCommand resolves the currently healthy application each time.
// It never starts a connection, retries authentication or asks for MFA.
func streamCommand(ctx context.Context, registry string, args []string, input io.Reader, output io.Writer) error {
	invalid := errors.New("usage: vpnmodo [--config FILE] proxy ALIAS HOST PORT")
	if len(args) != 4 || !access.Endpoint(args[2]) || len(args[3]) > 5 {
		return invalid
	}
	port, err := strconv.Atoi(args[3])
	if err != nil || port < 1 || port > 65535 {
		return invalid
	}
	if registry == "" {
		registry, err = host.DefaultRegistry()
		if err != nil {
			return err
		}
	}
	ref, err := managedReference(host.ReferencePath(registry), args[1])
	if err != nil {
		return err
	}
	vm, err := host.Find(registry, ref.Host)
	if err != nil {
		return err
	}
	check, cancel := context.WithTimeout(ctx, 20*time.Second)
	client, err := host.Connect(check, vm)
	if err != nil {
		cancel()
		return err
	}
	var result struct {
		ContainerID string `json:"container_id"`
	}
	err = client.Call(check, protocol.ConnectionApplication, protocol.ProfileParams{Profile: ref.Profile}, &result)
	client.Close()
	cancel()
	if err != nil {
		return err
	}
	if len(result.ContainerID) != 64 {
		return errors.New("healthy application unavailable")
	}
	if _, err = hex.DecodeString(result.ContainerID); err != nil {
		return errors.New("healthy application unavailable")
	}
	return host.Proxy(ctx, vm, host.ProxyRequest{ApplicationID: result.ContainerID, Profile: ref.Profile, Host: args[2], Port: port}, input, output)
}
