package controller

import (
	"context"
	"encoding/json"
	"github.com/derrickhayashi/vpnmodo/internal/profile"
	"github.com/derrickhayashi/vpnmodo/internal/profilestore"
	"io"
	"net"
	"net/http"
	"net/url"
	"time"
)

// Docker access here is read-only: readiness and managed inactivity checks.
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

// Profile mutations fail closed if Docker cannot prove the managed deployment
// inactive. Queries are filtered by all ownership labels; legacy VPNs are ignored.
func profileInactive(ctx context.Context, socket, name string) error {
	identity, err := profile.Identity(name)
	if err != nil {
		return profilestore.ErrUnavailable
	}
	labels := []string{profile.ManagerLabel + "=" + profile.ManagerValue, profile.ProfileLabel + "=" + name, "com.docker.compose.project=" + identity.Project}
	filters, _ := json.Marshal(map[string][]string{"label": labels})
	transport := &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{Timeout: 2 * time.Second}).DialContext(ctx, "unix", socket)
	}}
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport, Timeout: 3 * time.Second}
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, "http://docker/containers/json?all=1&filters="+url.QueryEscape(string(filters)), nil)
	response, err := client.Do(req)
	if err != nil {
		return profilestore.ErrUnavailable
	}
	defer response.Body.Close()
	data, err := io.ReadAll(io.LimitReader(response.Body, 1024*1024+1))
	if err != nil || len(data) > 1024*1024 || response.StatusCode != http.StatusOK {
		return profilestore.ErrUnavailable
	}
	var containers []struct {
		Labels map[string]string
		State  string
	}
	if json.Unmarshal(data, &containers) != nil || containers == nil {
		return profilestore.ErrUnavailable
	}
	for _, container := range containers {
		if !identity.Matches(container.Labels["com.docker.compose.project"], container.Labels) {
			return profilestore.ErrUnavailable
		}
		switch container.State {
		case "exited", "dead", "created":
		default:
			return profilestore.ErrBusy
		}
	}
	return nil
}
