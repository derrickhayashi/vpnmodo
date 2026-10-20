package controller

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/derrickhayashi/vpn-sidecars-next/internal/eventlog"
	"github.com/derrickhayashi/vpn-sidecars-next/internal/protocol"
)

func TestCorruptLogsDoNotPreventControllerStartup(t *testing.T) {
	root, err := os.MkdirTemp("/tmp", "vpnmodo-log-failure-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(root)
	state := filepath.Join(root, "state")
	logs := filepath.Join(state, "logs")
	os.MkdirAll(logs, 0700)
	path := filepath.Join(logs, "events.jsonl")
	os.WriteFile(path, []byte("invalid-complete-record\n"), 0600)
	server, err := Listen(Config{Socket: filepath.Join(root, "run", "controller.sock"), StateDir: state, Version: "test"})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- server.Serve(ctx) }()
	client := rpcClient(t, ctx, server.config.Socket)
	var status Status
	if err = client.Call(ctx, protocol.SystemStatus, struct{}{}, &status); err != nil || !status.StateReady || status.LoggingReady {
		t.Fatal(status, err)
	}
	var result eventlog.Result
	if err = client.Call(ctx, protocol.ConnectionLogs, eventlog.Query{Profile: "example"}, &result); err == nil {
		t.Fatal("unreadable logs claimed success")
	}
	b, _ := os.ReadFile(path)
	if string(b) != "invalid-complete-record\n" {
		t.Fatal("corrupt log reset")
	}
	client.Close()
	cancel()
	select {
	case err = <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("controller shutdown blocked")
	}
}
