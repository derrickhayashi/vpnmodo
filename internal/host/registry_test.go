package host

import (
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

func TestRegistryAtomicityAndConcurrentAdds(t *testing.T) {
	file := filepath.Join(t.TempDir(), "private", "hosts.json")
	var tasks sync.WaitGroup
	for _, name := range []string{"one", "two", "three", "four"} {
		tasks.Go(func() {
			if err := Add(file, Host{Name: name, SSH: "vm.example.com"}); err != nil {
				t.Error(err)
			}
		})
	}
	tasks.Wait()
	registry, err := Read(file)
	if err != nil || len(registry.Hosts) != 4 {
		t.Fatal("concurrent add lost data", err)
	}
	if Add(file, Host{Name: "one", SSH: "vm.example.com"}) != ErrDuplicate {
		t.Fatal("duplicate host accepted")
	}
	info, err := os.Stat(file)
	if err != nil || info.Mode().Perm() != 0600 {
		t.Fatal("unsafe file permissions")
	}
}
func TestRegistryAndSSHValidation(t *testing.T) {
	for _, target := range []string{"-oProxyCommand=id", "vm;id", "vm\nother", "user name@vm", "$(id)"} {
		if (Host{Name: "example", SSH: target}).Validate() == nil {
			t.Fatal("unsafe SSH target")
		}
	}
	args, err := SSHArgs(Host{Name: "example", SSH: "user@vm.example.com", SSHConfig: "/tmp/synthetic-config"}, bridgeCommand)
	if err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(args, " ")
	for _, required := range []string{"-T", "BatchMode=yes", "StrictHostKeyChecking=yes", "-- user@vm.example.com", "-F /tmp/synthetic-config"} {
		if !strings.Contains(joined, required) {
			t.Fatal("missing SSH protection", required)
		}
	}
	file := filepath.Join(t.TempDir(), "hosts.json")
	if os.WriteFile(file, []byte(`{"schema_version":1,"hosts":[],"extra":"synthetic-sensitive"}`), 0600) != nil {
		t.Fatal("write")
	}
	if _, err := Read(file); err == nil || strings.Contains(err.Error(), "synthetic-sensitive") {
		t.Fatal("unknown fields accepted/disclosed")
	}
	if os.Chmod(file, 0644) != nil {
		t.Fatal("chmod")
	}
	if _, err := Read(file); err == nil {
		t.Fatal("public registry accepted")
	}
	if safeSetupError("synthetic-sensitive") == "synthetic-sensitive" {
		t.Fatal("untrusted remote error echoed")
	}
}
