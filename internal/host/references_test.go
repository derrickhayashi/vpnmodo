package host

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"testing"
)

func TestReferencesConcurrentRegistrationAndRemoval(t *testing.T) {
	path := filepath.Join(t.TempDir(), "private", "profiles.json")
	var group sync.WaitGroup
	for _, name := range []string{"one", "two", "three"} {
		group.Go(func() {
			if err := AddReference(context.Background(), path, Reference{Name: name, Host: "example-vm", Profile: "example-profile"}); err != nil {
				t.Error(err)
			}
		})
	}
	group.Wait()
	refs, err := References(path)
	if err != nil || len(refs) != 3 {
		t.Fatal("registration lost", err)
	}
	if err := AddReference(context.Background(), path, refs[0]); err != ErrDuplicate {
		t.Fatal("alias overwritten")
	}
	if err := RemoveReference(context.Background(), path, "two"); err != nil {
		t.Fatal(err)
	}
	refs, err = References(path)
	if err != nil || len(refs) != 2 {
		t.Fatal("remove", err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var shape map[string]json.RawMessage
	if json.Unmarshal(data, &shape) != nil || len(shape) != 2 {
		t.Fatal("registry shape")
	}
	if err := os.Chmod(path, 0644); err != nil {
		t.Fatal(err)
	}
	if _, err := References(path); err == nil {
		t.Fatal("public registry accepted")
	}
}
