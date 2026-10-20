package lifecycle

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/derrickhayashi/vpn-sidecars-next/internal/eventlog"
	"github.com/derrickhayashi/vpn-sidecars-next/internal/operation"
)

func TestLifecycleEventsPersistAndLogFailureAllowsDisconnect(t *testing.T) {
	root := t.TempDir()
	logs, err := eventlog.Open(filepath.Join(root, "logs"))
	if err != nil {
		t.Fatal(err)
	}
	a := newAdapter()
	m := newManager(t, filepath.Join(root, "state"), a, Options{Events: logs})
	r, err := m.Ensure(context.Background(), "example")
	if err != nil {
		t.Fatal(err)
	}
	waitState(t, m, r.ID, operation.Succeeded)
	q, err := logs.Query(eventlog.Query{Profile: "example"})
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, e := range q.Events {
		if e.OperationID == r.ID && e.State == operation.Succeeded && e.Backend != "" {
			found = true
		}
	}
	if !found {
		t.Fatal("missing connection event")
	}
	// Repeated identical observations do not create a log flood.
	m.mu.Lock()
	before := m.state.Clone()
	m.logChanges(before, before)
	m.mu.Unlock()
	again, _ := logs.Query(eventlog.Query{Profile: "example"})
	if len(again.Events) != len(q.Events) {
		t.Fatal("duplicate snapshots logged")
	}
	// A disk/path failure must not turn an intentional teardown into a no-op.
	os.Rename(filepath.Join(root, "logs"), filepath.Join(root, "logs-saved"))
	r, err = m.Disconnect(context.Background(), "example")
	if err != nil {
		t.Fatal(err)
	}
	waitState(t, m, r.ID, operation.Succeeded)
	if logs.Ready() {
		t.Fatal("logging failure hidden")
	}
	_, _, connected := a.counts("example")
	if connected {
		t.Fatal("logging failure prevented teardown")
	}
	logs, err = eventlog.Open(filepath.Join(root, "logs-saved"))
	if err != nil {
		t.Fatal(err)
	}
	q, err = logs.Query(eventlog.Query{Profile: "example"})
	if err != nil || len(q.Events) == 0 {
		t.Fatal("restart lost history", err)
	}
	b, _ := os.ReadFile(filepath.Join(root, "logs-saved", "events.jsonl"))
	for _, secret := range []string{"9876543210", "synthetic-sensitive-adapter-output"} {
		if strings.Contains(string(b), secret) {
			t.Fatal("secret in log")
		}
	}
}

func TestHealthChangesAndRecoveryAreLogged(t *testing.T) {
	logs, err := eventlog.Open(filepath.Join(t.TempDir(), "logs"))
	if err != nil {
		t.Fatal(err)
	}
	a := newRecoveryAdapter()
	options := recoveryOptions()
	options.Events = logs
	options.Recovery.Cooldown = time.Nanosecond
	m := openRecoveryManager(t, filepath.Join(t.TempDir(), "state"), a, options)
	c := ensureRecovery(t, m, "example")
	a.setHealth("example", operation.HealthTunnelDown)
	m.wait.Add(1)
	m.monitor(c)
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		next, _ := m.Connection("example")
		if next.LastOperation != c.LastOperation && next.ActiveOperation == "" {
			break
		}
		time.Sleep(time.Millisecond)
	}
	result, err := logs.Query(eventlog.Query{Profile: "example"})
	if err != nil {
		t.Fatal(err)
	}
	down, recovered := false, false
	for _, e := range result.Events {
		if e.Kind == "health" && e.Health == operation.HealthTunnelDown {
			down = true
		}
		if e.Kind == "operation" && e.Operation == operation.RecoverConnection && e.State == operation.Succeeded {
			recovered = true
		}
	}
	if !down || !recovered {
		t.Fatal("missing loss/recovery timeline", result)
	}
}
