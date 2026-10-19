package jsonstrict

import (
	"errors"
	"strings"
	"testing"
)

func TestRejectsAmbiguousOrUnboundedInput(t *testing.T) {
	for name, data := range map[string]string{
		"duplicate":         `{"a":1,"a":2}`,
		"escaped duplicate": `{"a":1,"\u0061":2}`,
		"nested duplicate":  `{"a":[{"b":1,"b":2}]}`,
		"trailing":          `{} {}`,
		"truncated":         `{"a":`,
		"invalid utf8":      "{\"a\":\"\xff\"}",
		"too deep":          strings.Repeat("[", MaxDepth+2) + "0" + strings.Repeat("]", MaxDepth+2),
	} {
		t.Run(name, func(t *testing.T) {
			if Check([]byte(data), 4096) == nil {
				t.Fatal("accepted invalid input")
			}
		})
	}
	if !errors.Is(Check([]byte(`{}`), 1), ErrLimit) {
		t.Fatal("size limit ignored")
	}
}

func TestExactFieldNamesAndNestedTypes(t *testing.T) {
	type child struct {
		Name string `json:"name"`
	}
	type object struct {
		Child child   `json:"child"`
		Items []child `json:"items,omitempty"`
	}
	for _, input := range []string{
		`{"CHILD":{"name":"ok"}}`,
		`{"child":{"NAME":"ok"}}`,
		`{"child":{"name":"ok","unknown":"synthetic-sensitive"}}`,
		`{"child":{"name":null}}`,
		`{"child":{"name":42}}`,
		`{"items":[{"name":"ok","extra":true}]}`,
		`{"child":null}`,
		`null`,
	} {
		var target object
		err := Decode([]byte(input), &target, 4096)
		if err == nil {
			t.Errorf("accepted: %s", input)
		}
		if err != nil && strings.Contains(err.Error(), "synthetic-sensitive") {
			t.Fatal("input disclosed")
		}
	}
	var target object
	if err := Decode([]byte(`{"child":{"name":"ok"},"items":[{"name":"also-ok"}]}`), &target, 4096); err != nil {
		t.Fatal(err)
	}
	if target.Child.Name != "ok" {
		t.Fatal("value lost")
	}
	if Decode([]byte(`{}`), target, 4096) == nil {
		t.Fatal("non-pointer target accepted")
	}
	if Decode([]byte(`{}`), (*object)(nil), 4096) == nil {
		t.Fatal("nil target accepted")
	}
}
