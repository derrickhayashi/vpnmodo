package access

import (
	"bytes"
	"context"
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"testing"

	"github.com/derrickhayashi/vpn-sidecars-next/internal/privatefile"
)

func definition() Definition {
	return Definition{1, []Service{{Name: "dashboard", SSHTarget: "private-jump", LocalPort: 18080, RemoteHost: "127.0.0.1", RemotePort: 8080}}}
}
func TestCatalogRejectsAmbiguityAndUnsafeEndpoints(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "services.json")
	good, _ := json.Marshal(definition())
	for _, b := range [][]byte{
		[]byte(`{"schema_version":1,"schema_version":1,"services":[]}`),
		[]byte(`{"schema_version":1,"Services":[]}`),
		[]byte(`{"schema_version":1,"services":null}`),
		[]byte(`{"schema_version":1,"services":[],"password":"synthetic"}`),
	} {
		os.WriteFile(path, b, 0600)
		if _, err := Load(path); err == nil {
			t.Fatal("ambiguous definition accepted")
		}
	}
	os.WriteFile(path, good, 0600)
	if _, err := Load(path); err != nil {
		t.Fatal(err)
	}
	os.Chmod(path, 0644)
	if _, err := Load(path); err == nil {
		t.Fatal("public definition accepted")
	}
	for _, host := range []string{"-oProxyCommand=bad", "host;bad", "::1", "[::1]", "host\nother", "$(bad)", "host:22"} {
		d := definition()
		d.Services[0].RemoteHost = host
		if d.Validate() == nil {
			t.Fatal("unsafe endpoint accepted")
		}
	}
	d := definition()
	d.Services = append(d.Services, d.Services[0])
	if d.Validate() == nil {
		t.Fatal("duplicate port/name accepted")
	}
	link := filepath.Join(root, "link")
	os.Symlink(path, link)
	if _, err := Load(link); err == nil {
		t.Fatal("symlink accepted")
	}
}
func TestConfigureCannotLoseOwnedSessionState(t *testing.T) {
	registry := filepath.Join(t.TempDir(), "hosts.json")
	ctx := context.Background()
	if err := Configure(ctx, registry, "example", definition()); err != nil {
		t.Fatal(err)
	}
	if err := privatefile.Dir(registry + ".access"); err != nil {
		t.Fatal(err)
	}
	token, _ := privatefile.Token()
	dir := "/tmp/vpnmodo-" + strconv.Itoa(os.Geteuid()) + "-" + token
	if err := os.Mkdir(dir, 0700); err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)
	b, _ := json.Marshal([]state{{Service: definition().Services[0], Directory: dir}})
	privatefile.Write(statePath(registry, "example"), b)
	if Configure(ctx, registry, "example", definition()) == nil {
		t.Fatal("live/stale ownership discarded")
	}
	if _, err := Read(registry, "../escape"); err == nil {
		t.Fatal("unsafe alias accepted")
	}
}
func TestDescriptorAndCleanupNeverFollowForeignPaths(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "proxy.json")
	os.WriteFile(p, []byte(`{}`), 0600)
	if _, err := ReadDescriptor(p); err == nil {
		t.Fatal("foreign descriptor accepted")
	}
	if cleanup(state{Directory: dir}) == nil {
		t.Fatal("foreign directory removed")
	}
	if _, err := os.Stat(p); err != nil {
		t.Fatal("foreign data changed")
	}
}

func TestStopPreservesOtherAliasesAndUnrelatedListeners(t *testing.T) {
	registry := filepath.Join(t.TempDir(), "hosts.json")
	ctx := context.Background()
	if err := Configure(ctx, registry, "other", definition()); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(catalogPath(registry, "other"))
	if err != nil {
		t.Fatal(err)
	}
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	if _, err = Stop(ctx, registry, "example", ""); err != nil {
		t.Fatal(err)
	}
	after, err := os.ReadFile(catalogPath(registry, "other"))
	if err != nil || !bytes.Equal(before, after) {
		t.Fatal("other alias changed")
	}
	conn, err := net.Dial("tcp4", listener.Addr().String())
	if err != nil {
		t.Fatal("unrelated listener stopped")
	}
	conn.Close()
}
