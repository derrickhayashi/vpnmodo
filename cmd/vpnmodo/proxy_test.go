package main

import (
	"bytes"
	"context"
	"io"
	"testing"
)

func TestPublicProxyRejectsInvalidDestinationsBeforeConnecting(t *testing.T) {
	for _, args := range [][]string{{"proxy"}, {"proxy", "example", "bad;host", "22"}, {"proxy", "example", "127.0.0.1", "0"}, {"proxy", "example", "127.0.0.1", "65536"}} {
		if streamCommand(context.Background(), "", args, bytes.NewReader(nil), io.Discard) == nil {
			t.Fatal("invalid destination accepted")
		}
	}
}
