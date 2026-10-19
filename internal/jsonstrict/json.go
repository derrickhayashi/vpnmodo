// Package jsonstrict decodes configuration without accepting ambiguous JSON or
// including caller-controlled values in errors.
package jsonstrict

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"reflect"
	"strings"
	"unicode/utf8"
)

const MaxDepth = 32

var (
	ErrSyntax = errors.New("invalid JSON")
	ErrShape  = errors.New("unknown, duplicate, or invalid JSON field")
	ErrLimit  = errors.New("JSON input exceeds limits")
)

// Check rejects duplicate members at every level, invalid UTF-8, excessive depth,
// and trailing values. Errors never include input bytes or member names.
func Check(data []byte, limit int) error {
	if len(data) > limit {
		return ErrLimit
	}
	if !utf8.Valid(data) {
		return ErrSyntax
	}
	d := json.NewDecoder(bytes.NewReader(data))
	d.UseNumber()
	if err := value(d, 0); err != nil {
		return err
	}
	if _, err := d.Token(); err != io.EOF {
		return ErrSyntax
	}
	return nil
}

func value(d *json.Decoder, depth int) error {
	if depth > MaxDepth {
		return ErrLimit
	}
	token, err := d.Token()
	if err != nil {
		return ErrSyntax
	}
	delim, ok := token.(json.Delim)
	if !ok {
		return nil
	}
	switch delim {
	case '{':
		seen := make(map[string]bool)
		for d.More() {
			key, err := d.Token()
			if err != nil {
				return ErrSyntax
			}
			name, ok := key.(string)
			if !ok {
				return ErrSyntax
			}
			if seen[name] {
				return ErrShape
			}
			seen[name] = true
			if err := value(d, depth+1); err != nil {
				return err
			}
		}
	case '[':
		for d.More() {
			if err := value(d, depth+1); err != nil {
				return err
			}
		}
	default:
		return ErrSyntax
	}
	end, err := d.Token()
	if err != nil || (delim == '{' && end != json.Delim('}')) || (delim == '[' && end != json.Delim(']')) {
		return ErrSyntax
	}
	return nil
}

var unmarshaler = reflect.TypeFor[json.Unmarshaler]()
var rawMessage = reflect.TypeFor[json.RawMessage]()

// Decode uses exact JSON tag spelling, rather than encoding/json's default
// case-insensitive matching. Custom UnmarshalJSON implementations must enforce
// their own shape; Check has already bounded their input and rejected duplicates.
func Decode(data []byte, target any, limit int) error {
	if err := Check(data, limit); err != nil {
		return err
	}
	t := reflect.TypeOf(target)
	if t == nil || t.Kind() != reflect.Pointer || reflect.ValueOf(target).IsNil() {
		return ErrShape
	}
	if err := shape(data, t.Elem()); err != nil {
		return err
	}
	d := json.NewDecoder(bytes.NewReader(data))
	d.DisallowUnknownFields()
	if err := d.Decode(target); err != nil {
		return ErrShape
	}
	return nil
}

func shape(data []byte, t reflect.Type) error {
	data = bytes.TrimSpace(data)
	if t == rawMessage || t.Kind() == reflect.Interface {
		return nil
	}
	if t.Kind() == reflect.Pointer {
		if bytes.Equal(data, []byte("null")) {
			return ErrShape
		}
		return shape(data, t.Elem())
	}
	if reflect.PointerTo(t).Implements(unmarshaler) {
		return nil
	}
	if bytes.Equal(data, []byte("null")) {
		return ErrShape
	}
	switch t.Kind() {
	case reflect.Struct:
		var fields map[string]json.RawMessage
		if json.Unmarshal(data, &fields) != nil || fields == nil {
			return ErrShape
		}
		known := make(map[string]reflect.Type)
		for field := range t.Fields() {
			if !field.IsExported() {
				continue
			}
			name := strings.Split(field.Tag.Get("json"), ",")[0]
			if name == "-" {
				continue
			}
			if name == "" {
				name = field.Name
			}
			known[name] = field.Type
		}
		for name, raw := range fields {
			field, ok := known[name]
			if !ok {
				return ErrShape
			}
			if err := shape(raw, field); err != nil {
				return err
			}
		}
	case reflect.Slice, reflect.Array:
		var items []json.RawMessage
		if json.Unmarshal(data, &items) != nil {
			return ErrShape
		}
		for _, item := range items {
			if err := shape(item, t.Elem()); err != nil {
				return err
			}
		}
	}
	return nil
}
