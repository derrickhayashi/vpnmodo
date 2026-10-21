// Package profilestore commits non-secret profile records separately from the
// protected VM credential vault. RPC callers never receive vault references.
package profilestore

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/derrickhayashi/vpnmodo/internal/jsonstrict"
	"github.com/derrickhayashi/vpnmodo/internal/privatefile"
	"github.com/derrickhayashi/vpnmodo/internal/profile"
)

var (
	ErrNotFound    = errors.New("profile not found")
	ErrConflict    = errors.New("profile exists or revision conflicts")
	ErrBusy        = errors.New("profile is active; disconnect before changing it")
	ErrUnavailable = errors.New("cannot verify profile inactivity")
	ErrStorage     = privatefile.ErrStorage
	ErrUncertain   = privatefile.ErrUncertain
	tokenPattern   = regexp.MustCompile(`^[0-9a-f]{32}$`)
)

const maxProfiles = 128

type Snapshot = profile.Snapshot
type DeleteResult struct {
	Name                string `json:"name"`
	Deleted             bool   `json:"deleted"`
	CredentialsDisposed bool   `json:"credentials_disposed"`
}
type record struct {
	SchemaVersion int             `json:"schema_version"`
	Profile       profile.Profile `json:"profile"`
	Revision      uint64          `json:"revision"`
	CredentialRef string          `json:"credential_ref"`
}

// Guard must verify inactivity, not infer it from absent in-memory operations.
// Future lifecycle workers must coordinate through this same transaction lock.
type Guard func(context.Context, string) error
type Store struct {
	root  string
	guard Guard
	write func(string, []byte) (bool, error)
}

