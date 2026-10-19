// Package privatefile provides bounded private storage for managed state.
package privatefile

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"path/filepath"
	"syscall"
	"time"
)

var ErrStorage = errors.New("private storage unavailable")

// ErrUncertain means a write was published but its durable outcome needs inspection.
var ErrUncertain = errors.New("storage outcome uncertain; inspect before retrying")

func Token() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", ErrStorage
	}
	return hex.EncodeToString(b[:]), nil
}
func Owned(info os.FileInfo) bool {
	st, ok := info.Sys().(*syscall.Stat_t)
	return ok && st.Uid == uint32(os.Geteuid())
}
func Dir(path string) error {
	if !filepath.IsAbs(path) {
		return ErrStorage
	}
	if os.MkdirAll(path, 0700) != nil {
		return ErrStorage
	}
	info, err := os.Lstat(path)
	if err != nil || !info.IsDir() || info.Mode().Perm()&0077 != 0 || !Owned(info) {
		return ErrStorage
	}
	return nil
}
func Read(path string, limit int) ([]byte, error) {
	f, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if errors.Is(err, os.ErrNotExist) {
		return nil, os.ErrNotExist
	}
	if err != nil {
		return nil, ErrStorage
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() || !Owned(info) || info.Mode().Perm()&0077 != 0 || info.Size() > int64(limit) {
		return nil, ErrStorage
	}
	data, err := io.ReadAll(io.LimitReader(f, int64(limit)+1))
	if err != nil || len(data) > limit {
		return nil, ErrStorage
	}
	return data, nil
}
func SyncDir(path string) error {
	f, err := os.Open(path)
	if err != nil {
		return ErrStorage
	}
	defer f.Close()
	if f.Sync() != nil {
		return ErrStorage
	}
	return nil
}

// Write atomically replaces a private regular file. visible is true after rename,
// even if directory sync fails; callers must retain any newly referenced secrets.
func Write(path string, data []byte) (visible bool, err error) {
	if _, err := os.Lstat(path); err == nil {
		if _, err = Read(path, 1024*1024); err != nil {
			return false, ErrStorage
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return false, ErrStorage
	}
	f, err := os.CreateTemp(filepath.Dir(path), ".tmp-")
	if err != nil {
		return false, ErrStorage
	}
	defer os.Remove(f.Name())
	_, err = f.Write(data)
	if err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err != nil || closeErr != nil {
		return false, ErrStorage
	}
	if os.Rename(f.Name(), path) != nil {
		return false, ErrStorage
	}
	if SyncDir(filepath.Dir(path)) != nil {
		return true, ErrUncertain
	}
	return true, nil
}
func Lock(ctx context.Context, path string) (func(), error) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR|syscall.O_NOFOLLOW, 0600)
	if err != nil {
		return nil, ErrStorage
	}
	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() || !Owned(info) || info.Mode().Perm()&0077 != 0 {
		f.Close()
		return nil, ErrStorage
	}
	for {
		err = syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
		if err == nil {
			break
		}
		if err != syscall.EWOULDBLOCK && err != syscall.EAGAIN {
			f.Close()
			return nil, ErrStorage
		}
		select {
		case <-ctx.Done():
			f.Close()
			return nil, ErrStorage
		case <-time.After(20 * time.Millisecond):
		}
	}
	if ctx.Err() != nil {
		f.Close()
		return nil, ErrStorage
	}
	return func() { _ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN); _ = f.Close() }, nil
}
