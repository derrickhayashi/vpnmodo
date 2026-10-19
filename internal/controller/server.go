// Package controller runs VM-local discovery, status and profile administration.
// VPN connection/operation handlers come in subsequent stages.
package controller

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/creachadair/jrpc2"
	"github.com/derrickhayashi/vpn-sidecars-next/internal/jsonstrict"
	"github.com/derrickhayashi/vpn-sidecars-next/internal/lifecycle"
	"github.com/derrickhayashi/vpn-sidecars-next/internal/operationstore"
	"github.com/derrickhayashi/vpn-sidecars-next/internal/profile"
	"github.com/derrickhayashi/vpn-sidecars-next/internal/profilestore"
	"github.com/derrickhayashi/vpn-sidecars-next/internal/protocol"
	"github.com/derrickhayashi/vpn-sidecars-next/internal/rpcio"
)

const DefaultSocket = "/run/vpnmodo/controller.sock"

var ErrService = errors.New("controller unavailable; check private runtime and state directories")

type Status struct {
	Version        string              `json:"version"`
	API            protocol.APIVersion `json:"api"`
	StateReady     bool                `json:"state_ready"`
	DockerReady    bool                `json:"docker_ready"`
	RuntimeEnabled bool                `json:"runtime_enabled"`
}
type Config struct {
	Socket, StateDir, DockerSocket, Version string
	Adapter                                 lifecycle.Adapter
	OperationTimeout, MFATimeout            time.Duration
}
type Server struct {
	profiles    *profilestore.Store
	operations  *lifecycle.Manager
	cancel      context.CancelFunc
	closeOnce   sync.Once
	closeErr    error
	config      Config
	listener    net.Listener
	mu          sync.Mutex
	connections map[net.Conn]bool
	workers     sync.WaitGroup
	slots       chan struct{}
	closed      bool
}

// Listen refuses existing socket paths instead of unlinking another service.
func Listen(config Config) (*Server, error) {
	if config.Socket == "" || config.StateDir == "" || !filepath.IsAbs(config.Socket) || !filepath.IsAbs(config.StateDir) {
		return nil, ErrService
	}
	if err := privateDirectory(filepath.Dir(config.Socket)); err != nil {
		return nil, err
	}
	if err := privateDirectory(config.StateDir); err != nil {
		return nil, err
	}
	probe, err := os.CreateTemp(config.StateDir, ".write-check-")
	if err != nil {
		return nil, ErrService
	}
	name := probe.Name()
	_ = probe.Close()
	_ = os.Remove(name)
	var operations *lifecycle.Manager
	profiles, err := profilestore.Open(config.StateDir, func(ctx context.Context, name string) error {
		if operations != nil {
			if err := operations.CheckInactive(ctx, name); err != nil {
				if errors.Is(err, lifecycle.ErrBusy) {
					return profilestore.ErrBusy
				}
				if errors.Is(err, lifecycle.ErrPersistence) {
					return err
				}
				return profilestore.ErrUnavailable
			}
		}
		return profileInactive(ctx, config.DockerSocket, name)
	})
	if err != nil {
		return nil, ErrService
	}
	journal, err := operationstore.Open(config.StateDir)
	if err != nil {
		return nil, ErrService
	}
	serviceCtx, cancel := context.WithCancel(context.Background())
	operations, err = lifecycle.Open(serviceCtx, journal, lifecycle.Options{Adapter: config.Adapter, WithProfile: profiles.WithProfile, OperationTimeout: config.OperationTimeout, MFATimeout: config.MFATimeout})
	if err != nil {
		cancel()
		_ = journal.Close()
		return nil, ErrService
	}
	listener, err := net.Listen("unix", config.Socket)
	if err != nil {
		cancel()
		_ = operations.Close()
		return nil, ErrService
	}
	if os.Chmod(config.Socket, 0600) != nil {
		_ = listener.Close()
		cancel()
		_ = operations.Close()
		return nil, ErrService
	}
	return &Server{operations: operations, cancel: cancel, profiles: profiles, config: config, listener: listener, connections: make(map[net.Conn]bool), slots: make(chan struct{}, 32)}, nil
}
func privateDirectory(path string) error {
	if err := os.MkdirAll(path, 0700); err != nil {
		return ErrService
	}
	info, err := os.Lstat(path)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm()&0077 != 0 {
		return ErrService
	}
	return nil
}
func (s *Server) Close() error {
	s.closeOnce.Do(func() {
		s.closeErr = s.listener.Close()
		s.mu.Lock()
		s.closed = true
		s.cancel()
		for conn := range s.connections {
			_ = conn.Close()
		}
		s.mu.Unlock()
		s.workers.Wait()
		if err := s.operations.Close(); err != nil {
			s.closeErr = err
		}
	})
	return s.closeErr
}

