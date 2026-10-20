package controller

import (
	"net"
	"os"
	"path/filepath"
	"testing"

	"github.com/derrickhayashi/vpnmodo/internal/operation"
	"github.com/derrickhayashi/vpnmodo/internal/operationstore"
	"github.com/derrickhayashi/vpnmodo/internal/protocol"
	"github.com/derrickhayashi/vpnmodo/internal/rpcio"
)

func TestUpgradeBarrierRefusesMutationsOverRPC(t *testing.T) {
	config, ctx, _ := startTestServer(t)
	marker := filepath.Join(filepath.Dir(config.StateDir), "upgrade-pending")
	if err := os.WriteFile(marker, []byte("synthetic"), 0600); err != nil {
		t.Fatal(err)
	}
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
	if err = client.Call(ctx, protocol.SystemStatus, struct{}{}, &status); err != nil {
		t.Fatal(err)
	}
	for _, method := range []protocol.Method{protocol.ProfileCreate, protocol.ConnectionEnsure, protocol.ConnectionDisconnect} {
		err = client.Call(ctx, method, struct{}{}, &status)
		if err == nil {
			t.Fatalf("mutation %s accepted during upgrade", method)
		}
	}
	os.Remove(marker)
	if upgradePending(config.StateDir) {
		t.Fatal("barrier retained")
	}
}
func TestUpgradeSnapshotRefusesUnknownAndActiveState(t *testing.T) {
	root := t.TempDir()
	os.Chmod(root, 0700)
	if CheckUpgrade(root) == nil {
		t.Fatal("accepted absent journal")
	}
	store, err := operationstore.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	state := operationstore.Empty()
	if err = store.Save(state); err != nil {
		t.Fatal(err)
	}
	store.Close()
	if err = CheckUpgrade(root); err != nil {
		t.Fatal("empty valid snapshot", err)
	}
	store, err = operationstore.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	state.Connections["example"] = operation.Connection{Profile: "example", Desired: operation.Connected, Observed: operation.Unknown, Generation: 1, Revision: 1}
	if err = store.Save(state); err != nil {
		t.Fatal(err)
	}
	store.Close()
	if err = CheckUpgrade(root); err == nil {
		t.Fatal("accepted unknown connection")
	}
}
