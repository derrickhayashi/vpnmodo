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
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/derrickhayashi/vpn-sidecars-next/internal/lifecycle"
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
		server, err := Listen(Config{Socket: *socket, StateDir: *root, DockerSocket: "/var/run/docker.sock", Version: "synthetic-test-only", Adapter: adapter, OperationTimeout: time.Minute, MFATimeout: 20 * time.Second})
		if err != nil {
			return err
		}
		defer server.Close()
		return server.Serve(ctx)
	}
}

type syntheticState struct {
	Established bool `json:"established"`
	Connects    int  `json:"connects"`
	Stops       int  `json:"stops"`
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
	state.Stops++
	return f.write(p.Name, state)
}
func (f *fileAdapter) CanRecover(p profile.Profile) bool { return p.Backend.Type == profile.L2TP }
