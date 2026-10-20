package privatefile

import (
	"os"
	"path/filepath"
	"syscall"
	"testing"
)

func TestInputPermissionsAndManagedStorage(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "input.json")
	if err := os.WriteFile(path, []byte("{}"), 0600); err != nil {
		t.Fatal(err)
	}
	for _, mode := range []os.FileMode{0600, 0644} {
		if err := os.Chmod(path, mode); err != nil {
			t.Fatal(err)
		}
		if _, err := ReadInput(path, 2); err != nil {
			t.Fatalf("input mode %o: %v", mode, err)
		}
		_, err := Read(path, 2)
		if (err != nil) != (mode == 0644) {
			t.Fatalf("managed mode %o: %v", mode, err)
		}
	}
	if _, err := ReadInput(path, 1); err == nil {
		t.Fatal("oversized input accepted")
	}
	link := filepath.Join(root, "link")
	if err := os.Symlink(path, link); err != nil {
		t.Fatal(err)
	}
	fifo := filepath.Join(root, "fifo")
	if err := syscall.Mkfifo(fifo, 0600); err != nil {
		t.Fatal(err)
	}
	for _, unsafe := range []string{link, fifo, root, filepath.Join(root, "missing")} {
		if _, err := ReadInput(unsafe, 1024); err == nil {
			t.Fatalf("unsafe input accepted: %s", unsafe)
		}
	}
	saved := filepath.Join(root, "saved.json")
	if _, err := Write(saved, []byte("{}")); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(saved)
	if err != nil || info.Mode().Perm() != 0600 {
		t.Fatal("managed file is not private")
	}
}
