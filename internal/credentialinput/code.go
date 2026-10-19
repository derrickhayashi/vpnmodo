package credentialinput

import (
	"context"
	"io"
	"os"
	"strings"
	"syscall"

	"github.com/derrickhayashi/vpn-sidecars-next/internal/secret"
	"golang.org/x/term"
)

// ReadCode uses the controlling terminal with echo disabled or explicitly selected
// protected stdin. Cancellation restores the terminal and closes the reader.
func ReadCode(ctx context.Context, stdin bool) (secret.Value, error) {
	var file *os.File
	var state *term.State
	var err error
	if stdin {
		if term.IsTerminal(int(os.Stdin.Fd())) || !protected(os.Stdin) {
			return secret.Value{}, ErrInput
		}
		var fd int
		fd, err = syscall.Dup(int(os.Stdin.Fd()))
		if err == nil {
			syscall.CloseOnExec(fd)
			err = syscall.SetNonblock(fd, true)
			file = os.NewFile(uintptr(fd), "code-input")
		}
	} else {
		file, err = os.OpenFile("/dev/tty", os.O_RDWR|syscall.O_NONBLOCK, 0)
		if err == nil {
			state, err = term.MakeRaw(int(file.Fd()))
		}
	}
	if err != nil {
		if file != nil {
			file.Close()
		}
		return secret.Value{}, ErrInput
	}
	restore := func() {
		if state != nil {
			_ = term.Restore(int(file.Fd()), state)
		}
		_ = file.Close()
	}
	defer restore()
	if !stdin {
		code, e := term.NewTerminal(terminalIO{ctx: ctx, file: file, fd: int(file.Fd())}, "").ReadPassword("MFA code: ")
		return checkedCode(code, e)
	}
	type result struct {
		code string
		err  error
	}
	results := make(chan result, 1)
	go func() {
		data, e := io.ReadAll(io.LimitReader(file, 13))
		defer clear(data)
		results <- result{strings.TrimSpace(string(data)), e}
	}()
	select {
	case <-ctx.Done():
		return secret.Value{}, ErrInput
	case r := <-results:
		return checkedCode(r.code, r.err)
	}
}
func checkedCode(code string, err error) (secret.Value, error) {
	if err != nil || len(code) < 4 || len(code) > 10 || strings.Trim(code, "0123456789") != "" {
		return secret.Value{}, ErrInput
	}
	return secret.New(code), nil
}
