package credentialinput

import (
	"context"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"
)

const synthetic = `{"username":"synthetic-user","password":"synthetic-password","psk":"synthetic-shared"}`

func TestStrictBoundedInputAndRedaction(t *testing.T) {
	c, err := Decode(strings.NewReader(synthetic))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(fmt.Sprintf("%+v", c), "synthetic-") {
		t.Fatal("formatted credential leaked")
	}
	for _, data := range []string{`{}`, `{"username":"x","username":"y"}`, synthetic + `{}`, strings.Repeat("x", 65537), strings.Replace(synthetic, "username", "Username", 1)} {
		if _, err := Decode(strings.NewReader(data)); err == nil || strings.Contains(err.Error(), "synthetic") {
			t.Fatal("invalid input accepted/disclosed")
		}
	}
}
func TestPrivateDescriptorAndCancellation(t *testing.T) {
	file, err := os.CreateTemp(t.TempDir(), "credentials-")
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	if _, err = file.WriteString(synthetic); err != nil {
		t.Fatal(err)
	}
	if _, err = file.Seek(0, 0); err != nil {
		t.Fatal(err)
	}
	if _, err = Read(context.Background(), false, fmt.Sprint(file.Fd())); err != nil {
		t.Fatal(err)
	}
	if err = file.Chmod(0644); err != nil {
		t.Fatal(err)
	}
	if _, err = Read(context.Background(), false, fmt.Sprint(file.Fd())); err == nil {
		t.Fatal("public credential file accepted")
	}
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	defer writer.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	done := make(chan error, 1)
	go func() { _, err := Read(ctx, false, fmt.Sprint(reader.Fd())); done <- err }()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("cancelled input accepted")
		}
	case <-time.After(time.Second):
		t.Fatal("credential read ignored cancellation")
	}
}
