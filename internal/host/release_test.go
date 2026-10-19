package host

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestInstalledReleaseContainsCompleteLocalImageSet(t *testing.T) {
	image := "sha256:" + strings.Repeat("a", 64)
	r := InstalledRelease{SchemaVersion: 1, OK: true, Version: "v0.1.0-rc.1+git.aaaaaaaaaaaa", ControllerImage: image, BackendImages: map[string]string{"l2tp": image, "xauth": image, "application": image}}
	path := filepath.Join(t.TempDir(), "installed.json")
	write := func() {
		b, _ := json.Marshal(r)
		if err := os.WriteFile(path, b, 0600); err != nil {
			t.Fatal(err)
		}
	}
	write()
	if _, err := ReadInstalledRelease(path); err != nil {
		t.Fatal(err)
	}
	r.BackendImages["foreign"] = image
	write()
	if _, err := ReadInstalledRelease(path); err == nil {
		t.Fatal("extra role accepted")
	}
	delete(r.BackendImages, "foreign")
	r.BackendImages["xauth"] = "bad;synthetic-sensitive"
	write()
	if _, err := ReadInstalledRelease(path); err == nil || strings.Contains(err.Error(), "synthetic-sensitive") {
		t.Fatal("unsafe image accepted or disclosed")
	}
}
