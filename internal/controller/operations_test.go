package controller

import (
	"context"
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/derrickhayashi/vpn-sidecars-next/internal/operation"
	"github.com/derrickhayashi/vpn-sidecars-next/internal/profile"
	"github.com/derrickhayashi/vpn-sidecars-next/internal/protocol"
	"github.com/derrickhayashi/vpn-sidecars-next/internal/rpcio"
	"github.com/derrickhayashi/vpn-sidecars-next/internal/secret"
)

func rpcClient(t *testing.T, ctx context.Context, socket string) *rpcio.Client {
	t.Helper()
	conn, err := net.Dial("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	client, err := rpcio.Connect(ctx, conn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { client.Close() })
	return client
}
func rpcWait(t *testing.T, ctx context.Context, client *rpcio.Client, id string, state operation.State) protocol.Operation {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		var result protocol.Operation
		if err := client.Call(ctx, protocol.OperationStatus, protocol.OperationParams{OperationID: id}, &result); err != nil {
			t.Fatal(err)
		}
		if result.State == state {
			return result
		}
		time.Sleep(3 * time.Millisecond)
	}
	t.Fatal("RPC state not reached")
	return protocol.Operation{}
}
func createRPCProfile(t *testing.T, ctx context.Context, client *rpcio.Client, name string) {
	t.Helper()
	p := profile.Profile{SchemaVersion: 1, Name: name, Backend: profile.Backend{Type: profile.XAuth, XAuth: &profile.XAuthConfig{Server: "192.0.2.1"}}, Health: profile.TCPHealth{Host: "192.0.2.10", Port: 22}}
	c := profile.Credentials{Username: secret.New("synthetic-user"), Password: secret.New("synthetic-password"), PSK: secret.New("synthetic-shared")}
	payload, err := (protocol.ProfileCreateParams{Profile: p, Credentials: c}).EncodeForTransport()
	if err != nil {
		t.Fatal(err)
	}
	var result profile.Snapshot
	if err = client.Call(ctx, protocol.ProfileCreate, json.RawMessage(payload), &result); err != nil {
		t.Fatal(err)
	}
}
func TestOperationReconnectMFAAndNotificationOrdering(t *testing.T) {
	adapter, err := newFileAdapter(filepath.Join(t.TempDir(), "runtime"))
	if err != nil {
		t.Fatal(err)
	}
	config, ctx, _ := startTestServerWith(t, adapter)
	first := rpcClient(t, ctx, config.Socket)
	createRPCProfile(t, ctx, first, "pending-mfa")
	var op protocol.Operation
	if err = first.Call(ctx, protocol.ConnectionEnsure, protocol.ProfileParams{Profile: "pending-mfa"}, &op); err != nil {
		t.Fatal(err)
	}
	first.Close()
	second := rpcClient(t, ctx, config.Socket)
	pending := rpcWait(t, ctx, second, op.ID, operation.AwaitingMFA)
	var joined protocol.Operation
	if err = second.Call(ctx, protocol.ConnectionEnsure, protocol.ProfileParams{Profile: "pending-mfa"}, &joined); err != nil || joined.ID != pending.ID {
		t.Fatal("reconnect started duplicate login", err)
	}
	raw, err := net.Dial("unix", config.Socket)
	if err != nil {
		t.Fatal(err)
	}
	defer raw.Close()
	_ = raw.SetDeadline(time.Now().Add(3 * time.Second))
	reader, writer := protocol.NewFrameReader(raw), protocol.NewFrameWriter(raw)
	if err = writer.Write([]byte(`{"jsonrpc":"2.0","id":1,"method":"system.hello","params":{"api":{"major":1,"minor":0}}}`)); err != nil {
		t.Fatal(err)
	}
	if _, err = reader.Read(); err != nil {
		t.Fatal(err)
	}
	request, _ := json.Marshal(protocol.Request{JSONRPC: "2.0", ID: json.RawMessage(`2`), Method: protocol.OperationSubscribe, Params: json.RawMessage(`{"operation_id":"` + pending.ID + `"}`)})
	if err = writer.Write(request); err != nil {
		t.Fatal(err)
	}
	initial, err := reader.Read()
	if err != nil || !strings.Contains(string(initial), `"result"`) {
		t.Fatal("initial snapshot not first", err)
	}
	params := protocol.MFASubmitParams{OperationID: pending.ID, ChallengeID: pending.Challenge.ID, Code: secret.New("9876543210")}
	payload, err := params.EncodeForTransport()
	if err != nil {
		t.Fatal(err)
	}
	if err = second.Call(ctx, protocol.OperationSubmitMFA, json.RawMessage(payload), &joined); err != nil {
		t.Fatal(err)
	}
	notice, err := reader.Read()
	if err != nil || !strings.Contains(string(notice), "operation.changed") || strings.Contains(string(notice), "9876543210") {
		t.Fatal("invalid/unsafe notification", err)
	}
	if err = second.Call(ctx, protocol.OperationSubmitMFA, json.RawMessage(payload), &joined); err == nil {
		t.Fatal("MFA replay accepted")
	}
	rpcWait(t, ctx, second, pending.ID, operation.Succeeded)
	var connection operation.Connection
	if err = second.Call(ctx, protocol.ConnectionStatus, protocol.ProfileParams{Profile: "pending-mfa"}, &connection); err != nil || connection.Observed != operation.ObservedConnected {
		t.Fatal("connection status", err)
	}
}
func TestProductionBackendCannotBeEnabledByProfileInput(t *testing.T) {
	config, ctx, _ := startTestServer(t)
	client := rpcClient(t, ctx, config.Socket)
	createRPCProfile(t, ctx, client, "example")
	var op protocol.Operation
	if err := client.Call(ctx, protocol.ConnectionEnsure, protocol.ProfileParams{Profile: "example"}, &op); err == nil {
		t.Fatal("production skeleton initiated a VPN")
	}
	entries, err := os.ReadDir(filepath.Join(config.StateDir, "profiles"))
	if err != nil || len(entries) != 1 {
		t.Fatal("profile changed", err)
	}
}
