package controller

// The synthetic process exists only in the Go test binary. Production controller
// builds cannot enable it, even with environment variables or crafted profiles.
import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"os"
	"os/signal"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/derrickhayashi/vpn-sidecars-next/internal/lifecycle"
	"github.com/derrickhayashi/vpn-sidecars-next/internal/managedvpn"
	"github.com/derrickhayashi/vpn-sidecars-next/internal/operation"
	"github.com/derrickhayashi/vpn-sidecars-next/internal/privatefile"
	"github.com/derrickhayashi/vpn-sidecars-next/internal/profile"
	"github.com/derrickhayashi/vpn-sidecars-next/internal/protocol"
	"github.com/derrickhayashi/vpn-sidecars-next/internal/rpcio"
	"github.com/derrickhayashi/vpn-sidecars-next/internal/secret"
)

func TestMain(m *testing.M) {
	if len(os.Args) > 1 && (os.Args[1] == "serve" || os.Args[1] == "bridge" || os.Args[1] == "health" || os.Args[1] == "version") {
		if os.Getenv("VPNMODO_OPERATION_TEST_PROCESS") != "1" {
			os.Exit(2)
		}
		ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
		defer stop()
		if runSyntheticProcess(ctx, os.Args[1:]) != nil {
			fmt.Fprintln(os.Stderr, "synthetic acceptance controller failed")
			os.Exit(1)
		}
		os.Exit(0)
	}
	os.Exit(m.Run())
}
func runSyntheticProcess(ctx context.Context, args []string) error {
	flags := flag.NewFlagSet("synthetic", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	socket := flags.String("socket", DefaultSocket, "")
	root := flags.String("state-dir", os.Getenv("VPNMODO_STATE_DIR"), "")
	if flags.Parse(args[1:]) != nil {
		return ErrService
	}
	switch args[0] {
	case "version":
		fmt.Println("synthetic-test-only")
		return nil
	case "bridge":
		return Bridge(ctx, *socket, os.Stdin, os.Stdout)
	case "health":
		ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
		defer cancel()
		conn, err := (&net.Dialer{}).DialContext(ctx, "unix", *socket)
		if err != nil {
			return err
		}
		client, err := rpcio.Connect(ctx, conn)
		if err != nil {
			return err
		}
		defer client.Close()
		var status Status
		if err = client.Call(ctx, protocol.SystemStatus, struct{}{}, &status); err != nil {
			return err
		}
		if !status.StateReady {
			return ErrService
		}
		return nil
	default:
		if *root == "" {
			return ErrService
		}
		adapter, err := newFileAdapter(filepath.Join(*root, "synthetic-runtime"))
		if err != nil {
			return err
		}
		config := Config{Socket: *socket, StateDir: *root, DockerSocket: "/var/run/docker.sock", Version: "synthetic-test-only", Adapter: adapter, OperationTimeout: 3 * time.Minute, MFATimeout: 90 * time.Second, Recovery: lifecycle.RecoveryPolicy{Interval: 100 * time.Millisecond, ProbeTimeout: 3 * time.Second, Cooldown: 2 * time.Second, MaxBackoff: 8 * time.Second}}
		if os.Getenv("VPNMODO_L2TP_TEST_PROCESS") == "1" {
			config.Adapter = nil
			config.L2TPRunner = &traceDocker{root: *root, base: managedvpn.CLI{Binary: "/usr/local/bin/docker", Socket: "/var/run/docker.sock"}}
			config.EnableL2TP = true
			config.EnableXAuth = os.Getenv("VPNMODO_XAUTH_TEST_PROCESS") == "1"
			if config.EnableXAuth {
				config.MFATimeout = 45 * time.Second
			}
			config.Version = "l2tp-test-only"
			config.OperationTimeout = 3 * time.Minute
			config.Recovery = lifecycle.RecoveryPolicy{Interval: time.Second, ProbeTimeout: 15 * time.Second, Cooldown: 3 * time.Second, MaxBackoff: 12 * time.Second}
		}
		server, err := Listen(config)
		if err != nil {
			return err
		}
		defer server.Close()
		return server.Serve(ctx)
	}
}

type syntheticState struct {
	Health       operation.HealthState `json:"health,omitempty"`
	Forwarding   bool                  `json:"forwarding"`
	Repairs      int                   `json:"repairs"`
	Quiesces     int                   `json:"quiesces"`
	FailRecovery bool                  `json:"fail_recovery"`
	Established  bool                  `json:"established"`
	Connects     int                   `json:"connects"`
	Stops        int                   `json:"stops"`
}
type fileAdapter struct {
	mu   sync.Mutex
	root string
}

func newFileAdapter(root string) (*fileAdapter, error) {
	if privatefile.Dir(root) != nil {
		return nil, ErrService
	}
	return &fileAdapter{root: root}, nil
}
func (f *fileAdapter) read(name string) (syntheticState, error) {
	data, err := privatefile.Read(filepath.Join(f.root, name+".json"), 4096)
	if errors.Is(err, os.ErrNotExist) {
		return syntheticState{}, nil
	}
	if err != nil {
		return syntheticState{}, ErrService
	}
	var state syntheticState
	if json.Unmarshal(data, &state) != nil {
		return state, ErrService
	}
	return state, nil
}
func (f *fileAdapter) write(name string, state syntheticState) error {
	data, _ := json.Marshal(state)
	_, err := privatefile.Write(filepath.Join(f.root, name+".json"), data)
	return err
}
func (f *fileAdapter) Observe(_ context.Context, p profile.Profile) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	state, err := f.read(p.Name)
	return state.Established, err
}
func (f *fileAdapter) Connect(ctx context.Context, p profile.Profile, challenge func(context.Context) (secret.Value, error)) error {
	f.mu.Lock()
	state, err := f.read(p.Name)
	if err == nil {
		state.Connects++
		err = f.write(p.Name, state)
	}
	f.mu.Unlock()
	if err != nil {
		return err
	}
	if state.FailRecovery {
		return lifecycle.ErrUnavailable
	}
	if p.Name == "pending-mfa" || p.Name == "interactive-mfa" {
		code, err := challenge(ctx)
		if err != nil {
			return err
		}
		if !code.Check(func(v string) bool { return v == "9876543210" }) {
			return lifecycle.ErrAuthentication
		}
	}
	timer := time.NewTimer(100 * time.Millisecond)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	state, err = f.read(p.Name)
	if err != nil {
		return err
	}
	state.Established = true
	state.Health = ""
	return f.write(p.Name, state)
}
func (f *fileAdapter) Verify(ctx context.Context, p profile.Profile) error {
	established, err := f.Observe(ctx, p)
	if err != nil {
		return err
	}
	if !established {
		return lifecycle.ErrUnavailable
	}
	return ctx.Err()
}
func (f *fileAdapter) Disconnect(ctx context.Context, p profile.Profile) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	state, err := f.read(p.Name)
	if err != nil {
		return err
	}
	state.Established = false
	state.Forwarding = false
	state.Stops++
	return f.write(p.Name, state)
}
func (f *fileAdapter) CanRecover(p profile.Profile) bool { return p.Backend.Type == profile.L2TP }

