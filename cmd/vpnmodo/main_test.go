package main

import (
	"bytes"
	"context"
	"path/filepath"
	"strings"
	"testing"
)

func TestHostRegistrationHasNoProvisioningSideEffects(t *testing.T) {
	config := filepath.Join(t.TempDir(), "private", "hosts.json")
	var output bytes.Buffer
	if err := run(context.Background(), []string{"--config", config, "host", "add", "example", "--ssh", "vm.example.com", "--json"}, &output); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(output.String(), `"registered":true`) {
		t.Fatal("registration result")
	}
	output.Reset()
	if err := run(context.Background(), []string{"--config", config, "host", "list"}, &output); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(output.String(), `"ssh":"vm.example.com"`) {
		t.Fatal("host not persisted")
	}
	if err := run(context.Background(), []string{"--config", config, "host", "add", "example", "--ssh", "other.example.com"}, &output); err == nil {
		t.Fatal("duplicate registration")
	}
	if err := run(context.Background(), []string{"--config", config, "host", "setup", "example", "--image", "bad;synthetic-sensitive"}, &output); err == nil || strings.Contains(err.Error(), "synthetic-sensitive") {
		t.Fatal("unsafe image accepted/disclosed")
	}
}
