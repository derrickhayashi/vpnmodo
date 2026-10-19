// Package protocol defines versioned JSON-RPC messages and stream framing.
package protocol

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"strconv"
	"sync/atomic"

	"github.com/derrickhayashi/vpn-sidecars-next/internal/jsonstrict"
	"github.com/derrickhayashi/vpn-sidecars-next/internal/operation"
	"github.com/derrickhayashi/vpn-sidecars-next/internal/profile"
)

const JSONRPCVersion = "2.0"
const MaxMessageBytes = 1024 * 1024

type APIVersion struct {
	Major int `json:"major"`
	Minor int `json:"minor"`
}

func CurrentAPI() APIVersion { return APIVersion{Major: 1, Minor: 0} }

// Compatible accepts same-major clients no newer than the server. Feature
// capability checks still apply; JSON-RPC's own "2.0" is a separate version.
func (server APIVersion) Compatible(client APIVersion) bool {
	return server.Major > 0 && server.Minor >= 0 && client.Major == server.Major && client.Minor >= 0 && client.Minor <= server.Minor
}

type Method string

const (
	Hello                Method = "system.hello"
	SystemStatus         Method = "system.status"
	BackendList          Method = "backend.list"
	BackendDescribe      Method = "backend.describe"
	ProfileCreate        Method = "profile.create"
	ProfileList          Method = "profile.list"
	ProfileInspect       Method = "profile.inspect"
	ProfileUpdate        Method = "profile.update"
	CredentialRotate     Method = "profile.rotate_credentials"
	ProfileDelete        Method = "profile.delete"
	ConnectionEnsure     Method = "connection.ensure"
	ConnectionDisconnect Method = "connection.disconnect"
	OperationStatus      Method = "operation.status"
	OperationSubscribe   Method = "operation.subscribe"
	OperationCancel      Method = "operation.cancel"
	OperationSubmitMFA   Method = "operation.submit_mfa"
	Doctor               Method = "system.doctor"
	OperationChanged     Method = "operation.changed"
)

func (m Method) Known() bool {
	switch m {
	case Hello, SystemStatus, BackendList, BackendDescribe, ProfileCreate, ProfileList, ProfileInspect, ProfileUpdate, CredentialRotate, ProfileDelete, ConnectionEnsure, ConnectionDisconnect, OperationStatus, OperationSubscribe, OperationCancel, OperationSubmitMFA, Doctor:
		return true
	default:
		return false
	}
}

// Request/Notification are wire-only values: Params may contain secrets. Format
// redacts them, but raw encoded bytes remain sensitive and must never be logged.
type Request struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  Method          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

func (r Request) Notification() bool { return len(r.ID) == 0 }

func DecodeRequest(data []byte) (Request, error) {
	if err := jsonstrict.Check(data, MaxMessageBytes); err != nil {
		if err == jsonstrict.ErrSyntax {
			return Request{}, NewRPCError(CodeParse)
		}
		return Request{}, NewRPCError(CodeInvalidRequest)
	}
	var r Request
	if jsonstrict.Decode(data, &r, MaxMessageBytes) != nil || r.JSONRPC != JSONRPCVersion || len(r.Method) == 0 || len(r.Method) > 128 || (len(r.ID) != 0 && !validID(r.ID)) {
		return Request{}, NewRPCError(CodeInvalidRequest)
	}
	if len(r.Params) != 0 && bytes.TrimSpace(r.Params)[0] != '{' {
		return Request{}, NewRPCError(CodeInvalidParams)
	}
	return r, nil
}

func validID(id []byte) bool {
	id = bytes.TrimSpace(id)
	if len(id) == 0 || len(id) > 128 {
		return false
	}
	if id[0] == '"' {
		var s string
		return json.Unmarshal(id, &s) == nil && s != ""
	}
	// v1 calls use string IDs or integer IDs. Null/fractional IDs are excluded
	// by the application contract, even though JSON-RPC permits them generally.
	_, err := strconv.ParseInt(string(id), 10, 64)
	return err == nil
}

type HelloParams struct {
	API APIVersion `json:"api"`
}
type HelloResult struct {
	API               APIVersion            `json:"api"`
	ControllerVersion string                `json:"controller_version"`
	Backends          []profile.BackendType `json:"backends"`
}

func DecodeHello(data []byte) (HelloParams, error) {
	var p HelloParams
	if decodeParams(data, &p) != nil {
		return HelloParams{}, NewRPCError(CodeInvalidParams)
	}
	return p, nil
}

// Session gates calls on negotiation. The transport adapter must never send a
// response to a notification, including one rejected here, and must not execute
// it. This stage defines contracts, not a JSON-RPC dispatcher.
type Session struct{ negotiated atomic.Bool }

func (s *Session) Reset() { s.negotiated.Store(false) }

func (s *Session) Negotiate(p HelloParams) error {
	s.negotiated.Store(false)
	if !CurrentAPI().Compatible(p.API) {
		return NewRPCError(CodeVersion)
	}
	s.negotiated.Store(true)
	return nil
}
func (s *Session) Check(r Request) error {
	if r.JSONRPC != JSONRPCVersion || !validID(r.ID) {
		return NewRPCError(CodeInvalidRequest)
	}
	if !r.Method.Known() {
		return NewRPCError(CodeMethod)
	}
	if r.Method != Hello && !s.negotiated.Load() {
		return NewRPCError(CodeHandshake)
	}
	return nil
}