func Open(root string, guard Guard) (*Store, error) {
	s := &Store{root: root, guard: guard, write: privatefile.Write}
	if err := s.layout(); err != nil {
		return nil, err
	}
	if err := s.transaction(context.Background(), func(map[string]record) error { return nil }); err != nil {
		return nil, err
	}
	return s, nil
}
func (s *Store) layout() error {
	for _, p := range []string{s.root, s.profiles(), s.vault()} {
		if privatefile.Dir(p) != nil {
			return ErrStorage
		}
	}
	return privatefile.SyncDir(s.root)
}
func (s *Store) profiles() string                 { return filepath.Join(s.root, "profiles") }
func (s *Store) vault() string                    { return filepath.Join(s.root, "credentials") }
func (s *Store) path(name string) string          { return filepath.Join(s.profiles(), name+".json") }
func (s *Store) credentialPath(ref string) string { return filepath.Join(s.vault(), ref+".json") }
func (s *Store) records() (map[string]record, error) {
	entries, err := os.ReadDir(s.profiles())
	if err != nil || len(entries) > maxProfiles*2 {
		return nil, ErrStorage
	}
	records := map[string]record{}
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), ".tmp-") {
			continue
		}
		name := strings.TrimSuffix(entry.Name(), ".json")
		if !profile.ValidName(name) || entry.Name() != name+".json" {
			return nil, ErrStorage
		}
		data, err := privatefile.Read(s.path(name), 2*profile.MaxProfileBytes)
		if err != nil {
			return nil, ErrStorage
		}
		var r record
		if jsonstrict.Decode(data, &r, 2*profile.MaxProfileBytes) != nil || r.SchemaVersion != 1 || r.Profile.Name != name || r.Profile.Validate() != nil || r.Revision == 0 || !tokenPattern.MatchString(r.CredentialRef) {
			return nil, ErrStorage
		}
		records[name] = r
	}
	if len(records) > maxProfiles {
		return nil, ErrStorage
	}
	return records, nil
}
func (s *Store) cleanup(records map[string]record) error {
	// Publish current directory contents durably before disposing old credentials.
	if privatefile.SyncDir(s.profiles()) != nil {
		return ErrUncertain
	}
	live := map[string]bool{}
	for _, r := range records {
		live[r.CredentialRef+".json"] = true
	}
	for _, dir := range []string{s.profiles(), s.vault()} {
		entries, err := os.ReadDir(dir)
		if err != nil {
			return ErrStorage
		}
		for _, entry := range entries {
			name := entry.Name()
			temporary := strings.HasPrefix(name, ".tmp-")
			orphan := dir == s.vault() && !live[name] && strings.HasSuffix(name, ".json") && tokenPattern.MatchString(strings.TrimSuffix(name, ".json"))
			if temporary || orphan {
				if _, err := privatefile.Read(filepath.Join(dir, name), 2*profile.MaxProfileBytes); err != nil {
					return ErrStorage
				}
				if os.Remove(filepath.Join(dir, name)) != nil {
					return ErrStorage
				}
			}
		}
		if privatefile.SyncDir(dir) != nil {
			return ErrUncertain
		}
	}
	return nil
}
func (s *Store) transaction(ctx context.Context, fn func(map[string]record) error) error {
	if s.layout() != nil {
		return ErrStorage
	}
	unlock, err := privatefile.Lock(ctx, filepath.Join(s.root, ".profiles.lock"))
	if err != nil {
		return err
	}
	defer unlock()
	records, err := s.records()
	if err != nil {
		return err
	}
	if err = s.cleanup(records); err != nil {
		return err
	}
	return fn(records)
}
func (s *Store) snapshot(r record) Snapshot {
	data, err := privatefile.Read(s.credentialPath(r.CredentialRef), profile.MaxProfileBytes)
	var credentials profile.Credentials
	present := err == nil && jsonstrict.Decode(data, &credentials, profile.MaxProfileBytes) == nil && credentials.ValidateFor(r.Profile.Backend.Type) == nil
	return Snapshot{Profile: r.Profile, Revision: r.Revision, CredentialsPresent: present}
}
func (s *Store) List(ctx context.Context) ([]profile.Summary, error) {
	result := []profile.Summary{}
	err := s.transaction(ctx, func(records map[string]record) error {
		for _, r := range records {
			snapshot := s.snapshot(r)
			result = append(result, profile.Summary{Name: r.Profile.Name, Backend: r.Profile.Backend.Type, Revision: r.Revision, CredentialsPresent: snapshot.CredentialsPresent})
		}
		sort.Slice(result, func(i, j int) bool { return result[i].Name < result[j].Name })
		return nil
	})
	return result, err
}
func (s *Store) Inspect(ctx context.Context, name string) (Snapshot, error) {
	if !profile.ValidName(name) {
		return Snapshot{}, profile.ErrProfile
	}
	var result Snapshot
	err := s.transaction(ctx, func(records map[string]record) error {
		r, ok := records[name]
		if !ok {
			return ErrNotFound
		}
		result = s.snapshot(r)
		return nil
	})
	return result, err
}
func (s *Store) inactive(ctx context.Context, name string) error {
	if s.guard == nil {
		return ErrUnavailable
	}
	return s.guard(ctx, name)
}
func (s *Store) Create(ctx context.Context, p profile.Profile, c profile.Credentials) (Snapshot, error) {
	if p.Validate() != nil {
		return Snapshot{}, profile.ErrProfile
	}
	if c.ValidateFor(p.Backend.Type) != nil {
		return Snapshot{}, profile.ErrCredentials
	}
	var result Snapshot
	err := s.transaction(ctx, func(records map[string]record) error {
		if _, ok := records[p.Name]; ok {
			return ErrConflict
		}
		if len(records) >= maxProfiles {
			return ErrStorage
		}
		if err := s.inactive(ctx, p.Name); err != nil {
			return err
		}
		ref, err := s.saveCredentials(c)
		if err != nil {
			return err
		}
		r := record{SchemaVersion: 1, Profile: p, Revision: 1, CredentialRef: ref}
		if err = s.commit(r); err != nil {
			return err
		}
		result = s.snapshot(r)
		if !result.CredentialsPresent {
			return ErrUncertain
		}
		return nil
	})
	return result, err
}
func (s *Store) saveCredentials(c profile.Credentials) (string, error) {
	ref, err := privatefile.Token()
	if err != nil {
		return "", ErrStorage
	}
	data, err := c.EncodeForStorage()
	if err != nil {
		return "", profile.ErrCredentials
	}
	if _, err = s.write(s.credentialPath(ref), data); err != nil {
		return "", err
	}
	return ref, nil
}
func (s *Store) commit(r record) error {
	data, err := json.Marshal(r)
	if err != nil {
		return ErrStorage
	}
	_, err = s.write(s.path(r.Profile.Name), data)
	return err
}
func (s *Store) Update(ctx context.Context, p profile.Profile, expected uint64) (Snapshot, error) {
	if p.Validate() != nil {
		return Snapshot{}, profile.ErrProfile
	}
	return s.change(ctx, p.Name, expected, func(r *record) error {
		if r.Profile.Backend.Type != p.Backend.Type {
			return ErrConflict
		}
		r.Profile = p
		return nil
	})
}
func (s *Store) Rotate(ctx context.Context, name string, expected uint64, c profile.Credentials) (Snapshot, error) {
	if c.Validate() != nil {
		return Snapshot{}, profile.ErrCredentials
	}
	return s.change(ctx, name, expected, func(r *record) error {
		if c.ValidateFor(r.Profile.Backend.Type) != nil {
			return profile.ErrCredentials
		}
		ref, err := s.saveCredentials(c)
		if err != nil {
			return err
		}
		r.CredentialRef = ref
		return nil
	})
}
func (s *Store) change(ctx context.Context, name string, expected uint64, edit func(*record) error) (Snapshot, error) {
	if !profile.ValidName(name) || expected == 0 {
		return Snapshot{}, profile.ErrProfile
	}
	var result Snapshot
	err := s.transaction(ctx, func(records map[string]record) error {
		r, ok := records[name]
		if !ok {
			return ErrNotFound
		}
		if r.Revision != expected || r.Revision == math.MaxUint64 {
			return ErrConflict
		}
		if err := s.inactive(ctx, name); err != nil {
			return err
		}
		if err := edit(&r); err != nil {
			return err
		}
		r.Revision++
		if err := s.commit(r); err != nil {
			return err
		}
		records[name] = r
		if err := s.cleanup(records); err != nil {
			return ErrUncertain
		}
		result = s.snapshot(r)
		return nil
	})
	return result, err
}
func (s *Store) Delete(ctx context.Context, name string, expected uint64) (DeleteResult, error) {
	return s.DeleteWith(ctx, name, expected, nil)
}

