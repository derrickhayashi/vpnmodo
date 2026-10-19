package host

import (
	"context"
	_ "embed"
	"encoding/json"
	"io"
	"os/exec"
	"strings"
)

//go:embed proxy.py
var proxyProgram string

type ProxyRequest struct {
	ApplicationID string `json:"application_id"`
	Profile       string `json:"profile"`
	Host          string `json:"host"`
	Port          int    `json:"port"`
}

func Probe(ctx context.Context, h Host) error {
	args, err := SSHArgs(h, "true")
	if err != nil {
		return err
	}
	cmd := exec.CommandContext(ctx, "ssh", args...)
	cmd.Stderr = io.Discard
	cmd.Stdout = io.Discard
	if cmd.Run() != nil {
		return ErrSSH
	}
	return nil
}

// Proxy is used only by the internal OpenSSH ProxyCommand subprocess. Its stdin
// after the header and stdout are the private destination's SSH byte stream.
func Proxy(ctx context.Context, h Host, p ProxyRequest, input io.Reader, output io.Writer) error {
	command := "python3 -c '" + strings.ReplaceAll(proxyProgram, "'", "'\"'\"'") + "'"
	args, err := SSHArgs(h, command)
	if err != nil {
		return err
	}
	cmd := exec.CommandContext(ctx, "ssh", args...)
	cmd.Stdout = output
	cmd.Stderr = io.Discard
	pipe, err := cmd.StdinPipe()
	if err != nil {
		return ErrSSH
	}
	if cmd.Start() != nil {
		pipe.Close()
		return ErrSSH
	}
	header, _ := json.Marshal(p)
	header = append(header, '\n')
	if _, err = pipe.Write(header); err != nil {
		cmd.Process.Kill()
		cmd.Wait()
		return ErrSSH
	}
	go func() { defer pipe.Close(); _, _ = io.Copy(pipe, input) }()
	if cmd.Wait() != nil {
		return ErrSSH
	}
	return nil
}
