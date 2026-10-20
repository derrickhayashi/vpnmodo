package managedvpn

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/derrickhayashi/vpn-sidecars-next/internal/eventlog"
	"github.com/derrickhayashi/vpn-sidecars-next/internal/profile"
)

func TestBothBackendDiagnosticFilteringAndDedup(t *testing.T) {
	for _, backend := range []profile.BackendType{profile.L2TP, profile.XAuth} {
		t.Run(string(backend), func(t *testing.T) {
			a, _, _, p := setup(t)
			if backend == profile.XAuth {
				p.Backend = profile.Backend{Type: profile.XAuth, XAuth: &profile.XAuthConfig{Server: "192.0.2.1"}}
			}
			if err := a.prepareDiagnostics(p); err != nil {
				t.Fatal(err)
			}
			dir := filepath.Join(t.TempDir(), "logs")
			logs, err := eventlog.Open(dir)
			if err != nil {
				t.Fatal(err)
			}
			a.SetEventLog(logs)
			stamp := time.Now().UTC().Format(time.RFC3339)
			spool := stamp + " 01234567-0123-0123-0123-0123456789ab:0 peer_delete\n" + stamp + " bad-id synthetic-password\n"
			expired := time.Now().Add(-15*24*time.Hour).UTC().Format(time.RFC3339) + " 01234567-0123-0123-0123-0123456789ac:0 dpd_timeout\n"
			path := filepath.Join(a.dir(p), "diagnostics", "events")
			if err = os.WriteFile(path, []byte(spool+expired), 0600); err != nil {
				t.Fatal(err)
			}
			a.collectDiagnostics(p)
			a.collectDiagnostics(p)
			result, err := logs.Query(eventlog.Query{Profile: p.Name})
			if err != nil || len(result.Events) != 1 || result.Events[0].Backend != backend {
				t.Fatal(result, err)
			}
			data, _ := os.ReadFile(path)
			if strings.Contains(string(data), "dpd_timeout") {
				t.Fatal("expired diagnostics retained")
			}
			c := container{ID: strings.Repeat("a", 64)}
			c.State.ExitCode = 31
			a.logContainerExit(p, c)
			a.logContainerExit(p, c)
			result, err = logs.Query(eventlog.Query{Profile: p.Name})
			if err != nil || len(result.Events) != 2 || result.Events[0].ExitCode == nil {
				t.Fatal(result, err)
			}
			data, _ = os.ReadFile(filepath.Join(dir, "events.jsonl"))
			if strings.Contains(string(data), "synthetic-password") {
				t.Fatal("raw source text persisted")
			}
		})
	}
}
func TestDiagnosticCollectionBeforeContainerRemoval(t *testing.T) {
	a, _, _, p := setup(t)
	if err := a.Connect(context.Background(), p, nil); err != nil {
		t.Fatal(err)
	}
	logs, _ := eventlog.Open(filepath.Join(t.TempDir(), "logs"))
	a.SetEventLog(logs)
	spool := time.Now().UTC().Format(time.RFC3339) + " 01234567-0123-0123-0123-0123456789ab:0 ppp_down\n"
	os.WriteFile(filepath.Join(a.dir(p), "diagnostics", "events"), []byte(spool), 0600)
	if err := a.Disconnect(context.Background(), p); err != nil {
		t.Fatal(err)
	}
	result, err := logs.Query(eventlog.Query{Profile: p.Name})
	if err != nil || len(result.Events) != 1 || result.Events[0].Reason != "ppp_down" {
		t.Fatal(result, err)
	}
}
