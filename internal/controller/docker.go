package controller

import (
	"context"
	"io"
	"net"
	"net/http"
	"time"
)

// The skeleton only probes the Unix socket; it cannot modify Docker resources.
func dockerReady(ctx context.Context, socket string) bool {
	if socket == "" {
		return false
	}
	transport := &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{Timeout: 2 * time.Second}).DialContext(ctx, "unix", socket)
	}}
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport, Timeout: 3 * time.Second}
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, "http://docker/_ping", nil)
	resp, err := client.Do(req)
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 16))
	return err == nil && resp.StatusCode == http.StatusOK && string(body) == "OK"
}
