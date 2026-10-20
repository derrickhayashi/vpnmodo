package managedvpn

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/derrickhayashi/vpnmodo/internal/eventlog"
	"github.com/derrickhayashi/vpnmodo/internal/privatefile"
	"github.com/derrickhayashi/vpnmodo/internal/profile"
)

func (a *Adapter) SetEventLog(events *eventlog.Store) { a.events = events }
func (a *Adapter) prepareDiagnostics(p profile.Profile) error {
	dir := filepath.Join(a.dir(p), "diagnostics")
	if privatefile.Dir(dir) != nil {
		return ErrRuntime
	}
	// Pre-create both files as the VM user. Root backend writers preserve these
	// inodes/ownership when rotating; the unprivileged controller can read them.
	for _, name := range []string{"events", "previous", "lock"} {
		path := filepath.Join(dir, name)
		if _, err := os.Lstat(path); os.IsNotExist(err) {
			if _, err = privatefile.Write(path, nil); err != nil {
				return ErrRuntime
			}
		} else if _, err = privatefile.Read(path, 128*1024); err != nil {
			return ErrRuntime
		}
	}
	return nil
}
func (a *Adapter) CollectDiagnostics(p profile.Profile) { a.collectDiagnostics(p) }
func (a *Adapter) collectDiagnostics(p profile.Profile) {
	if a.events == nil {
		return
	}
	dir := filepath.Join(a.dir(p), "diagnostics")
	if _, err := os.Lstat(dir); os.IsNotExist(err) {
		return
	}
	if privatefile.Dir(dir) != nil {
		a.events.MarkFailed()
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	unlock, err := privatefile.Lock(ctx, filepath.Join(dir, "lock"))
	if err != nil {
		a.events.MarkFailed()
		return
	}
	defer unlock()
	for _, name := range []string{"previous", "events"} {
		data, err := privatefile.Read(filepath.Join(a.dir(p), "diagnostics", name), 128*1024)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			a.events.MarkFailed()
			continue
		}
		lines := strings.Split(string(data), "\n")
		var retained strings.Builder
		changed := false
		// Ignore a last partial write; the next bounded probe reads it again.
		for _, line := range lines[:len(lines)-1] {
			fields := strings.Fields(line)
			if len(fields) != 3 || !eventlog.DiagnosticReason(fields[2]) {
				continue
			}
			at, err := time.Parse(time.RFC3339, fields[0])
			if err != nil {
				continue
			}
			if at.Before(time.Now().Add(-eventlog.Retention)) {
				changed = true
				continue
			}
			if err := a.events.Append(eventlog.Event{At: at, Profile: p.Name, Backend: p.Backend.Type, Kind: "diagnostic", Reason: fields[2], SourceID: fields[1]}); err != nil {
				retained.WriteString(line + "\n")
			} else {
				changed = true
			}
		}
		if changed {
			if _, err := privatefile.Write(filepath.Join(dir, name), []byte(retained.String())); err != nil {
				a.events.MarkFailed()
			}
		}
	}
}
func (a *Adapter) logContainerExit(p profile.Profile, c container) {
	if a.events == nil {
		return
	}
	reason := "container_exit"
	if c.State.OOMKilled {
		reason = "container_oom"
	} else if p.Backend.Type == profile.XAuth && c.State.ExitCode == 31 {
		reason = "client_exit"
	}
	code := c.State.ExitCode
	_ = a.events.Append(eventlog.Event{At: c.State.FinishedAt, Profile: p.Name, Backend: p.Backend.Type, Kind: "diagnostic", Reason: reason, ExitCode: &code, SourceID: c.ID + ":exit"})
}
