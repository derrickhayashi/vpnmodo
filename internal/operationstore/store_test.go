package operationstore

import (
	"os"
	"path/filepath"
	"testing"
)

func TestPrivateJournalRejectsUnsafeAndCorruptFiles(t *testing.T) {
	root := filepath.Join(t.TempDir(), "state")
	store, err := Open(root)
	if err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(filepath.Join(root, "operations.db"))
	if err != nil || info.Mode().Perm() != 0600 {
		t.Fatal("unsafe journal permissions")
	}
	if err = store.Close(); err != nil {
		t.Fatal(err)
	}
	if err = os.Chmod(filepath.Join(root, "operations.db"), 0644); err != nil {
		t.Fatal(err)
	}
	if opened, err := Open(root); err == nil {
		opened.Close()
		t.Fatal("public journal accepted")
	}
	if err = os.Remove(filepath.Join(root, "operations.db")); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(t.TempDir(), "target")
	if err = os.WriteFile(target, []byte("synthetic-sensitive"), 0600); err != nil {
		t.Fatal(err)
	}
	if err = os.Symlink(target, filepath.Join(root, "operations.db")); err != nil {
		t.Fatal(err)
	}
	if opened, err := Open(root); err == nil {
		opened.Close()
		t.Fatal("symlink accepted")
	}
	if err = os.Remove(filepath.Join(root, "operations.db")); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(root, "operations.db"), nil, 0600); err != nil {
		t.Fatal(err)
	}
	if opened, err := Open(root); err == nil {
		opened.Close()
		t.Fatal("truncated journal silently reset")
	}
}
