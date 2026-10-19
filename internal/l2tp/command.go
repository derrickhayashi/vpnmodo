// Package l2tp manages only explicitly labeled vpnmodo Compose deployments.
package l2tp

import (
	"bytes"
	"context"
	"errors"
	"os/exec"
	"syscall"
	"time"
)

var ErrRuntime = errors.New("managed L2TP runtime unavailable")
var ErrOwnership = errors.New("managed L2TP ownership conflict")

type commandError struct{ code int }

func (e commandError) Error() string { return "Docker command failed" }

type Runner interface {
	Run(context.Context, ...string) ([]byte, error)
}
type CLI struct{ Binary, Socket string }
type limitedBuffer struct{ bytes.Buffer }

func (b *limitedBuffer) Write(p []byte) (int, error) {
	if b.Len()+len(p) > 2*1024*1024 {
		return 0, ErrRuntime
	}
	return b.Buffer.Write(p)
}
func (r CLI) Run(parent context.Context, args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(parent, 150*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, r.Binary, append([]string{"--host", "unix://" + r.Socket}, args...)...)
	cmd.Env = []string{"PATH=/usr/local/bin:/usr/bin:/bin", "HOME=/run/vpnmodo", "DOCKER_CONFIG=/run/vpnmodo/docker", "COMPOSE_DISABLE_ENV_FILE=1", "COMPOSE_ANSI=never", "DOCKER_CLI_HINTS=false"}
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error { return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) }
	cmd.WaitDelay = 2 * time.Second
	var output limitedBuffer
	cmd.Stdout = &output
	if err := cmd.Run(); err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		var exit *exec.ExitError
		if errors.As(err, &exit) {
			return nil, commandError{exit.ExitCode()}
		}
		return nil, ErrRuntime
	}
	return output.Bytes(), nil
}
