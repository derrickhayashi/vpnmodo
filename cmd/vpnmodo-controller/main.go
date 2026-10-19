package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"net"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/derrickhayashi/vpn-sidecars-next/internal/controller"
	"github.com/derrickhayashi/vpn-sidecars-next/internal/protocol"
	"github.com/derrickhayashi/vpn-sidecars-next/internal/rpcio"
)

var version = "dev"

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "vpnmodo-controller:", err)
		os.Exit(1)
	}
}
func run(args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("expected serve, bridge, health or version")
	}
	if args[0] == "version" && len(args) == 1 {
		fmt.Fprintln(os.Stdout, version)
		return nil
	}
	flags := flag.NewFlagSet("controller", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	socket := flags.String("socket", controller.DefaultSocket, "")
	root := os.Getenv("VPNMODO_STATE_DIR")
	if root == "" {
		root = "/var/lib/vpnmodo/controller-state"
	}
	state := flags.String("state-dir", root, "")
	docker := flags.String("docker-socket", "/var/run/docker.sock", "")
	if flags.Parse(args[1:]) != nil || flags.NArg() != 0 {
		return fmt.Errorf("invalid command options")
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	switch args[0] {
	case "serve":
		if !filepath.IsAbs(*state) {
			return controller.ErrService
		}
		server, err := controller.Listen(controller.Config{Socket: *socket, StateDir: *state, DockerSocket: *docker, Version: version, EnableManagedVPN: true, EnableXAuth: true, XAuthImage: os.Getenv("VPNMODO_XAUTH_IMAGE"), L2TPImage: os.Getenv("VPNMODO_L2TP_IMAGE"), ApplicationImage: os.Getenv("VPNMODO_APPLICATION_IMAGE")})
		if err != nil {
			return err
		}
		defer server.Close()
		return server.Serve(ctx)
	case "bridge":
		return controller.Bridge(ctx, *socket, os.Stdin, os.Stdout)
	case "health":
		ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
		defer cancel()
		conn, err := (&net.Dialer{}).DialContext(ctx, "unix", *socket)
		if err != nil {
			return controller.ErrService
		}
		client, err := rpcio.Connect(ctx, conn)
		if err != nil {
			return err
		}
		defer client.Close()
		var status controller.Status
		if err := client.Call(ctx, protocol.SystemStatus, struct{}{}, &status); err != nil {
			return err
		}
		if !status.StateReady {
			return controller.ErrService
		}
		return nil
	default:
		return fmt.Errorf("unknown controller command")
	}
}
