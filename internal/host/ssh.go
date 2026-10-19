package host

import (
	"bytes"
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"io"
	"os/exec"
	"regexp"
	"strings"
	"sync"

	"github.com/derrickhayashi/vpn-sidecars-next/internal/jsonstrict"
	"github.com/derrickhayashi/vpn-sidecars-next/internal/protocol"
	"github.com/derrickhayashi/vpn-sidecars-next/internal/rpcio"
)

const bridgeCommand = "docker --host unix:///var/run/docker.sock exec -i vpnmodo-controller /usr/local/bin/vpnmodo-controller bridge"

var ErrSSH = errors.New("SSH transport failed; check host keys, authentication and Docker access")

//go:embed bootstrap.py
var bootstrap string

func SSHArgs(h Host, command string) ([]string, error) {
	if h.Validate() != nil {
		return nil, ErrRegistry
	}
	args := []string{"-T", "-o", "BatchMode=yes", "-o", "StrictHostKeyChecking=yes", "-o", "ForwardAgent=no", "-o", "PermitLocalCommand=no", "-o", "RemoteCommand=none", "-o", "ClearAllForwardings=yes", "-o", "ConnectTimeout=10", "-o", "ServerAliveInterval=15", "-o", "ServerAliveCountMax=3"}
	if h.SSHConfig != "" {
		args = append(args, "-F", h.SSHConfig)
	}
	return append(args, "--", h.SSH, command), nil
}

type pipe struct {
	read   io.ReadCloser
	write  io.WriteCloser
	cancel context.CancelFunc
	cmd    *exec.Cmd
	once   sync.Once
}

func (p *pipe) Read(b []byte) (int, error)  { return p.read.Read(b) }
func (p *pipe) Write(b []byte) (int, error) { return p.write.Write(b) }
func (p *pipe) Close() error {
	p.once.Do(func() { _ = p.write.Close(); _ = p.read.Close(); p.cancel(); _ = p.cmd.Wait() })
	return nil
}
func Connect(ctx context.Context, h Host) (*rpcio.Client, error) {
	args, err := SSHArgs(h, bridgeCommand)
	if err != nil {
		return nil, err
	}
	child, cancel := context.WithCancel(ctx)
	cmd := exec.CommandContext(child, "ssh", args...)
	cmd.Stderr = io.Discard
	input, err := cmd.StdinPipe()
	if err != nil {
		cancel()
		return nil, ErrSSH
	}
	output, err := cmd.StdoutPipe()
	if err != nil {
		cancel()
		_ = input.Close()
		return nil, ErrSSH
	}
	if cmd.Start() != nil {
		cancel()
		_ = input.Close()
		_ = output.Close()
		return nil, ErrSSH
	}
	stream := &pipe{read: output, write: input, cancel: cancel, cmd: cmd}
	client, err := rpcio.Connect(ctx, stream)
	if err != nil {
		_ = stream.Close()
		var safe *protocol.RPCError
		if errors.As(err, &safe) {
			return nil, safe
		}
		return nil, ErrSSH
	}
	return client, nil
}

type SetupParams struct {
	Image   string `json:"image"`
	Replace bool   `json:"replace"`
}
type SetupResult struct {
	OK     bool   `json:"ok"`
	Action string `json:"action,omitempty"`
	Error  string `json:"error,omitempty"`
}

var imagePattern = regexp.MustCompile(`^[a-z0-9][a-z0-9._/:@-]{0,254}$`)

func Setup(ctx context.Context, h Host, params SetupParams) (SetupResult, error) {
	if !imagePattern.MatchString(params.Image) {
		return SetupResult{}, errors.New("supply a valid controller image reference")
	}
	// Only the embedded program enters the remote shell command. All operator
	// values are JSON stdin and subprocess arguments inside that fixed program.
	command := "python3 -c '" + strings.ReplaceAll(bootstrap, "'", "'\"'\"'") + "'"
	args, err := SSHArgs(h, command)
	if err != nil {
		return SetupResult{}, err
	}
	payload, _ := json.Marshal(params)
	cmd := exec.CommandContext(ctx, "ssh", args...)
	cmd.Stdin = bytes.NewReader(payload)
	cmd.Stderr = io.Discard
	var buffer limitedBuffer
	cmd.Stdout = &buffer
	if cmd.Run() != nil {
		return SetupResult{}, ErrSSH
	}
	var result SetupResult
	if jsonstrict.Decode(buffer.Bytes(), &result, 4096) != nil {
		return SetupResult{}, ErrSSH
	}
	if !result.OK {
		return result, errors.New("controller setup failed; " + safeSetupError(result.Error))
	}
	return result, nil
}

// Never return arbitrary remote output or errors to terminal/log callers.
func safeSetupError(code string) string {
	switch code {
	case "prerequisites":
		return "Linux Docker Engine, Compose and Python 3 are required"
	case "image":
		return "controller image unavailable; check host registry access"
	case "ownership":
		return "controller name belongs to another deployment"
	case "replace_required":
		return "controller differs; use explicit --replace to upgrade or repair"
	case "storage":
		return "private controller storage is unavailable"
	default:
		return "inspect the host prerequisites and retry explicitly"
	}
}

type limitedBuffer struct{ bytes.Buffer }

func (b *limitedBuffer) Write(p []byte) (int, error) {
	if b.Len()+len(p) > 4096 {
		return 0, ErrSSH
	}
	return b.Buffer.Write(p)
}