func (s *Server) Serve(ctx context.Context) error {
	defer s.Close()
	s.operations.StartReconcile()
	done := make(chan struct{})
	go func() {
		select {
		case <-ctx.Done():
			_ = s.Close()
		case <-done:
		}
	}()
	defer close(done)
	defer s.workers.Wait()
	for {
		conn, err := s.listener.Accept()
		if err != nil {
			if ctx.Err() != nil || errors.Is(err, net.ErrClosed) {
				return nil
			}
			return ErrService
		}
		select {
		case s.slots <- struct{}{}:
		default:
			_ = conn.Close()
			continue
		}
		s.mu.Lock()
		if s.closed {
			s.mu.Unlock()
			_ = conn.Close()
			<-s.slots
			continue
		}
		s.connections[conn] = true
		s.workers.Add(1)
		s.mu.Unlock()
		go func() {
			defer s.workers.Done()
			defer func() { <-s.slots }()
			defer conn.Close()
			defer func() { s.mu.Lock(); delete(s.connections, conn); s.mu.Unlock() }()
			connectionCtx, connectionCancel := context.WithCancel(ctx)
			defer connectionCancel()
			channel := rpcio.New(conn, true)
			mux := &dispatcher{config: s.config, profiles: s.profiles, operations: s.operations, connectionCtx: connectionCtx, channel: channel, subscriptions: map[string]*subscription{}}
			defer mux.closeSubscriptions()
			server := jrpc2.NewServer(mux, &jrpc2.ServerOptions{DisableBuiltin: true, Concurrency: 1, AllowPush: true, NewContext: func() context.Context { return ctx }})
			server.Start(channel)
			_ = server.Wait()
		}()
	}
}

type dispatcher struct {
	operations    *lifecycle.Manager
	connectionCtx context.Context
	channel       *rpcio.Channel
	subMu         sync.Mutex
	subscriptions map[string]*subscription
	profiles      *profilestore.Store
	config        Config
	session       protocol.Session
}

