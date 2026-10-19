package protocol

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"testing"

	"github.com/derrickhayashi/vpn-sidecars-next/internal/operation"
	"github.com/derrickhayashi/vpn-sidecars-next/internal/secret"
)

func assertRPC(t *testing.T, err error, code ErrorCode) {
	t.Helper()
	var rpc *RPCError
	if !errors.As(err, &rpc) || rpc.Code != code {
		t.Fatalf("want RPC code %d, got %v", code, err)
	}
}

func TestRequestParsingAndVersionGate(t *testing.T) {
	r, err := DecodeRequest([]byte(`{"jsonrpc":"2.0","id":"req-1","method":"profile.list","params":{}}`))
	if err != nil {
		t.Fatal(err)
	}
	var session Session
	assertRPC(t, session.Check(r), CodeHandshake)
	assertRPC(t, session.Negotiate(HelloParams{API: APIVersion{Major: 2}}), CodeVersion)
	if err := session.Negotiate(HelloParams{API: CurrentAPI()}); err != nil {
		t.Fatal(err)
	}
	if err := session.Check(r); err != nil {
		t.Fatal(err)
	}
	assertRPC(t, session.Negotiate(HelloParams{API: APIVersion{Major: 1, Minor: 1}}), CodeVersion)
	assertRPC(t, session.Check(r), CodeHandshake)
	if CurrentAPI().Compatible(APIVersion{Major: 1, Minor: -1}) {
		t.Fatal("negative minor version accepted")
	}
	if !(APIVersion{Major: 1, Minor: 2}).Compatible(CurrentAPI()) {
		t.Fatal("older client rejected")
	}
	hello, err := DecodeHello([]byte(`{"api":{"major":1,"minor":0}}`))
	if err != nil || hello.API != CurrentAPI() {
		t.Fatal("hello decode", err)
	}
	if _, err := DecodeHello([]byte(`{"api":{"Major":1,"minor":0}}`)); err == nil {
		t.Fatal("case-insensitive hello")
	}
	unknown := r
	unknown.Method = "rpc.arbitrary"
	assertRPC(t, session.Check(unknown), CodeMethod)
	notification, err := DecodeRequest([]byte(`{"jsonrpc":"2.0","method":"connection.ensure","params":{"profile":"example"}}`))
	if err != nil || !notification.Notification() {
		t.Fatal("notification decode", err)
	}
	assertRPC(t, session.Check(notification), CodeInvalidRequest)
	// The transport must ignore the rejected notification, never reply or mutate.
}

func TestInvalidRequestsAndSafeErrors(t *testing.T) {
	for _, input := range []string{
		`[]`, `null`, `{}`, `{"jsonrpc":"1.0","method":"system.hello","id":1}`,
		`{"jsonrpc":"2.0","method":1,"id":1}`,
		`{"jsonrpc":"2.0","method":"system.hello","id":null}`,
		`{"jsonrpc":"2.0","method":"system.hello","id":1.5}`,
		`{"jsonrpc":"2.0","method":"system.hello","id":{}}`,
		`{"jsonrpc":"2.0","method":"system.hello","id":1,"params":[]}`,
		`{"jsonrpc":"2.0","method":"system.hello","id":1,"params":null}`,
		`{"jsonrpc":"2.0","method":"system.hello","id":1,"params":{"a":1,"a":2}}`,
		`{"jsonrpc":"2.0","method":"system.hello","id":1,"unknown":"synthetic-sensitive"}`,
	} {
		if _, err := DecodeRequest([]byte(input)); err == nil {
			t.Errorf("accepted %s", input)
		} else if strings.Contains(err.Error(), "synthetic-sensitive") {
			t.Fatal("input disclosed")
		}
	}
	_, err := DecodeRequest([]byte(`{"secret":"synthetic-sensitive"`))
	assertRPC(t, err, CodeParse)
	if _, err := DecodeProfileParams([]byte(`{"profile":"example"}`)); err != nil {
		t.Fatal(err)
	}
	if _, err := DecodeProfileParams([]byte(`{"profile":"../example"}`)); err == nil {
		t.Fatal("unsafe profile")
	}
	if _, err := DecodeOperationParams([]byte(`{"operation_id":"op-` + strings.Repeat("a", 32) + `"}`)); err != nil {
		t.Fatal(err)
	}
	if _, err := DecodeOperationParams([]byte(`{"operation_id":"example"}`)); err == nil {
		t.Fatal("bad operation ID")
	}
	forged := RPCError{Code: CodeInternal, Message: "synthetic-sensitive"}
	encoded, err := json.Marshal(forged)
	if err != nil || bytes.Contains(encoded, []byte("synthetic-sensitive")) || strings.Contains(fmt.Sprintf("%#v", forged), "synthetic-sensitive") {
		t.Fatal("unsafe error output")
	}
	if forged.Valid() || !NewRPCError(CodeInternal).Valid() || NewRPCError(-123).Code != CodeInternal {
		t.Fatal("unsafe error classification")
	}
	request := Request{Params: json.RawMessage(`{"code":"synthetic-sensitive"}`)}
	if strings.Contains(fmt.Sprintf("%#v", request), "synthetic-sensitive") {
		t.Fatal("raw RPC params formatted")
	}
}

