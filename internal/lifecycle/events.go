package lifecycle

import (
	"github.com/derrickhayashi/vpn-sidecars-next/internal/eventlog"
	"github.com/derrickhayashi/vpn-sidecars-next/internal/operationstore"
)

// Called only after a durable journal commit. A log failure never prevents safe
// teardown or changes the outcome of a committed lifecycle operation.
func (m *Manager) logChanges(old, next operationstore.State) {
	if m.options.Events == nil {
		return
	}
	for id, r := range next.Operations {
		before, ok := old.Operations[id]
		if ok && before.State == r.State && before.Failure == r.Failure {
			continue
		}
		_ = m.options.Events.Append(eventlog.Event{At: r.UpdatedAt, Profile: r.Profile, Backend: next.Connections[r.Profile].Backend, Kind: "operation", OperationID: r.ID, Operation: r.Kind, State: r.State, Failure: r.Failure})
	}
	for name, c := range next.Connections {
		before, ok := old.Connections[name]
		if !ok || before.Desired != c.Desired || before.Observed != c.Observed {
			_ = m.options.Events.Append(eventlog.Event{Profile: name, Backend: c.Backend, Kind: "connection", OperationID: c.LastOperation, Desired: c.Desired, Observed: c.Observed})
		}
		if c.Recovery != nil && (!ok || before.Recovery == nil || before.Recovery.Health != c.Recovery.Health) {
			_ = m.options.Events.Append(eventlog.Event{At: c.Recovery.CheckedAt, Profile: name, Backend: c.Backend, Kind: "health", OperationID: c.LastOperation, Health: c.Recovery.Health})
		}
	}
}
