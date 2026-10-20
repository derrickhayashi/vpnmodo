package operationstore

import (
	"github.com/derrickhayashi/vpnmodo/internal/operation"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"
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

func TestRecoveryCloneAndRoundTripPreserveBudget(t *testing.T) {
	s := Empty()
	s.Connections["example"] = operation.Connection{Profile: "example", Generation: 1, Revision: 1, Desired: operation.Connected, Observed: operation.Unknown, RecoveryEnabled: true, Recovery: &operation.Recovery{Attempts: 2, NotBefore: time.Now().UTC().Add(time.Hour), Health: operation.HealthDetached}}
	copy := s.Clone()
	copy.Connections["example"].Recovery.Attempts = 3
	if s.Connections["example"].Recovery.Attempts != 2 {
		t.Fatal("clone aliases mutable recovery state")
	}
	store, err := Open(filepath.Join(t.TempDir(), "state"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if err = store.Save(s); err != nil {
		t.Fatal(err)
	}
	loaded, err := store.Load()
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(s, loaded) {
		t.Fatal("budget or cooldown lost")
	}
}