func TestResponseAndNotificationEnvelopes(t *testing.T) {
	result, err := EncodeResult(json.RawMessage(`"req-1"`), struct {
		Ready bool `json:"ready"`
	}{true})
	if err != nil {
		t.Fatal(err)
	}
	var decoded map[string]json.RawMessage
	if err := json.Unmarshal(result, &decoded); err != nil {
		t.Fatal(err)
	}
	if string(decoded["id"]) != `"req-1"` || decoded["result"] == nil || decoded["error"] != nil {
		t.Fatal("wrong result envelope")
	}
	result, err = EncodeError(nil, CodeParse)
	if err != nil {
		t.Fatal(err)
	}
	decoded = nil
	if err := json.Unmarshal(result, &decoded); err != nil {
		t.Fatal(err)
	}
	if string(decoded["id"]) != `null` || decoded["error"] == nil || decoded["result"] != nil {
		t.Fatal("wrong error envelope")
	}
	if _, err := EncodeResult(nil, true); err == nil {
		t.Fatal("invalid response ID")
	}
	if _, err := EncodeResult(json.RawMessage(`1`), secret.New("synthetic-sensitive")); err == nil {
		t.Fatal("secret in result")
	}
	event, err := EncodeOperationChanged(testOperation(operation.Queued))
	if err != nil {
		t.Fatal(err)
	}
	r, err := DecodeRequest(event)
	if err != nil || !r.Notification() || r.Method != OperationChanged {
		t.Fatal("invalid server event", err)
	}
}

func TestFramingBoundsAndConcurrentWrites(t *testing.T) {
	var buffer bytes.Buffer
	w := NewFrameWriter(&buffer)
	var group sync.WaitGroup
	for i := 0; i < 24; i++ {
		group.Go(func() {
			if err := w.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":{}}`)); err != nil {
				t.Error(err)
			}
		})
	}
	group.Wait()
	r := NewFrameReader(&buffer)
	for i := 0; i < 24; i++ {
		if _, err := r.Read(); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := r.Read(); err != io.EOF {
		t.Fatal("expected EOF", err)
	}
	for _, input := range []string{`{"unfinished":`, "{}", "\n", "[]\n", "{} {}\n", "{\"a\":1,\"a\":2}\n", strings.Repeat(" ", MaxMessageBytes+2) + "\n"} {
		if _, err := NewFrameReader(strings.NewReader(input)).Read(); err == nil {
			t.Fatal("accepted invalid frame")
		}
	}
	for _, input := range []string{"{}\n", "{}\r", `[]`, `{"a":1,"a":2}`, strings.Repeat("x", MaxMessageBytes+1)} {
		if err := w.Write([]byte(input)); err == nil {
			t.Fatal("wrote invalid frame")
		}
	}
	prefix, suffix := `{"padding":"`, `"}`
	boundary := prefix + strings.Repeat("x", MaxMessageBytes-len(prefix)-len(suffix)) + suffix
	if err := w.Write([]byte(boundary)); err != nil {
		t.Fatal("boundary rejected", err)
	}
	if _, err := NewFrameReader(&buffer).Read(); err != nil {
		t.Fatal("boundary read rejected", err)
	}
	if err := NewFrameWriter(shortWriter{}).Write([]byte(`{}`)); !errors.Is(err, io.ErrShortWrite) {
		t.Fatal("short write ignored", err)
	}
}

type shortWriter struct{}

func (shortWriter) Write(p []byte) (int, error) { return len(p) - 1, nil }

func FuzzDecodeRequest(f *testing.F) {
	for _, seed := range []string{`{"jsonrpc":"2.0","id":1,"method":"system.hello"}`, `null`, `[]`, `{"params":null}`} {
		f.Add([]byte(seed))
	}
	f.Fuzz(func(t *testing.T, data []byte) {
		r, err := DecodeRequest(data)
		if err == nil && (r.JSONRPC != JSONRPCVersion || r.Method == "") {
			t.Fatal("invalid accepted request")
		}
	})
}
