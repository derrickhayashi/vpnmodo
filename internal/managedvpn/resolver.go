package managedvpn

import (
	"bytes"
	"errors"
	"io"
	"os"
	"path/filepath"
	"syscall"

	"github.com/derrickhayashi/vpn-sidecars-next/internal/privatefile"
)

// This fixed, nonsecret file is readable by the workload UID through its bind
// mount. Its host ancestors remain private. It can never name an outside resolver.
const xauthResolver = "nameserver 127.0.0.1\noptions ndots:0\n"

func checkXAuthResolver(path string) error {
	f, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if errors.Is(err, os.ErrNotExist) {
		return os.ErrNotExist
	}
	if err != nil {
		return ErrRuntime
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() || !privatefile.Owned(info) || info.Mode().Perm() != 0444 {
		return ErrRuntime
	}
	data, err := io.ReadAll(io.LimitReader(f, 128))
	if err != nil || !bytes.Equal(data, []byte(xauthResolver)) {
		return ErrRuntime
	}
	return nil
}
func writeXAuthResolver(root string) error {
	path := filepath.Join(root, "xauth-resolv.conf")
	err := checkXAuthResolver(path)
	if err == nil {
		return nil
	}
	if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	f, err := os.CreateTemp(root, ".resolver-")
	if err != nil {
		return ErrRuntime
	}
	defer os.Remove(f.Name())
	_, err = f.WriteString(xauthResolver)
	if err == nil {
		err = f.Chmod(0444)
	}
	if err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err != nil || closeErr != nil || os.Rename(f.Name(), path) != nil {
		return ErrRuntime
	}
	return privatefile.SyncDir(root)
}
