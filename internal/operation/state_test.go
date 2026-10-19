package operation

import "testing"

func TestStateMachineRequiresVerificationAndTerminalStatesStayTerminal(t *testing.T) {
	path := []State{Queued, Running, AwaitingMFA, Running, Verifying, Succeeded}
	for i := 0; i < len(path)-1; i++ {
		if !CanTransition(EnsureConnection, path[i], path[i+1]) {
			t.Fatalf("valid transition rejected %s -> %s", path[i], path[i+1])
		}
	}
	if CanTransition(EnsureConnection, Running, Succeeded) {
		t.Fatal("connection succeeded without verification")
	}
	if CanTransition(RecoverConnection, Running, AwaitingMFA) {
		t.Fatal("background recovery requested MFA")
	}
	if CanTransition(DisconnectConnection, Running, Verifying) {
		t.Fatal("disconnect verification transition")
	}
	if !CanTransition(DisconnectConnection, Running, Succeeded) || !CanTransition(ChangeProfile, Running, Succeeded) {
		t.Fatal("generic completion blocked")
	}
	for _, kind := range []Kind{EnsureConnection, RecoverConnection, DisconnectConnection, ChangeProfile} {
		for _, terminal := range []State{Succeeded, Failed, Cancelled} {
			for _, to := range []State{Queued, Running, AwaitingMFA, Verifying, Succeeded, Failed, Cancelled} {
				if CanTransition(kind, terminal, to) {
					t.Fatal("terminal state restarted")
				}
			}
		}
		for _, from := range []State{Queued, Running} {
			if !CanTransition(kind, from, Cancelled) || !CanTransition(kind, from, Failed) {
				t.Fatal("cannot cancel/fail active operation")
			}
		}
	}
	if CanTransition("unknown", Queued, Running) || CanTransition(EnsureConnection, Running, Running) {
		t.Fatal("unknown or duplicate transition")
	}
}