// Test-only controls are private state files in the isolated acceptance VM.
func (f *fileAdapter) Check(ctx context.Context, p profile.Profile) (operation.HealthState, error) {
	if ctx.Err() != nil {
		return "", ctx.Err()
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	state, err := f.read(p.Name)
	if err != nil {
		return "", err
	}
	if state.Health != "" {
		return state.Health, nil
	}
	if !state.Established {
		return operation.HealthStopped, nil
	}
	if !state.Forwarding {
		return operation.HealthDetached, nil
	}
	return operation.HealthHealthy, nil
}
func (f *fileAdapter) Quiesce(ctx context.Context, p profile.Profile) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	state, err := f.read(p.Name)
	if err != nil {
		return err
	}
	state.Quiesces++
	state.Forwarding = false
	return f.write(p.Name, state)
}
func (f *fileAdapter) RepairForwarding(ctx context.Context, p profile.Profile) error {
	if err := f.Verify(ctx, p); err != nil {
		return err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	state, err := f.read(p.Name)
	if err != nil {
		return err
	}
	if !state.Forwarding {
		state.Repairs++
	}
	state.Forwarding = true
	if state.Health == operation.HealthDetached {
		state.Health = ""
	}
	return f.write(p.Name, state)
}

// Synthetic lab diagnostics allowlist command phases and process states only.
// No args, environment, credentials, backend text or raw Docker payload is saved.
type traceDocker struct {
	mu    sync.Mutex
	root  string
	base  managedvpn.Runner
	steps []map[string]any
}

func (d *traceDocker) Run(ctx context.Context, args ...string) ([]byte, error) {
	out, err := d.base.Run(ctx, args...)
	phase := args[0]
	if phase == "network" {
		phase += "." + args[1]
	}
	if phase == "compose" {
		phase += "." + args[len(args)-1]
	}
	if phase == "exec" && len(args) > 4 {
		switch args[4] {
		case "/usr/local/bin/vpn-tunnelcheck":
			phase = "tunnel"
		case "/usr/local/bin/vpn-servicecheck":
			phase = "service"
		case "readlink":
			phase = "namespace"
		case "test":
			phase = "ready"
		}
	}
	row := map[string]any{"phase": phase, "ok": err == nil}
	if phase == "service" && err != nil {
		addr, _ := d.base.Run(ctx, "exec", args[1], "ip", "-4", "address", "show", "dev", "ppp0")
		row["expected_local"] = strings.Contains(string(addr), "inet 198.51.100.")
		row["expected_peer"] = strings.Contains(string(addr), "peer 198.51.100.1")
		route, _ := d.base.Run(ctx, "exec", args[1], "ip", "-4", "route", "get", "198.51.100.1")
		row["route_ppp"] = strings.Contains(string(route), "dev ppp0")
		filters, _ := d.base.Run(ctx, "exec", args[1], "iptables", "-nvxL", "OUTPUT")
		if found := regexp.MustCompile(`policy DROP ([0-9]+) packets`).FindSubmatch(filters); len(found) == 2 {
			count, _ := strconv.Atoi(string(found[1]))
			row["dropped_packets"] = count
		}
		for _, line := range strings.Split(string(filters), "\n") {
			fields := strings.Fields(line)
			if len(fields) > 6 && fields[2] == "ACCEPT" && fields[6] == "ppp0" {
				count, _ := strconv.Atoi(fields[0])
				row["ppp_accepted_packets"] = count
			}
		}
	}
	if e, ok := err.(interface{ ExitCode() int }); ok {
		row["exit"] = e.ExitCode()
	}
	if args[0] == "inspect" && err == nil {
		var cs []struct {
			State struct {
				Running  bool
				ExitCode int
			}
			Config struct{ Labels map[string]string }
		}
		if json.Unmarshal(out, &cs) == nil && len(cs) == 1 {
			role := cs[0].Config.Labels["com.docker.compose.service"]
			if role == "vpn" || role == "application" {
				row["role"] = role
				row["running"] = cs[0].State.Running
				row["process_exit"] = cs[0].State.ExitCode
			}
		}
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	d.steps = append(d.steps, row)
	if len(d.steps) > 200 {
		d.steps = d.steps[len(d.steps)-200:]
	}
	data, _ := json.Marshal(d.steps)
	_, _ = privatefile.Write(filepath.Join(d.root, "l2tp-trace.json"), data)
	return out, err
}

// Private input is deliberately excluded from the synthetic diagnostic trace.
func (d *traceDocker) RunInput(ctx context.Context, input []byte, args ...string) ([]byte, error) {
	if r, ok := d.base.(managedvpn.InputRunner); ok {
		return r.RunInput(ctx, input, args...)
	}
	return nil, ErrService
}
