package controller

import (
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"github.com/derrickhayashi/vpn-sidecars-next/internal/eventlog"
	"github.com/derrickhayashi/vpn-sidecars-next/internal/operation"
	"github.com/derrickhayashi/vpn-sidecars-next/internal/protocol"
)

func TestLogRetrievalOverAuthenticatedRPC(t *testing.T) {
	adapter, err := newFileAdapter(filepath.Join(t.TempDir(), "runtime"))
	if err != nil {
		t.Fatal(err)
	}
	config, ctx, _ := startTestServerWith(t, adapter)
	client := rpcClient(t, ctx, config.Socket)
	createRPCProfile(t, ctx, client, "example")
	var op protocol.Operation
	if err = client.Call(ctx, protocol.ConnectionDisconnect, protocol.ProfileParams{Profile: "example"}, &op); err != nil {
		t.Fatal(err)
	}
	rpcWait(t, ctx, client, op.ID, operation.Succeeded)
	var result eventlog.Result
	if err = client.Call(ctx, protocol.ConnectionLogs, eventlog.Query{Profile: "example", Limit: 2}, &result); err != nil || len(result.Events) != 2 || result.NextBefore == 0 {
		t.Fatal(result, err)
	}
	before := result.NextBefore
	if err = client.Call(ctx, protocol.ConnectionLogs, eventlog.Query{Profile: "example", Limit: 2, Before: before}, &result); err != nil || len(result.Events) == 0 || result.Events[0].Sequence >= before {
		t.Fatal(result, err)
	}
	b, _ := json.Marshal(result)
	for _, value := range []string{"synthetic-password", "synthetic-shared", "192.0.2.1"} {
		if strings.Contains(string(b), value) {
			t.Fatal("sensitive result")
		}
	}
	for _, q := range []any{eventlog.Query{Profile: "../escape"}, eventlog.Query{Profile: "example", Limit: 501}, map[string]any{"profile": "example", "unknown": "ignored?"}} {
		if err = client.Call(ctx, protocol.ConnectionLogs, q, &result); err == nil {
			t.Fatal("invalid query accepted")
		}
	}
	var status Status
	if err = client.Call(ctx, protocol.SystemStatus, struct{}{}, &status); err != nil || !status.LoggingReady {
		t.Fatal(status, err)
	}
}
