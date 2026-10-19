package credentialinput

import (
	"context"
	"io"
	"os"
	"syscall"

	"golang.org/x/sys/unix"
)

// /dev/tty opened with O_NONBLOCK can return EAGAIN immediately on Darwin.
// Poll explicitly so hidden prompts wait for input and remain cancellable.
// Only this reader owns the descriptor; restoration/close happen after Read.
type terminalIO struct {
	ctx  context.Context
	file *os.File
	fd   int
}

func (t terminalIO) Write(p []byte) (int, error) { return t.file.Write(p) }
func (t terminalIO) Read(p []byte) (int, error) {
	for {
		if err := t.ctx.Err(); err != nil {
			return 0, err
		}
		n, err := syscall.Read(t.fd, p)
		if err == syscall.EINTR {
			continue
		}
		if err != syscall.EAGAIN && err != syscall.EWOULDBLOCK {
			if n == 0 && err == nil {
				return 0, io.EOF
			}
			if n < 0 {
				n = 0
			}
			return n, err
		}
		_, err = unix.Poll([]unix.PollFd{{Fd: int32(t.fd), Events: unix.POLLIN}}, 100)
		if err != nil && err != syscall.EINTR {
			return 0, err
		}
	}
}
