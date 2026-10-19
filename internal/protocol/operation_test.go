package protocol

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/derrickhayashi/vpn-sidecars-next/internal/operation"
	"github.com/derrickhayashi/vpn-sidecars-next/internal/secret"
)

func testOperation(state operation.State) Operation {
	return Operation{ID: "op-" + strings.Repeat("a", 32), Profile: "example", Kind: operation.EnsureConnection, State: state, Revision: 1}
}

func TestChallengeBindingExpirationAndSecretDelivery(t *testing.T) {
	now := time.Date(2026, 10, 19, 0, 0, 0, 0, time.UTC)
	o := testOperation(operation.AwaitingMFA)
	o.Challenge = &operation.MFAChallenge{ID: "mfa-" + strings.Repeat("b", 32), ExpiresAt: now.Add(time.Minute)}
	if err := o.CheckChallenge(o.ID, o.Challenge.ID, now); err != nil {
		t.Fatal(err)
	}
	assertRPC(t, o.CheckChallenge(o.ID, o.Challenge.ID, o.Challenge.ExpiresAt), CodeMFAExpired)
	if o.CheckChallenge("op-"+strings.Repeat("c", 32), o.Challenge.ID, now) == nil || o.CheckChallenge(o.ID, "mfa-"+strings.Repeat("d", 32), now) == nil {
		t.Fatal("challenge not bound")
	}
	p := MFASubmitParams{OperationID: o.ID, ChallengeID: o.Challenge.ID, Code: secret.New("123456")}
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
		p.Code = secret.New(code)
		if p.Validate() == nil {
			t.Fatal("invalid code accepted")
		}
	}
	persisted, err := json.Marshal(o)
	if err != nil || bytes.Contains(persisted, []byte(`"code"`)) {
		t.Fatal("MFA persisted", err)
	}
	o.State = operation.Running
	if o.Validate() == nil {
		t.Fatal("stale challenge retained")
	}
	o.Challenge = nil
	o.State = operation.Failed
	if o.Validate() == nil {
		t.Fatal("failure without classified reason")
	}
	o.Failure = NewRPCError(CodeInterrupted)
	if err := o.Validate(); err != nil {
		t.Fatal(err)
	}
	o.State = operation.Succeeded
	if o.Validate() == nil {
		t.Fatal("success with failure")
	}
}
