package controller

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/derrickhayashi/vpnmodo/internal/lifecycle"
	"github.com/derrickhayashi/vpnmodo/internal/profile"
	"github.com/derrickhayashi/vpnmodo/internal/protocol"
	"github.com/derrickhayashi/vpnmodo/internal/rpcio"
)

func startTestServer(t *testing.T) (Config, context.Context, context.CancelFunc) {
	return startTestServerWith(t, nil)
}
func startTestServerWith(t *testing.T, adapter lifecycle.Adapter) (Config, context.Context, context.CancelFunc) {
	t.Helper()
	root, err := os.MkdirTemp("/tmp", "vpnmodo-test-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(root) })
	config := Config{Socket: filepath.Join(root, "run", "controller.sock"), StateDir: filepath.Join(root, "state"), Version: "test", Adapter: adapter}
	dockerListener, err := net.Listen("unix", filepath.Join(root, "docker.sock"))
	if err != nil {
		t.Fatal(err)
	}
	fake := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/containers/json" && r.Method == "GET" {
			_, _ = io.WriteString(w, "[]")
			return
		}
		if r.URL.Path != "/_ping" || r.Method != "GET" {
			t.Error("unexpected Docker mutation")
			w.WriteHeader(400)
			return
		}
		_, _ = io.WriteString(w, "OK")
	})}
	go fake.Serve(dockerListener)
	t.Cleanup(func() { _ = fake.Close() })
	config.DockerSocket = filepath.Join(root, "docker.sock")
	server, err := Listen(config)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	finished := make(chan error, 1)
	go func() { finished <- server.Serve(ctx) }()
	t.Cleanup(func() {
		cancel()
		select {
		case err := <-finished:
			if err != nil {
				t.Error(err)
			}
		case <-time.After(2 * time.Second):
			t.Error("service did not shut down")
		}
	})
	return config, ctx, cancel
}
func TestDiscoveryAndStatusOverPrivateSocket(t *testing.T) {
	config, ctx, _ := startTestServer(t)
	conn, err := net.Dial("unix", config.Socket)
	if err != nil {
		t.Fatal(err)
	}
	client, err := rpcio.Connect(ctx, conn)
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	var status Status
	if err := client.Call(ctx, protocol.SystemStatus, struct{}{}, &status); err != nil {
		t.Fatal(err)
	}
	if !status.StateReady || !status.DockerReady || status.RuntimeEnabled || status.Version != "test" {
		t.Fatal("misleading status")
	}
	var backends []profile.BackendDescriptor
	if err := client.Call(ctx, protocol.BackendList, struct{}{}, &backends); err != nil || len(backends) != 2 {
		t.Fatal("backend discovery", err)
	}
	var descriptor profile.BackendDescriptor
	if err := client.Call(ctx, protocol.BackendDescribe, struct {
		Backend string `json:"backend"`
	}{"ipsec-xauth"}, &descriptor); err != nil || !descriptor.Capabilities.RequireFailClosed {
		t.Fatal("missing required capability", err)
	}
	if err := client.Call(ctx, protocol.ProfileCreate, struct{}{}, &status); err == nil {
		t.Fatal("skeleton accepts mutation")
	}
	mode, err := os.Stat(config.Socket)
	if err != nil || mode.Mode().Perm() != 0600 {
		t.Fatal("socket is not private")
	}
	if _, err := Listen(config); err == nil {
		t.Fatal("replaced an active socket")
	}
}
func TestHandshakeErrorsNotificationsAndRedaction(t *testing.T) {
	config, _, _ := startTestServer(t)
	conn, err := net.Dial("unix", config.Socket)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	reader, writer := protocol.NewFrameReader(conn), protocol.NewFrameWriter(conn)
	call := func(data string) map[string]json.RawMessage {
		t.Helper()
		if err := writer.Write([]byte(data)); err != nil {
			t.Fatal(err)
		}
		reply, err := reader.Read()
		if err != nil {
			t.Fatal(err)
		}
		if bytes.Contains(reply, []byte("synthetic-sensitive")) {
			t.Fatal("raw error disclosed")
		}
		var parsed map[string]json.RawMessage
		if json.Unmarshal(reply, &parsed) != nil {
			t.Fatal("invalid reply")
		}
		return parsed
	}
	if call(`{"jsonrpc":"2.0","id":1,"method":"system.status"}`)["error"] == nil {
		t.Fatal("handshake skipped")
	}
	if err := writer.Write([]byte(`{"jsonrpc":"2.0","method":"system.hello","params":{"api":{"major":1,"minor":0}}}`)); err != nil {
		t.Fatal(err)
	}
	hello := `{"jsonrpc":"2.0","id":2,"method":"system.hello","params":{"api":{"major":1,"minor":0}}}`
	if string(call(hello)["id"]) != "2" {
		t.Fatal("notification produced reply")
	}
	if call(`{"jsonrpc":"2.0","id":3,"method":"system.hello","params":{"api":"synthetic-sensitive"}}`)["error"] == nil {
		t.Fatal("invalid hello accepted")
	}
	if call(`{"jsonrpc":"2.0","id":4,"method":"system.status"}`)["error"] == nil {
		t.Fatal("malformed renegotiation left gate open")
	}
	if call(hello)["result"] == nil {
		t.Fatal("cannot renegotiate")
	}
	if call(`{"jsonrpc":"2.0","id":5,"method":"backend.list","extra":"synthetic-sensitive"}`)["error"] == nil {
		t.Fatal("unknown field accepted")
	}
	if call(`{"jsonrpc":"2.0","id":6,"method":"backend.list","params":{"extra":"synthetic-sensitive"}}`)["error"] == nil {
		t.Fatal("unknown parameter accepted")
	}
}
func TestBridgeEOFAndDisconnectDoNotStopService(t *testing.T) {
	config, ctx, _ := startTestServer(t)
	input := strings.NewReader("{\"jsonrpc\":\"2.0\",\"id\":1,\"method\":\"system.hello\",\"params\":{\"api\":{\"major\":1,\"minor\":0}}}\n")
	var output bytes.Buffer
	short, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	if err := Bridge(short, config.Socket, input, &output); err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(output.Bytes(), []byte(`"result"`)) {
		t.Fatal("stdin EOF discarded response")
	}
	conn, err := net.Dial("unix", config.Socket)
	if err != nil {
		t.Fatal(err)
	}
	client, err := rpcio.Connect(short, conn)
	if err != nil {
		t.Fatal("bridge disconnect stopped service", err)
	}
	_ = client.Close()
}
func TestRejectsUnsafeStorage(t *testing.T) {
	root, err := os.MkdirTemp("/tmp", "vpnmodo-test-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(root)
	public := filepath.Join(root, "public")
	if os.Mkdir(public, 0755) != nil {
		t.Fatal("mkdir")
	}
	if _, err := Listen(Config{Socket: filepath.Join(root, "run", "socket"), StateDir: public}); err == nil {
		t.Fatal("public storage accepted")
	}
	link := filepath.Join(root, "link")
	if os.Symlink(public, link) != nil {
		t.Fatal("symlink")
	}
	if _, err := Listen(Config{Socket: filepath.Join(root, "run", "socket"), StateDir: link}); err == nil {
		t.Fatal("symlink storage accepted")
	}
}
