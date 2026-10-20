package profilestore

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/derrickhayashi/vpnmodo/internal/privatefile"
	"github.com/derrickhayashi/vpnmodo/internal/profile"
	"github.com/derrickhayashi/vpnmodo/internal/secret"
)

var testContext = context.Background()

func fixture(t *testing.T) profile.Profile {
	t.Helper()
	data, err := os.ReadFile("../../examples/profiles/v1/example-l2tp.json")
	if err != nil {
		t.Fatal(err)
	}
	p, err := profile.DecodeProfile(data)
	if err != nil {
		t.Fatal(err)
	}
	return p
}
func credentials() profile.Credentials {
	return profile.Credentials{Username: secret.New("synthetic-user"), Password: secret.New("synthetic-password"), PSK: secret.New("synthetic-shared")}
}
func newStore(t *testing.T) *Store {
	t.Helper()
	s, err := Open(filepath.Join(t.TempDir(), "state"), func(context.Context, string) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	return s
}
func assertSafe(t *testing.T, value any) {
	t.Helper()
	data, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{"synthetic-user", "synthetic-password", "synthetic-shared"} {
		if bytes.Contains(data, []byte(secret)) {
			t.Fatal("credential escaped")
		}
	}
}
func TestLifecycleRevisionsAndSecretSeparation(t *testing.T) {
	s := newStore(t)
	p := fixture(t)
	created, err := s.Create(testContext, p, credentials())
	if err != nil || created.Revision != 1 || !created.CredentialsPresent {
		t.Fatal("create", err)
	}
	assertSafe(t, created)
	if _, err = s.Create(testContext, p, credentials()); !errors.Is(err, ErrConflict) {
		t.Fatal("duplicate", err)
	}
	records, err := s.records()
	if err != nil {
		t.Fatal(err)
	}
	recordData, err := os.ReadFile(s.path(p.Name))
	if err != nil {
		t.Fatal(err)
	}
	assertSafe(t, json.RawMessage(recordData))
	oldRef := records[p.Name].CredentialRef
	p.Health.Port = 443
	updated, err := s.Update(testContext, p, 1)
	if err != nil || updated.Revision != 2 {
		t.Fatal("update", err)
	}
	if _, err = s.Update(testContext, p, 1); !errors.Is(err, ErrConflict) {
		t.Fatal("stale revision accepted")
	}
	rotated, err := s.Rotate(testContext, p.Name, 2, credentials())
	if err != nil || rotated.Revision != 3 {
		t.Fatal("rotate", err)
	}
	if _, err = os.Stat(s.credentialPath(oldRef)); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("old credentials retained")
	}
	reopened, err := Open(s.root, s.guard)
	if err != nil {
		t.Fatal(err)
	}
	got, err := reopened.Inspect(testContext, p.Name)
	if err != nil || got.Revision != 3 || !got.CredentialsPresent {
		t.Fatal("restart", err)
	}
	all, err := s.List(testContext)
	if err != nil || len(all) != 1 {
		t.Fatal("list", err)
	}
	assertSafe(t, all)
	deleted, err := s.Delete(testContext, p.Name, 3)
	if err != nil || !deleted.CredentialsDisposed {
		t.Fatal("delete", err)
	}
	if _, err = s.Inspect(testContext, p.Name); !errors.Is(err, ErrNotFound) {
		t.Fatal("deleted profile visible")
	}
	files, _ := os.ReadDir(s.vault())
	if len(files) != 0 {
		t.Fatal("credentials remain after deletion")
	}
}
func TestWriteFailuresHaveRecoverableOutcomes(t *testing.T) {
	for _, published := range []bool{false, true} {
		t.Run(map[bool]string{false: "before-commit", true: "after-commit"}[published], func(t *testing.T) {
			s := newStore(t)
			p := fixture(t)
			s.write = func(path string, data []byte) (bool, error) {
				if filepath.Dir(path) == s.profiles() {
					if published {
						ok, err := privatefile.Write(path, data)
						if err != nil {
							return ok, err
						}
						return true, ErrUncertain
					}
					return false, ErrStorage
				}
				return privatefile.Write(path, data)
			}
			if _, err := s.Create(testContext, p, credentials()); err == nil {
				t.Fatal("injected failure ignored")
			}
			reopened, err := Open(s.root, s.guard)
			if err != nil {
				t.Fatal(err)
			}
			got, err := reopened.Inspect(testContext, p.Name)
			if published {
				if err != nil || !got.CredentialsPresent {
					t.Fatal("committed profile lost credentials", err)
				}
			} else {
				if !errors.Is(err, ErrNotFound) {
					t.Fatal("partial profile usable")
				}
				entries, _ := os.ReadDir(s.vault())
				if len(entries) != 0 {
					t.Fatal("orphan recovery failed")
				}
			}
		})
	}
}
func TestFailedRotationKeepsOldCredentialsAndCleansStaging(t *testing.T) {
	s := newStore(t)
	p := fixture(t)
	if _, err := s.Create(testContext, p, credentials()); err != nil {
		t.Fatal(err)
	}
	s.write = func(path string, data []byte) (bool, error) {
		if filepath.Dir(path) == s.profiles() {
			return false, ErrStorage
		}
		return privatefile.Write(path, data)
	}
	if _, err := s.Rotate(testContext, p.Name, 1, credentials()); err == nil {
		t.Fatal("rotation failure lost")
	}
	s.write = privatefile.Write
	got, err := s.Inspect(testContext, p.Name)
	if err != nil || got.Revision != 1 || !got.CredentialsPresent {
		t.Fatal("old record damaged", err)
	}
	files, _ := os.ReadDir(s.vault())
	if len(files) != 1 {
		t.Fatal("orphan secret retained")
	}
}
func TestIndependentStoresSerializeMutations(t *testing.T) {
	s := newStore(t)
	p := fixture(t)
	if _, err := s.Create(testContext, p, credentials()); err != nil {
		t.Fatal(err)
	}
	other, err := Open(s.root, s.guard)
	if err != nil {
		t.Fatal(err)
	}
	var group sync.WaitGroup
	results := make(chan error, 2)
	for _, store := range []*Store{s, other} {
		group.Go(func() { _, err := store.Update(testContext, p, 1); results <- err })
	}
	group.Wait()
	close(results)
	successes, conflicts := 0, 0
	for err := range results {
		if err == nil {
			successes++
		} else if errors.Is(err, ErrConflict) {
			conflicts++
		} else {
			t.Fatal(err)
		}
	}
	if successes != 1 || conflicts != 1 {
		t.Fatal("lost update")
	}
}
func TestInactiveCheckAndUnsafeStorage(t *testing.T) {
	s := newStore(t)
	p := fixture(t)
	if _, err := s.Create(testContext, p, credentials()); err != nil {
		t.Fatal(err)
	}
	for _, blocked := range []error{ErrBusy, ErrUnavailable} {
		s.guard = func(context.Context, string) error { return blocked }
		if _, err := s.Update(testContext, p, 1); !errors.Is(err, blocked) {
			t.Fatal("active check bypassed")
		}
		if _, err := s.Rotate(testContext, p.Name, 1, credentials()); !errors.Is(err, blocked) {
			t.Fatal("rotate bypassed")
		}
		if _, err := s.Delete(testContext, p.Name, 1); !errors.Is(err, blocked) {
			t.Fatal("delete bypassed")
		}
	}
	if err := os.Chmod(s.credentialPathFromTest(t, p.Name), 0644); err != nil {
		t.Fatal(err)
	}
	got, err := s.Inspect(testContext, p.Name)
	if err != nil || got.CredentialsPresent {
		t.Fatal("unsafe credentials reported available", err)
	}
	if err := os.Chmod(s.profiles(), 0755); err != nil {
		t.Fatal(err)
	}
	if _, err := s.List(testContext); err == nil {
		t.Fatal("public directory accepted")
	}
}
func (s *Store) credentialPathFromTest(t *testing.T, name string) string {
	t.Helper()
	r, err := s.records()
	if err != nil {
		t.Fatal(err)
	}
	return s.credentialPath(r[name].CredentialRef)
}
func TestUnsafeNamesSymlinksAndCorruptRecords(t *testing.T) {
	s := newStore(t)
	for _, name := range []string{"../outside", "Bad", "x/y"} {
		if _, err := s.Inspect(testContext, name); err == nil {
			t.Fatal("unsafe name")
		}
	}
	target := filepath.Join(t.TempDir(), "target")
	if err := os.WriteFile(target, []byte("synthetic-sensitive"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, s.path("example")); err != nil {
		t.Fatal(err)
	}
	if _, err := s.List(testContext); err == nil || strings.Contains(err.Error(), "synthetic-sensitive") {
		t.Fatal("symlink accepted/disclosed")
	}
	if err := os.Remove(s.path("example")); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(s.path("example"), []byte(`{"schema_version":1,"secret":"synthetic-sensitive"}`), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := Open(s.root, s.guard); err == nil {
		t.Fatal("corruption ignored")
	}
}
