package controller

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/derrickhayashi/vpn-sidecars-next/internal/profile"
	"github.com/derrickhayashi/vpn-sidecars-next/internal/profilestore"
	"github.com/derrickhayashi/vpn-sidecars-next/internal/protocol"
	"github.com/derrickhayashi/vpn-sidecars-next/internal/rpcio"
	"github.com/derrickhayashi/vpn-sidecars-next/internal/secret"
)

func TestProfileLifecycleOverRPC(t *testing.T) {
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
	data, err := os.ReadFile("../../examples/profiles/v1/example-xauth.json")
	if err != nil {
		t.Fatal(err)
	}
	p, err := profile.DecodeProfile(data)
	if err != nil {
		t.Fatal(err)
	}
	c := profile.Credentials{Username: secret.New("synthetic-user"), Password: secret.New("synthetic-password"), PSK: secret.New("synthetic-shared")}
	encoded, err := (protocol.ProfileCreateParams{Profile: p, Credentials: c}).EncodeForTransport()
	if err != nil {
		t.Fatal(err)
	}
	var snapshot profile.Snapshot
	if err = client.Call(ctx, protocol.ProfileCreate, json.RawMessage(encoded), &snapshot); err != nil || snapshot.Revision != 1 || !snapshot.CredentialsPresent {
		t.Fatal("create", err)
	}
	if err = client.Call(ctx, protocol.ProfileCreate, json.RawMessage(encoded), &snapshot); err == nil {
		t.Fatal("duplicate accepted")
	}
	p.Health.Port = 443
	if err = client.Call(ctx, protocol.ProfileUpdate, protocol.ProfileUpdateParams{Profile: p, ExpectedRevision: 1}, &snapshot); err != nil || snapshot.Revision != 2 {
		t.Fatal("update", err)
	}
	rotated, err := (protocol.CredentialRotateParams{Profile: p.Name, ExpectedRevision: 2, Credentials: c}).EncodeForTransport()
	if err != nil {
		t.Fatal(err)
	}
	if err = client.Call(ctx, protocol.CredentialRotate, json.RawMessage(rotated), &snapshot); err != nil || snapshot.Revision != 3 {
		t.Fatal("rotate", err)
	}
	var snapshots []profile.Summary
	if err = client.Call(ctx, protocol.ProfileList, struct{}{}, &snapshots); err != nil || len(snapshots) != 1 {
		t.Fatal("list", err)
	}
	wire, _ := json.Marshal(snapshots)
	if strings.Contains(string(wire), "synthetic-") || strings.Contains(string(wire), "credential_ref") {
		t.Fatal("sensitive data in result")
	}
	var deleted profilestore.DeleteResult
	if err = client.Call(ctx, protocol.ProfileDelete, protocol.ProfileChangeParams{Profile: p.Name, ExpectedRevision: 3}, &deleted); err != nil || !deleted.CredentialsDisposed {
		t.Fatal("delete", err)
	}
}
func TestDockerInactivityChecksOwnershipAndFailsClosed(t *testing.T) {
	root, err := os.MkdirTemp("/tmp", "vpnmodo-docker-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(root)
	socket := filepath.Join(root, "docker.sock")
	listener, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	type answer struct {
		body string
		code int
	}
	responses := make(chan answer, 8)
	fake := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "GET" || r.URL.Path != "/containers/json" || r.URL.Query().Get("all") != "1" {
			t.Error("unexpected Docker request")
		}
		var filters map[string][]string
		if json.Unmarshal([]byte(r.URL.Query().Get("filters")), &filters) != nil || len(filters["label"]) != 3 {
			t.Error("missing ownership filters")
		}
		reply := <-responses
		w.WriteHeader(reply.code)
		_, _ = io.WriteString(w, reply.body)
	})}
	go fake.Serve(listener)
	defer fake.Close()
	for _, test := range []struct {
		body string
		code int
		want error
	}{
		{`[]`, 200, nil},
		{`[{"State":"running","Labels":{"io.vpnmodo.managed-by":"vpnmodo","io.vpnmodo.profile":"example","com.docker.compose.project":"vpnmodo-example"}}]`, 200, profilestore.ErrBusy},
		{`[{"State":"paused","Labels":{"io.vpnmodo.managed-by":"vpnmodo","io.vpnmodo.profile":"example","com.docker.compose.project":"vpnmodo-example"}}]`, 200, profilestore.ErrBusy},
		{`[{"State":"running","Labels":{}}]`, 200, profilestore.ErrUnavailable},
		{`null`, 200, profilestore.ErrUnavailable},
		{`synthetic-sensitive`, 503, profilestore.ErrUnavailable},
	} {
		responses <- answer{test.body, test.code}
		if err := profileInactive(context.Background(), socket, "example"); !errors.Is(err, test.want) {
			t.Fatal("unexpected inactivity result", err)
		}
	}
}
