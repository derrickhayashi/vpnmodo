// Package secret provides values that redact formatting and require explicit
// encoding before entering a transport payload.
package secret

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
)

var ErrEncoding = errors.New("secret requires explicit transport encoding")
var ErrInput = errors.New("invalid secret input")

// Value does not promise secure erasure of Go-managed memory.
type Value struct{ value string }

func New(value string) Value                      { return Value{value: value} }
func (v Value) Format(state fmt.State, verb rune) { _, _ = io.WriteString(state, "[REDACTED]") }
func (v Value) MarshalJSON() ([]byte, error)      { return nil, ErrEncoding }
func (v *Value) UnmarshalJSON(data []byte) error {
	var value string
	if string(data) == "null" || json.Unmarshal(data, &value) != nil {
		return ErrInput
	}
	v.value = value
	return nil
}

// EncodeForTransport explicitly releases a JSON string for authenticated secret
// delivery. The bytes must never be logged, persisted as operation data or used
// as command arguments. Ordinary JSON encoding of Value remains blocked.
func (v Value) EncodeForTransport() ([]byte, error) { return json.Marshal(v.value) }

// Check allows validation without exposing the value as a reusable public field.
// The predicate must neither retain nor log the plaintext.
func (v Value) Check(predicate func(string) bool) bool { return predicate(v.value) }
