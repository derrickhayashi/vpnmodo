package access

import (
	"bytes"
	"context"
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/derrickhayashi/vpn-sidecars-next/internal/host"
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
	if _, err := Load(path); err != nil {
		t.Fatalf("ordinary input permissions rejected: %v", err)
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
	b, _ := json.Marshal([]state{{Alias: "example", Service: definition().Services[0], Directory: dir}})
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

func TestStopRejectsDescriptorBoundToAnotherProfileAlias(t *testing.T) {
	registry := filepath.Join(t.TempDir(), "hosts.json")
	ctx := context.Background()
	if err := Configure(ctx, registry, "example", definition()); err != nil {
		t.Fatal(err)
	}
	token, _ := privatefile.Token()
	dir := "/tmp/vpnmodo-" + strconv.Itoa(os.Geteuid()) + "-" + token
	if err := os.Mkdir(dir, 0700); err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)
	svc := definition().Services[0]
	descriptor := Descriptor{Alias: "other", Service: svc, Host: host.Host{Name: "vm", SSH: "192.0.2.1"}, Request: host.ProxyRequest{Profile: "other", Host: "192.0.2.10", Port: 22, ApplicationID: strings.Repeat("a", 64)}}
	data, _ := json.Marshal(descriptor)
	privatefile.Write(filepath.Join(dir, "proxy.json"), data)
	saveStates(registry, "example", []state{{Alias: "example", Service: svc, Directory: dir}})
	results, err := Stop(ctx, registry, "example", "")
	if err != nil || len(results) != 1 || results[0].Code != "stop_failed" {
		t.Fatal("foreign binding accepted", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "proxy.json")); err != nil {
		t.Fatal("other alias descriptor removed")
	}
	states, err := readStates(registry, "example")
	if err != nil || len(states) != 1 {
		t.Fatal("uncertain ownership discarded")
	}
}

func TestDoctorClassifiesAnUnownedOccupiedPortWithoutStoppingIt(t *testing.T) {
	ctx := context.Background()
	registry := filepath.Join(t.TempDir(), "hosts.json")
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	d := definition()
	d.Services[0].LocalPort = listener.Addr().(*net.TCPAddr).Port
	if err = Configure(ctx, registry, "example", d); err != nil {
		t.Fatal(err)
	}
	results, err := Status(ctx, registry, "example")
	if err != nil || len(results) != 1 || results[0].Code != "local_port_conflict" {
		t.Fatal("occupied port not diagnosed", results, err)
	}
	conn, err := net.Dial("tcp4", listener.Addr().String())
	if err != nil {
		t.Fatal("unowned listener was stopped")
	}
	conn.Close()
}

func TestSavedCatalogRemainsPrivate(t *testing.T) {
	registry := filepath.Join(t.TempDir(), "hosts.json")
	if err := Configure(context.Background(), registry, "example", definition()); err != nil {
		t.Fatal(err)
	}
	path := catalogPath(registry, "example")
	if _, err := Read(registry, "example"); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil || info.Mode().Perm() != 0600 {
		t.Fatal("saved catalog is not private")
	}
	if err := os.Chmod(path, 0644); err != nil {
		t.Fatal(err)
	}
	if _, err := Read(registry, "example"); err == nil {
		t.Fatal("nonprivate saved catalog accepted")
	}
}
