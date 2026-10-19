package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/derrickhayashi/vpn-sidecars-next/internal/controller"
	"github.com/derrickhayashi/vpn-sidecars-next/internal/host"
	"github.com/derrickhayashi/vpn-sidecars-next/internal/protocol"
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
	if len(args) == 0 || len(args) == 1 && (args[0] == "--help" || args[0] == "help") {
		fmt.Fprintln(output, "Usage: vpnmodo [--config FILE] host add NAME --ssh TARGET [--ssh-config FILE]\n       vpnmodo [--config FILE] host list\n       vpnmodo [--config FILE] host setup NAME --image IMAGE [--replace] [--json]\n       vpnmodo [--config FILE] host check NAME [--json]\n       vpnmodo profile help\n       vpnmodo connection {ensure|disconnect|status} ALIAS [--wait] [--json]\n       vpnmodo operation {status|cancel|watch} ID --host HOST [--json]\n       vpnmodo version")
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
	if len(args) < 2 || args[0] != "host" {
		return errors.New("usage: vpnmodo [--config FILE] host {add|list|check|setup}")
	}
	if *registry == "" {
		path, err := host.DefaultRegistry()
		if err != nil {
			return err
		}
		*registry = path
	}
	if args[1] == "list" && len(args) == 2 {
		r, err := host.Read(*registry)
		if err != nil {
			return err
		}
		return json.NewEncoder(output).Encode(r.Hosts)
	}
	if len(args) < 3 {
		return errors.New("host name is required")
	}
	name := args[2]
	flags := flag.NewFlagSet("host", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	target := flags.String("ssh", "", "")
	config := flags.String("ssh-config", "", "")
	image := flags.String("image", "", "")
	replace := flags.Bool("replace", false, "")
	machine := flags.Bool("json", false, "")
	if flags.Parse(args[3:]) != nil || flags.NArg() != 0 {
		return errors.New("invalid host options")
	}
	switch args[1] {
	case "add":
		if *image != "" || *replace {
			return errors.New("setup options are invalid for host registration")
		}
		if err := host.Add(*registry, host.Host{Name: name, SSH: *target, SSHConfig: *config}); err != nil {
			return err
		}
		if *machine {
			return json.NewEncoder(output).Encode(struct {
				Name       string `json:"name"`
				Registered bool   `json:"registered"`
			}{name, true})
		}
		fmt.Fprintln(output, "Registered host:", name)
		return nil
	case "check", "setup":
		if *target != "" || *config != "" {
			return errors.New("update the registered host separately")
		}
		h, err := host.Find(*registry, name)
		if err != nil {
			return err
		}
		duration := 20 * time.Second
		if args[1] == "setup" {
			duration = 4 * time.Minute
		} else if *image != "" || *replace {
			return errors.New("setup options are invalid for host check")
		}
		ctx, cancel := context.WithTimeout(base, duration)
		defer cancel()
		if args[1] == "setup" {
			result, err := host.Setup(ctx, h, host.SetupParams{Image: *image, Replace: *replace})
			if err != nil {
				return err
			}
			if *machine {
				return json.NewEncoder(output).Encode(result)
			}
			fmt.Fprintln(output, "Controller", result.Action)
			return nil
		}
		client, err := host.Connect(ctx, h)
		if err != nil {
			return err
		}
		defer client.Close()
		var status controller.Status
		if err := client.Call(ctx, protocol.SystemStatus, struct{}{}, &status); err != nil {
			return err
		}
		if *machine {
			if err := json.NewEncoder(output).Encode(status); err != nil {
				return err
			}
		} else {
			fmt.Fprintf(output, "Controller %s; storage ready: %t; Docker ready: %t\n", status.Version, status.StateReady, status.DockerReady)
		}
		if !status.StateReady || !status.DockerReady {
			return errors.New("controller prerequisites are not ready")
		}
		return nil
	default:
		return errors.New("unknown host command")
	}
}
