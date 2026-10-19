package secret

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
)

func TestValueRemainsRedactedOutsideExplicitTransportEncoding(t *testing.T) {
	value := New("synthetic-sensitive")
	for _, format := range []string{"%v", "%+v", "%#v", "%s", "%q", "%x"} {
		if strings.Contains(fmt.Sprintf(format, value), "synthetic-sensitive") {
			t.Fatalf("secret disclosed with %s", format)
		}
	}
	if _, err := json.Marshal(value); !errors.Is(err, ErrEncoding) {
		t.Fatal("ordinary encoding allowed", err)
	}
	if !value.Check(func(plain string) bool { return plain == "synthetic-sensitive" }) {
		t.Fatal("validation lost value")
	}
	encoded, err := value.EncodeForTransport()
	if err != nil || string(encoded) != `"synthetic-sensitive"` {
		t.Fatal("explicit transport encoding failed")
	}
	var roundtrip Value
	if err := json.Unmarshal(encoded, &roundtrip); err != nil {
		t.Fatal(err)
	}
	if !roundtrip.Check(func(plain string) bool { return plain == "synthetic-sensitive" }) {
		t.Fatal("round trip changed secret")
	}
	for _, invalid := range []string{`null`, `123`, `{}`} {
		if err := json.Unmarshal([]byte(invalid), &roundtrip); err == nil {
			t.Fatal("invalid secret input accepted")
		}
	}
}
