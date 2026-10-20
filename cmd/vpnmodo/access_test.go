package main

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/derrickhayashi/vpn-sidecars-next/internal/host"
)

func TestAccessFailuresHaveStableJSONAndNoNetworkMutation(t *testing.T) {
	registry := filepath.Join(t.TempDir(), "private", "hosts.json")
	for _, args := range [][]string{{"doctor", "missing", "--json"}, {"up", "missing", "--json"}, {"doctor", "missing", "--unknown", "--json"}, {"service", "stop", "../bad", "--json"}} {
		var out bytes.Buffer
		command := append([]string{"--config", registry}, args...)
		if run(context.Background(), command, &out) == nil {
			t.Fatal("failure reported success")
		}
		var report accessReport
		if json.Unmarshal(out.Bytes(), &report) != nil || report.Type != "access" || report.Ready || report.Error == "" {
			t.Fatal("invalid failure envelope", out.String())
		}
	}
}
func TestServiceConfigureIsPrivateAndRequiresNoVMConnection(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	registry := filepath.Join(dir, "registry", "hosts.json")
	if err := host.Add(registry, host.Host{Name: host.ManagedName, SSH: "192.0.2.250"}); err != nil {
		t.Fatal(err)
	}
	if err := host.AddReference(ctx, host.ReferencePath(registry), host.Reference{Name: "example", Host: host.ManagedName, Profile: "example"}); err != nil {
		t.Fatal(err)
	}
	source := filepath.Join(dir, "services.json")
	os.WriteFile(source, []byte(`{"schema_version":1,"services":[{"name":"web","ssh_target":"private-jump","local_port":18080,"remote_host":"127.0.0.1","remote_port":8080}]}`), 0600)
	var out bytes.Buffer
	if err := run(ctx, []string{"--config", registry, "service", "configure", "example", "--file", source, "--json"}, &out); err != nil {
		t.Fatal(err)
	}
	var report accessReport
	if json.Unmarshal(out.Bytes(), &report) != nil || report.Outcome != "configured" || report.Ready {
		t.Fatal("configuration claimed runtime readiness")
	}
	info, err := os.Stat(filepath.Join(registry+".access", "example.json"))
	if err != nil || info.Mode().Perm() != 0600 {
		t.Fatal("private catalog", err)
	}
}
