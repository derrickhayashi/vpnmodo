package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRegistryAuthFileValidation(t *testing.T) {
	path := filepath.Join(t.TempDir(), "registry.json")
	for _, raw := range []string{
		`{"username":"synthetic-registry-user","token":""}`,
		`{"username":"synthetic-registry-user","token":null}`,
		`{"username":"synthetic-registry-user","token":"synthetic-registry-token","token":"duplicate"}`,
		`{"username":"synthetic-registry-user","token":"synthetic-registry-token","unexpected":true}`,
	} {
		if os.WriteFile(path, []byte(raw), 0600) != nil {
			t.Fatal("write fixture")
		}
		_, err := readRegistryFile(path)
		if err == nil || strings.Contains(err.Error(), "synthetic-") {
			t.Fatal("invalid registry input accepted or disclosed")
		}
	}
	if os.WriteFile(path, []byte(`{"username":"synthetic-registry-user","token":"synthetic-registry-token"}`), 0600) != nil {
		t.Fatal("write fixture")
	}
	if os.Chmod(path, 0644) != nil {
		t.Fatal("chmod fixture")
	}
	if _, err := readRegistryFile(path); err == nil {
		t.Fatal("read shared registry credentials")
	}
}
