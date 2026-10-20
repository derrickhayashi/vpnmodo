package main

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	assets "github.com/derrickhayashi/vpn-sidecars-next"
	"github.com/derrickhayashi/vpn-sidecars-next/internal/host"
	"github.com/derrickhayashi/vpn-sidecars-next/internal/privatefile"
)

func fakeLima(t *testing.T) (registry, dir, log string) {
	t.Helper()
	root := t.TempDir()
	bin := filepath.Join(root, "bin")
	if err := os.Mkdir(bin, 0700); err != nil {
		t.Fatal(err)
	}
	registry = filepath.Join(root, "private", "hosts.json")
	dir = filepath.Join(filepath.Dir(registry), "setup", "vpn-vm")
	if err := privatefile.Dir(dir); err != nil {
		t.Fatal(err)
	}
	if _, err := privatefile.Write(filepath.Join(dir, "ghcr-config.json"), []byte(`{"auths":{"ghcr.io":{"auth":"synthetic-fixture"}}}`)); err != nil {
		t.Fatal(err)
	}
	receipt := host.InstalledRelease{SchemaVersion: 1, OK: true, Version: "synthetic-release", ControllerImage: "sha256:" + strings.Repeat("1", 64), BackendImages: map[string]string{}}
	for i, role := range []string{"l2tp", "xauth", "application"} {
		receipt.BackendImages[role] = "sha256:" + strings.Repeat(string(rune('2'+i)), 64)
	}
	data, _ := json.Marshal(receipt)
	source := filepath.Join(root, "receipt.json")
	if err := os.WriteFile(source, data, 0600); err != nil {
		t.Fatal(err)
	}
	log = filepath.Join(root, "commands.log")
	for name, script := range map[string]string{
		"limactl": `#!/bin/sh
set -eu
case "$1" in
  list) if [ -f "$LIMA_TEST_STATE" ]; then printf '{"name":"vpn-vm","sshConfigFile":"%s","status":"Running"}\n' "$LIMA_TEST_SSH_CONFIG"; fi ;;
  start) printf '%s\n' "$*" >> "$LIMA_TEST_LOG"; touch "$LIMA_TEST_STATE" "$LIMA_TEST_SSH_CONFIG"; [ "${LIMA_TEST_FAIL_START:-}" != yes ] ;;
  *) exit 1 ;;
esac
`,
		"ssh": "#!/bin/sh\ncat >/dev/null\n",
		"scp": "#!/bin/sh\nset -eu\ncp \"$LIMA_TEST_RECEIPT\" \"$4\"\n",
	} {
		if err := os.WriteFile(filepath.Join(bin, name), []byte(script), 0700); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("LIMA_TEST_STATE", filepath.Join(root, "running"))
	t.Setenv("LIMA_TEST_SSH_CONFIG", filepath.Join(root, "ssh.config"))
	t.Setenv("LIMA_TEST_RECEIPT", source)
	t.Setenv("LIMA_TEST_LOG", log)
	return registry, dir, log
}

func TestSetupProvisionAndRetry(t *testing.T) {
	registry, dir, log := fakeLima(t)
	var output bytes.Buffer
	first, receipt, err := provisionLima(context.Background(), registry, &output)
	if err != nil || receipt.Version != "synthetic-release" || !first.Sudo || first.SSHConfig != os.Getenv("LIMA_TEST_SSH_CONFIG") {
		t.Fatalf("provision failed: %v", err)
	}
	for name, expected := range map[string][]byte{"lima.yaml": assets.LimaYAML, "registry-release.json": assets.RegistryRelease, "install-vm.py": assets.VMInstaller} {
		path := filepath.Join(dir, name)
		data, err := privatefile.Read(path, 64*1024)
		if err != nil || !bytes.Equal(data, expected) {
			t.Fatalf("missing or changed embedded asset: %s", name)
		}
	}
	second, _, err := provisionLima(context.Background(), registry, &output)
	if err != nil || second != first {
		t.Fatalf("retry did not reuse host: %v", err)
	}
	registered, err := host.Read(registry)
	if err != nil || len(registered.Hosts) != 1 || registered.Hosts[0] != first {
		t.Fatal("registration was not idempotent")
	}
	commands, _ := os.ReadFile(log)
	if strings.Count(string(commands), "--name") != 1 || strings.Count(string(commands), "start --tty=false") != 2 {
		t.Fatal("retry tried to recreate the VM")
	}
	if strings.Contains(output.String(), "synthetic-fixture") {
		t.Fatal("registry credentials appeared in output")
	}
}

func TestSetupRefusesUnrelatedVM(t *testing.T) {
	registry, _, log := fakeLima(t)
	if err := os.WriteFile(os.Getenv("LIMA_TEST_STATE"), nil, 0600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := provisionLima(context.Background(), registry, &bytes.Buffer{}); err == nil {
		t.Fatal("adopted unrelated Lima instance")
	}
	if _, err := os.Stat(log); !os.IsNotExist(err) {
		t.Fatal("started unrelated VM")
	}
}

func TestSetupRefusesConflictingHost(t *testing.T) {
	registry, _, log := fakeLima(t)
	if err := host.Add(registry, host.Host{Name: "vpn-vm", SSH: "other.example.com"}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := provisionLima(context.Background(), registry, &bytes.Buffer{}); err == nil {
		t.Fatal("overwrote existing host")
	}
	if _, err := os.Stat(log); !os.IsNotExist(err) {
		t.Fatal("created VM before checking host conflict")
	}
}

func TestSetupRejectsInvalidReceiptAndCredentials(t *testing.T) {
	registry, dir, _ := fakeLima(t)
	if err := os.WriteFile(os.Getenv("LIMA_TEST_RECEIPT"), []byte(`{"ok":true}`), 0600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := provisionLima(context.Background(), registry, &bytes.Buffer{}); err == nil {
		t.Fatal("accepted invalid installation receipt")
	}
	registered, _ := host.Read(registry)
	if len(registered.Hosts) != 0 {
		t.Fatal("registered host after failed installation")
	}
	path := filepath.Join(dir, "ghcr-config.json")
	if err := os.Chmod(path, 0644); err != nil {
		t.Fatal(err)
	}
	if err := ensureRegistryAuth(context.Background(), path, &bytes.Buffer{}); err == nil {
		t.Fatal("accepted exposed credential file")
	}
}

func TestSetupHelpAndInvalidOptions(t *testing.T) {
	var output bytes.Buffer
	if err := run(context.Background(), []string{"setup", "--help"}, &output); err != nil || !strings.Contains(output.String(), "Usage: vpnmodo setup") {
		t.Fatal("setup help unavailable")
	}
	for _, args := range [][]string{{"setup", "unexpected"}, {"setup", "--unknown"}} {
		if err := run(context.Background(), args, &output); err == nil {
			t.Fatal("accepted invalid setup arguments")
		}
	}
}

func TestSetupRepairsRunningInstanceAfterBootFailure(t *testing.T) {
	registry, dir, _ := fakeLima(t)
	if _, _, err := provisionLima(context.Background(), registry, &bytes.Buffer{}); err != nil {
		t.Fatal(err)
	}
	t.Setenv("LIMA_TEST_FAIL_START", "yes")
	if _, _, err := provisionLima(context.Background(), registry, &bytes.Buffer{}); err != nil {
		t.Fatalf("did not repair setup-owned running instance: %v", err)
	}
	if err := os.Remove(filepath.Join(dir, "lima.yaml")); err != nil {
		t.Fatal(err)
	}
	if _, _, err := provisionLima(context.Background(), registry, &bytes.Buffer{}); err == nil {
		t.Fatal("attempted repair of an unrelated instance")
	}
}
