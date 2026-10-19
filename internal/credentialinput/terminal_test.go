package credentialinput

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"
)

// Invoked only by the native PTY smoke harness with synthetic input.
func TestTerminalHelper(t *testing.T) {
	mode := os.Getenv("VPNMODO_SYNTHETIC_TERMINAL")
	if mode == "" {
		t.Skip("native PTY helper")
	}
	timeout := 5 * time.Second
	if mode == "cancel" {
		timeout = 300 * time.Millisecond
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	if mode == "credentials" {
		_, err := Read(ctx, false, "")
		if err != nil {
			t.Fatal(err)
		}
	} else {
		_, err := ReadCode(ctx, false)
		if mode == "cancel" {
			if err == nil {
				t.Fatal("cancelled prompt accepted")
			}
		} else if err != nil {
			t.Fatal(err)
		}
	}
	fmt.Println("TERMINAL_RESTORED")
	var acknowledgement [1]byte
	if _, err := os.Stdin.Read(acknowledgement[:]); err != nil {
		t.Fatal("terminal harness acknowledgement failed")
	}
}