func (d *dispatcher) Assign(_ context.Context, _ string) jrpc2.Handler { return d.handle }
func fault(code protocol.ErrorCode) error {
	safe := protocol.NewRPCError(code)
	return &jrpc2.Error{Code: jrpc2.Code(safe.Code), Message: safe.Message}
}
func (d *dispatcher) handle(ctx context.Context, req *jrpc2.Request) (any, error) {
	guard := protocol.Request{JSONRPC: protocol.JSONRPCVersion, ID: json.RawMessage(req.ID()), Method: protocol.Method(req.Method())}
	if err := d.session.Check(guard); err != nil {
		return nil, fault(err.(*protocol.RPCError).Code)
	}
	raw := json.RawMessage(`{}`)
	_ = req.UnmarshalParams(&raw)
	switch guard.Method {
	case protocol.Hello:
		d.session.Reset()
		params, err := protocol.DecodeHello(raw)
		if err != nil {
			return nil, fault(protocol.CodeInvalidParams)
		}
		if err := d.session.Negotiate(params); err != nil {
			return nil, fault(protocol.CodeVersion)
		}
		return protocol.HelloResult{API: protocol.CurrentAPI(), ControllerVersion: d.config.Version, Backends: []profile.BackendType{profile.L2TP, profile.XAuth}}, nil
	case protocol.BackendList:
		if jsonstrict.Decode(raw, &struct{}{}, protocol.MaxMessageBytes) != nil {
			return nil, fault(protocol.CodeInvalidParams)
		}
		l2tp, _ := profile.DescribeBackend(profile.L2TP)
		xauth, _ := profile.DescribeBackend(profile.XAuth)
		return []profile.BackendDescriptor{l2tp, xauth}, nil
	case protocol.BackendDescribe:
		var params struct {
			Backend profile.BackendType `json:"backend"`
		}
		if jsonstrict.Decode(raw, &params, protocol.MaxMessageBytes) != nil {
			return nil, fault(protocol.CodeInvalidParams)
		}
		descriptor, err := profile.DescribeBackend(params.Backend)
		if err != nil {
			return nil, fault(protocol.CodeBackendUnavailable)
		}
		return descriptor, nil
	case protocol.ProfileCreate:
		params, err := protocol.DecodeProfileCreate(raw)
		if err != nil {
			return nil, fault(protocol.CodeInvalidParams)
		}
		result, err := d.profiles.Create(ctx, params.Profile, params.Credentials)
		return result, profileFault(err)
	case protocol.ProfileList:
		if jsonstrict.Decode(raw, &struct{}{}, profile.MaxProfileBytes) != nil {
			return nil, fault(protocol.CodeInvalidParams)
		}
		result, err := d.profiles.List(ctx)
		return result, profileFault(err)
	case protocol.ProfileInspect:
		params, err := protocol.DecodeProfileParams(raw)
		if err != nil {
			return nil, fault(protocol.CodeInvalidParams)
		}
		result, err := d.profiles.Inspect(ctx, params.Profile)
		return result, profileFault(err)
	case protocol.ProfileUpdate:
		params, err := protocol.DecodeProfileUpdate(raw)
		if err != nil {
			return nil, fault(protocol.CodeInvalidParams)
		}
		result, err := d.profiles.Update(ctx, params.Profile, params.ExpectedRevision)
		return result, profileFault(err)
	case protocol.CredentialRotate:
		params, err := protocol.DecodeCredentialRotate(raw)
		if err != nil {
			return nil, fault(protocol.CodeInvalidParams)
		}
		result, err := d.profiles.Rotate(ctx, params.Profile, params.ExpectedRevision, params.Credentials)
		return result, profileFault(err)
	case protocol.ProfileDelete:
		params, err := protocol.DecodeProfileChange(raw)
		if err != nil {
			return nil, fault(protocol.CodeInvalidParams)
		}
		result, err := d.profiles.DeleteWith(ctx, params.Profile, params.ExpectedRevision, func() error { return d.operations.Forget(params.Profile) })
		if errors.Is(err, lifecycle.ErrPersistence) {
			return nil, lifecycleFault(err)
		}
		return result, profileFault(err)
	case protocol.ConnectionEnsure, protocol.ConnectionDisconnect, protocol.ConnectionStatus, protocol.OperationStatus, protocol.OperationCancel, protocol.OperationSubscribe, protocol.OperationSubmitMFA:
		return d.lifecycle(ctx, req, raw, guard.Method)
	case protocol.SystemStatus:
		if jsonstrict.Decode(raw, &struct{}{}, protocol.MaxMessageBytes) != nil {
			return nil, fault(protocol.CodeInvalidParams)
		}
		return Status{Version: d.config.Version, API: protocol.CurrentAPI(), StateReady: d.operations.Ready(), RuntimeEnabled: d.config.Adapter != nil, DockerReady: dockerReady(ctx, d.config.DockerSocket)}, nil
	default:
		return nil, fault(protocol.CodeMethod)
	}
}

// Bridge validates bounded frames in both directions. Its lifetime is unrelated
// to service-owned work. A closed stdin half-closes the socket and drains replies.
func Bridge(ctx context.Context, socket string, input io.Reader, output io.Writer) error {
	conn, err := (&net.Dialer{Timeout: 5 * time.Second}).DialContext(ctx, "unix", socket)
	if err != nil {
		return ErrService
	}
	defer conn.Close()
	done := make(chan struct{})
	defer close(done)
	go func() {
		select {
		case <-ctx.Done():
			_ = conn.Close()
		case <-done:
		}
	}()
	sending := make(chan error, 1)
	go func() {
		err := copyFrames(input, conn)
		if err != nil {
			_ = conn.Close()
		} else if unix, ok := conn.(*net.UnixConn); ok {
			_ = unix.CloseWrite()
		}
		sending <- err
	}()
	if err := copyFrames(conn, output); err != nil {
		return ErrService
	}
	select {
	case err := <-sending:
		if err != nil {
			return ErrService
		}
	default:
	}
	return nil
}
func copyFrames(input io.Reader, output io.Writer) error {
	reader, writer := protocol.NewFrameReader(input), protocol.NewFrameWriter(output)
	for {
		data, err := reader.Read()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return err
		}
		if err := writer.Write(data); err != nil {
			return err
		}
	}
}

func profileFault(err error) error {
	switch {
	case err == nil:
		return nil
	case errors.Is(err, profilestore.ErrNotFound):
		return fault(protocol.CodeNotFound)
	case errors.Is(err, profilestore.ErrConflict):
		return fault(protocol.CodeConflict)
	case errors.Is(err, profilestore.ErrBusy):
		return fault(protocol.CodeProfileActive)
	case errors.Is(err, profilestore.ErrUnavailable):
		return fault(protocol.CodeBackendUnavailable)
	case errors.Is(err, lifecycle.ErrPersistence):
		return fault(protocol.CodePersistence)
	case errors.Is(err, profilestore.ErrUncertain):
		return fault(protocol.CodeInterrupted)
	default:
		return fault(protocol.CodeInternal)
	}
}