// DeleteWith runs beforeRemove under the profile transaction after inactivity and
// revision checks. Lifecycle metadata is dropped before deleting the profile;
// a subsequent filesystem error leaves a valid inactive profile for inspection.
func (s *Store) DeleteWith(ctx context.Context, name string, expected uint64, beforeRemove func() error) (DeleteResult, error) {
	if !profile.ValidName(name) || expected == 0 {
		return DeleteResult{}, profile.ErrProfile
	}
	var result DeleteResult
	err := s.transaction(ctx, func(records map[string]record) error {
		r, ok := records[name]
		if !ok {
			return ErrNotFound
		}
		if r.Revision != expected {
			return ErrConflict
		}
		if err := s.inactive(ctx, name); err != nil {
			return err
		}
		if beforeRemove != nil {
			if err := beforeRemove(); err != nil {
				return err
			}
		}
		if os.Remove(s.path(name)) != nil {
			return ErrStorage
		}
		delete(records, name)
		if s.cleanup(records) != nil {
			return ErrUncertain
		}
		result = DeleteResult{Name: name, Deleted: true, CredentialsDisposed: true}
		return nil
	})
	return result, err
}

// WithProfile serializes lifecycle submission with profile edits. fn must never
// re-enter the profile store; it may commit lifecycle intent under its own lock.
func (s *Store) WithProfile(ctx context.Context, name string, fn func(Snapshot) error) error {
	if !profile.ValidName(name) {
		return profile.ErrProfile
	}
	return s.transaction(ctx, func(records map[string]record) error {
		r, ok := records[name]
		if !ok {
			return ErrNotFound
		}
		return fn(s.snapshot(r))
	})
}

// WithCredentials is an internal provisioning boundary, never an RPC read-back.
// fn must not retain credentials or reenter the store. Runtime writers must use
// protected files; no external commands run while the profile transaction is held.
func (s *Store) WithCredentials(ctx context.Context, name string, fn func(Snapshot, profile.Credentials) error) error {
	if !profile.ValidName(name) {
		return profile.ErrProfile
	}
	return s.transaction(ctx, func(records map[string]record) error {
		r, ok := records[name]
		if !ok {
			return ErrNotFound
		}
		data, err := privatefile.Read(s.credentialPath(r.CredentialRef), profile.MaxProfileBytes)
		var c profile.Credentials
		if err != nil || jsonstrict.Decode(data, &c, profile.MaxProfileBytes) != nil || c.ValidateFor(r.Profile.Backend.Type) != nil {
			return profile.ErrCredentials
		}
		return fn(Snapshot{Profile: r.Profile, Revision: r.Revision, CredentialsPresent: true}, c)
	})
}
