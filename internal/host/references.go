package host

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"

	"github.com/derrickhayashi/vpnmodo/internal/jsonstrict"
	"github.com/derrickhayashi/vpnmodo/internal/privatefile"
	"github.com/derrickhayashi/vpnmodo/internal/profile"
)

type Reference struct {
	Name    string `json:"name"`
	Host    string `json:"host"`
	Profile string `json:"profile"`
}
type references struct {
	SchemaVersion int         `json:"schema_version"`
	Profiles      []Reference `json:"profiles"`
}

func ReferencePath(hostRegistry string) string { return hostRegistry + ".profiles.json" }
func (r Reference) Validate() error {
	if !profile.ValidName(r.Name) || !profile.ValidName(r.Host) || !profile.ValidName(r.Profile) {
		return ErrRegistry
	}
	return nil
}
func References(path string) ([]Reference, error) {
	data, err := privatefile.Read(path, 64*1024)
	if errors.Is(err, os.ErrNotExist) {
		return []Reference{}, nil
	}
	if err != nil {
		return nil, ErrRegistry
	}
	var r references
	if jsonstrict.Decode(data, &r, 64*1024) != nil || r.SchemaVersion != 1 || r.Profiles == nil || len(r.Profiles) > 256 {
		return nil, ErrRegistry
	}
	seen := map[string]bool{}
	for _, ref := range r.Profiles {
		if ref.Validate() != nil || seen[ref.Name] {
			return nil, ErrRegistry
		}
		seen[ref.Name] = true
	}
	return r.Profiles, nil
}
func FindReference(path, name string) (Reference, error) {
	refs, err := References(path)
	if err != nil {
		return Reference{}, err
	}
	for _, r := range refs {
		if r.Name == name {
			return r, nil
		}
	}
	return Reference{}, errors.New("profile is not registered locally; use profile register")
}
func AddReference(ctx context.Context, path string, r Reference) error {
	if r.Validate() != nil {
		return ErrRegistry
	}
	return changeReferences(ctx, path, func(refs []Reference) ([]Reference, error) {
		for _, old := range refs {
			if old.Name == r.Name {
				return nil, ErrDuplicate
			}
		}
		if len(refs) >= 256 {
			return nil, ErrRegistry
		}
		return append(refs, r), nil
	})
}
func RemoveReference(ctx context.Context, path, name string) error {
	if !profile.ValidName(name) {
		return ErrRegistry
	}
	return changeReferences(ctx, path, func(refs []Reference) ([]Reference, error) {
		for i, r := range refs {
			if r.Name == name {
				return append(refs[:i], refs[i+1:]...), nil
			}
		}
		return refs, nil
	})
}
func changeReferences(ctx context.Context, path string, edit func([]Reference) ([]Reference, error)) error {
	if !filepath.IsAbs(path) || privatefile.Dir(filepath.Dir(path)) != nil {
		return ErrRegistry
	}
	unlock, err := privatefile.Lock(ctx, path+".lock")
	if err != nil {
		return ErrRegistry
	}
	defer unlock()
	refs, err := References(path)
	if err != nil {
		return err
	}
	refs, err = edit(refs)
	if err != nil {
		return err
	}
	data, err := json.Marshal(references{1, refs})
	if err != nil || len(data) > 64*1024 {
		return ErrRegistry
	}
	if _, err = privatefile.Write(path, data); err != nil {
		return ErrRegistry
	}
	return nil
}
