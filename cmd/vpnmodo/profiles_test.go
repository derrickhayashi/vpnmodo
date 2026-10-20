package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestProfileCommandRejectsSecretsAndInvalidSettingsBeforeSSH(t *testing.T) {
	root := t.TempDir()
	registry := filepath.Join(root, "private", "hosts.json")
	file := filepath.Join(root, "profile.json")
	if err := os.WriteFile(file, []byte(`{"schema_version":1,"name":"example","password":"synthetic-sensitive"}`), 0600); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{
		{"profile", "create", "example", "--file", file},
		{"profile", "create", "example", "--password", "synthetic-sensitive"},
		{"profile", "rotate-credentials", "example", "--revision", "0"},
		{"profile", "create", "../bad"},
		{"profile", "inspect", "example", "--credentials-stdin"},
	} {
		var out bytes.Buffer
		err := run(context.Background(), append([]string{"--config", registry}, args...), &out)
		if err == nil || strings.Contains(err.Error(), "synthetic-sensitive") || out.Len() != 0 {
			t.Fatal("invalid request accepted/disclosed")
		}
	}
}
