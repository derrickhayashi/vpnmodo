package contract

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"
)

func testOperation(state OperationState) Operation {
	return Operation{ID: "op-" + strings.Repeat("a", 32), Profile: "example", Kind: EnsureConnection, State: state, Revision: 1}
}

func TestStateMachineRequiresVerificationAndTerminalStatesStayTerminal(t *testing.T) {
	path := []OperationState{Queued, Running, AwaitingMFA, Running, Verifying, Succeeded}
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
	for _, kind := range []OperationKind{EnsureConnection, RecoverConnection, DisconnectConnection, ChangeProfile} {
		for _, terminal := range []OperationState{Succeeded, Failed, Cancelled} {
			for _, to := range []OperationState{Queued, Running, AwaitingMFA, Verifying, Succeeded, Failed, Cancelled} {
				if CanTransition(kind, terminal, to) {
					t.Fatal("terminal state restarted")
				}
			}
		}
		for _, from := range []OperationState{Queued, Running} {
			if !CanTransition(kind, from, Cancelled) || !CanTransition(kind, from, Failed) {
				t.Fatal("cannot cancel/fail active operation")
			}
		}
	}
	if CanTransition("unknown", Queued, Running) || CanTransition(EnsureConnection, Running, Running) {
		t.Fatal("unknown or duplicate transition")
	}
}

func TestChallengeBindingExpirationAndSecretDelivery(t *testing.T) {
	now := time.Date(2026, 10, 19, 0, 0, 0, 0, time.UTC)
	o := testOperation(AwaitingMFA)
	o.Challenge = &MFAChallenge{ID: "mfa-" + strings.Repeat("b", 32), ExpiresAt: now.Add(time.Minute)}
	if err := o.CheckChallenge(o.ID, o.Challenge.ID, now); err != nil {
		t.Fatal(err)
	}
	assertRPC(t, o.CheckChallenge(o.ID, o.Challenge.ID, o.Challenge.ExpiresAt), CodeMFAExpired)
	if o.CheckChallenge("op-"+strings.Repeat("c", 32), o.Challenge.ID, now) == nil || o.CheckChallenge(o.ID, "mfa-"+strings.Repeat("d", 32), now) == nil {
		t.Fatal("challenge not bound")
	}
	p := MFASubmitParams{OperationID: o.ID, ChallengeID: o.Challenge.ID, Code: NewSecret("123456")}
	if _, err := json.Marshal(p); err == nil {
		t.Fatal("MFA ordinary serialization")
	}
	if strings.Contains(fmt.Sprintf("%#v", p), "123456") {
		t.Fatal("MFA formatting")
	}
	encoded, err := p.EncodeForTransport()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := DecodeMFASubmit(encoded); err != nil {
		t.Fatal(err)
	}
	for _, code := range []string{"", "123", "12345678901", "12a456", "123\n45"} {
		p.Code = NewSecret(code)
		if p.Validate() == nil {
			t.Fatal("invalid code accepted")
		}
	}
	persisted, err := json.Marshal(o)
	if err != nil || bytes.Contains(persisted, []byte(`"code"`)) {
		t.Fatal("MFA persisted", err)
	}
	o.State = Running
	if o.Validate() == nil {
		t.Fatal("stale challenge retained")
	}
	o.Challenge = nil
	o.State = Failed
	if o.Validate() == nil {
		t.Fatal("failure without classified reason")
	}
	o.Failure = NewRPCError(CodeInterrupted)
	if err := o.Validate(); err != nil {
		t.Fatal(err)
	}
	o.State = Succeeded
	if o.Validate() == nil {
		t.Fatal("success with failure")
	}
}