type ErrorCode int

const (
	CodeParse              ErrorCode = -32700
	CodeInvalidRequest     ErrorCode = -32600
	CodeMethod             ErrorCode = -32601
	CodeInvalidParams      ErrorCode = -32602
	CodeInternal           ErrorCode = -32603
	CodeVersion            ErrorCode = -32001
	CodeHandshake          ErrorCode = -32002
	CodeNotFound           ErrorCode = -32003
	CodeBusy               ErrorCode = -32004
	CodeConflict           ErrorCode = -32005
	CodeCredentials        ErrorCode = -32006
	CodeMFAExpired         ErrorCode = -32007
	CodeInterrupted        ErrorCode = -32008
	CodeBackendUnavailable ErrorCode = -32009
	CodeProfileActive      ErrorCode = -32010
)

var errorMessages = map[ErrorCode]string{
	CodeParse: "Invalid JSON", CodeInvalidRequest: "Invalid request", CodeMethod: "Method not found", CodeInvalidParams: "Invalid parameters", CodeInternal: "Internal error",
	CodeVersion: "Incompatible API version", CodeHandshake: "Version negotiation required", CodeNotFound: "Resource not found", CodeBusy: "Operation in progress", CodeConflict: "Resource conflict", CodeCredentials: "Credentials unavailable or invalid", CodeMFAExpired: "MFA challenge expired", CodeInterrupted: "Operation interrupted; explicit retry required", CodeBackendUnavailable: "Backend unavailable", CodeProfileActive: "Profile active; disconnect before changing it",
}

// RPCError is deliberately closed: no raw stderr, paths, or arbitrary error data.
type RPCError struct {
	Code    ErrorCode `json:"code"`
	Message string    `json:"message"`
}

func NewRPCError(code ErrorCode) *RPCError {
	message, ok := errorMessages[code]
	if !ok {
		code = CodeInternal
		message = errorMessages[code]
	}
	return &RPCError{Code: code, Message: message}
}
func (e *RPCError) Error() string {
	if e == nil {
		return ""
	}
	return NewRPCError(e.Code).Message
}
func (e *RPCError) Valid() bool {
	if e == nil {
		return false
	}
	message, ok := errorMessages[e.Code]
	return ok && e.Message == message
}
func (e RPCError) MarshalJSON() ([]byte, error) {
	safe := NewRPCError(e.Code)
	return json.Marshal(struct {
		Code    ErrorCode `json:"code"`
		Message string    `json:"message"`
	}{safe.Code, safe.Message})
}
func (e RPCError) Format(state fmt.State, verb rune) {
	_, _ = io.WriteString(state, NewRPCError(e.Code).Message)
}

// EncodeResult and EncodeError create mutually exclusive response envelopes.
// Result values must be typed, credential-free method results. Secret blocks
// accidental JSON encoding, but cannot protect arbitrary raw strings/bytes.
func EncodeResult(id json.RawMessage, result any) ([]byte, error) {
	if !validID(id) {
		return nil, NewRPCError(CodeInvalidRequest)
	}
	return encode(struct {
		JSONRPC string          `json:"jsonrpc"`
		ID      json.RawMessage `json:"id"`
		Result  any             `json:"result"`
	}{JSONRPCVersion, id, result})
}

func EncodeError(id json.RawMessage, code ErrorCode) ([]byte, error) {
	if !validID(id) {
		id = json.RawMessage("null")
	}
	return encode(struct {
		JSONRPC string          `json:"jsonrpc"`
		ID      json.RawMessage `json:"id"`
		Error   *RPCError       `json:"error"`
	}{JSONRPCVersion, id, NewRPCError(code)})
}

func EncodeOperationChanged(operation Operation) ([]byte, error) {
	if err := operation.Validate(); err != nil {
		return nil, err
	}
	params, err := encode(struct {
		Operation Operation `json:"operation"`
	}{operation})
	if err != nil {
		return nil, err
	}
	return encode(Request{JSONRPC: JSONRPCVersion, Method: OperationChanged, Params: params})
}

type ProfileParams struct {
	Profile string `json:"profile"`
}
type OperationParams struct {
	OperationID string `json:"operation_id"`
}

func DecodeProfileParams(data []byte) (ProfileParams, error) {
	var p ProfileParams
	if decodeParams(data, &p) != nil || !profile.ValidName(p.Profile) {
		return ProfileParams{}, NewRPCError(CodeInvalidParams)
	}
	return p, nil
}
func DecodeOperationParams(data []byte) (OperationParams, error) {
	var p OperationParams
	if decodeParams(data, &p) != nil || !operation.ValidID(p.OperationID) {
		return OperationParams{}, NewRPCError(CodeInvalidParams)
	}
	return p, nil
}
func decodeParams(data []byte, target any) error {
	return jsonstrict.Decode(data, target, MaxMessageBytes)
}
func encode(value any) ([]byte, error) { return json.Marshal(value) }
