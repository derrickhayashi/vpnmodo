// Package credentialinput accepts credentials through protected input, never flags.
package credentialinput

import (
	"context"
	"errors"
	"io"
	"os"
	"strconv"
	"syscall"

	"github.com/derrickhayashi/vpnmodo/internal/jsonstrict"
	"github.com/derrickhayashi/vpnmodo/internal/privatefile"
	"github.com/derrickhayashi/vpnmodo/internal/profile"
	"github.com/derrickhayashi/vpnmodo/internal/secret"
	"golang.org/x/term"
)

var ErrInput = errors.New("invalid credential input; use hidden prompts or JSON on stdin/a protected file descriptor")

func Decode(input io.Reader) (profile.Credentials, error) {
	data, err := io.ReadAll(io.LimitReader(input, profile.MaxProfileBytes+1))
	if err != nil {
		return profile.Credentials{}, ErrInput
	}
	defer clear(data)
	var c profile.Credentials
	if jsonstrict.Decode(data, &c, profile.MaxProfileBytes) != nil || c.Validate() != nil {
		return profile.Credentials{}, ErrInput
	}
	return c, nil
}

// Read uses JSON when explicitly selected; a terminal is never read with echo.
func Read(ctx context.Context, stdin bool, descriptor string) (profile.Credentials, error) {
	if stdin && descriptor != "" {
		return profile.Credentials{}, ErrInput
	}
	if stdin {
		if term.IsTerminal(int(os.Stdin.Fd())) {
			return profile.Credentials{}, ErrInput
		}
		if !protected(os.Stdin) {
			return profile.Credentials{}, ErrInput
		}
		return readDescriptor(ctx, int(os.Stdin.Fd()))
	}
	if descriptor != "" {
		fd, err := strconv.Atoi(descriptor)
		if err != nil || fd < 3 || fd > 1024 || term.IsTerminal(fd) {
			return profile.Credentials{}, ErrInput
		}
		return readDescriptor(ctx, fd)
	}
	return prompt(ctx)
}
func prompt(ctx context.Context) (profile.Credentials, error) {
	values, err := promptSecrets(ctx, []string{"Username", "Password", "Pre-shared key"})
	if err != nil {
		return profile.Credentials{}, err
	}
	c := profile.Credentials{Username: values[0], Password: values[1], PSK: values[2]}
	if c.Validate() != nil {
		return profile.Credentials{}, ErrInput
	}
	return c, nil
}

func promptSecrets(ctx context.Context, labels []string) ([]secret.Value, error) {
	terminal, err := os.OpenFile("/dev/tty", os.O_RDWR|syscall.O_NONBLOCK, 0)
	if err != nil {
		return nil, ErrInput
	}
	defer terminal.Close()
	fd := int(terminal.Fd())
	state, err := term.MakeRaw(fd)
	if err != nil {
		return nil, ErrInput
	}
	defer term.Restore(fd, state)
	reader := term.NewTerminal(terminalIO{ctx: ctx, file: terminal, fd: fd}, "")
	values := make([]secret.Value, len(labels))
	for i, label := range labels {
		if ctx.Err() != nil {
			return nil, ErrInput
		}
		value, err := reader.ReadPassword(label + ": ")
		if err != nil {
			return nil, ErrInput
		}
		values[i] = secret.New(value)
	}
	return values, nil
}

func protected(file *os.File) bool {
	info, err := file.Stat()
	if err != nil || !privatefile.Owned(info) {
		return false
	}
	return info.Mode().IsRegular() && info.Mode().Perm()&0077 == 0 || info.Mode()&os.ModeNamedPipe != 0
}

func readDescriptor(ctx context.Context, fd int) (profile.Credentials, error) {
	copyFD, err := syscall.Dup(fd)
	if err != nil {
		return profile.Credentials{}, ErrInput
	}
	syscall.CloseOnExec(copyFD)
	if syscall.SetNonblock(copyFD, true) != nil {
		syscall.Close(copyFD)
		return profile.Credentials{}, ErrInput
	}
	file := os.NewFile(uintptr(copyFD), "credential-input")
	defer file.Close()
	if !protected(file) {
		return profile.Credentials{}, ErrInput
	}
	type result struct {
		credentials profile.Credentials
		err         error
	}
	results := make(chan result, 1)
	go func() { c, err := Decode(file); results <- result{c, err} }()
	select {
	case <-ctx.Done():
		_ = file.Close()
		return profile.Credentials{}, ErrInput
	case r := <-results:
		return r.credentials, r.err
	}
}
