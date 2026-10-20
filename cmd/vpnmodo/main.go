package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"syscall"

	"github.com/derrickhayashi/vpn-sidecars-next/internal/host"
)

var version = "dev"

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := run(ctx, os.Args[1:], os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, "vpnmodo:", err)
		os.Exit(1)
	}
}
func run(base context.Context, args []string, output io.Writer) error {
	if len(args) > 0 && args[0] == "__proxy" {
		return proxyCommand(base, args)
	}
	if len(args) == 0 || len(args) == 1 && (args[0] == "--help" || args[0] == "help") {
		fmt.Fprintln(output, "Usage: vpnmodo setup [--registry-auth FILE]\n       vpnmodo {up|down|status|doctor} PROFILE\n       vpnmodo profile help\n       vpnmodo service {configure|start|stop|status} PROFILE\n       vpnmodo operation {status|cancel|watch|submit-mfa} ID\n       vpnmodo proxy PROFILE HOST PORT\n       vpnmodo version")
		return nil
	}
	root := flag.NewFlagSet("vpnmodo", flag.ContinueOnError)
	root.SetOutput(io.Discard)
	registry := root.String("config", "", "")
	if root.Parse(args) != nil {
		return errors.New("invalid global options")
	}
	args = root.Args()
	if len(args) == 1 && args[0] == "version" {
		fmt.Fprintln(output, version)
		return nil
	}
	if len(args) > 0 && args[0] == "setup" {
		return setupCommand(base, *registry, args[1:], output)
	}
	if len(args) > 0 && args[0] == "proxy" {
		return streamCommand(base, *registry, args, os.Stdin, output)
	}
	if len(args) > 0 && (args[0] == "service" || args[0] == "up" || args[0] == "down" || args[0] == "status" || args[0] == "doctor") {
		if *registry == "" {
			path, err := host.DefaultRegistry()
			if err != nil {
				return err
			}
			*registry = path
		}
		return accessCommand(base, *registry, args, output)
	}
	if len(args) >= 1 && (args[0] == "connection" || args[0] == "operation") {
		if *registry == "" {
			path, err := host.DefaultRegistry()
			if err != nil {
				return err
			}
			*registry = path
		}
		return operationCommand(base, *registry, args, output)
	}
	if len(args) >= 2 && args[0] == "profile" {
		if *registry == "" {
			path, err := host.DefaultRegistry()
			if err != nil {
				return err
			}
			*registry = path
		}
		return profileCommand(base, *registry, args[1:], output)
	}
	return errors.New("unknown command; run vpnmodo --help")
}
