package upgrade

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func fixture(data []byte) Manifest {
	sum := sha256.Sum256(data)
	m := Manifest{SchemaVersion: 1, Version: "v1.2.3", CLIVersion: "v1.2.3+git.0123456789ab", APIMajor: 1, StateSchema: 1, CLI: Binary{"vpnmodo-darwin-arm64", hex.EncodeToString(sum[:]), int64(len(data))}}
	m.Runtime = Runtime{1, m.Version, m.CLIVersion, "linux", "arm64", map[string]string{}}
	for _, role := range []string{"controller", "l2tp", "xauth", "application"} {
		m.Runtime.Images[role] = "ghcr.io/" + Repository + "/" + role + "@sha256:" + strings.Repeat("a", 64)
	}
	m.Runtime.Images["application"] = "ghcr.io/" + Repository + "/vpntools@sha256:" + strings.Repeat("a", 64)
	return m
}
func TestReleaseIntegrityAndCompatibility(t *testing.T) {
	ctx := context.Background()
	binary := []byte("synthetic binary")
	m := fixture(binary)
	manifest, _ := json.Marshal(m)
	sum := sha256.Sum256(manifest)
	r := Release{Tag: m.Version, Assets: []Asset{{1, "upgrade-darwin-arm64.json", int64(len(manifest)), "sha256:" + hex.EncodeToString(sum[:])}, {2, m.CLI.Asset, int64(len(binary)), "sha256:" + m.CLI.SHA256}}}
	tamper := false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		if req.Header.Get("Authorization") != "Bearer synthetic" {
			t.Error("missing authentication")
		}
		switch {
		case strings.HasSuffix(req.URL.Path, "latest"):
			json.NewEncoder(w).Encode(r)
		case strings.HasSuffix(req.URL.Path, "assets/1"):
			w.Write(manifest)
		case strings.HasSuffix(req.URL.Path, "assets/2"):
			if tamper {
				fmt.Fprint(w, "corrupted binary")
			} else {
				w.Write(binary)
			}
		default:
			t.Error("unexpected request")
			w.WriteHeader(404)
		}
	}))
	defer server.Close()
	c := NewClient("synthetic")
	c.Base = server.URL
	c.HTTP = server.Client()
	release, err := c.Resolve(ctx, "", false)
	if err != nil {
		t.Fatal(err)
	}
	selected, err := c.Manifest(ctx, release, "darwin", "arm64")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = c.Binary(ctx, release, selected); err != nil {
		t.Fatal(err)
	}
	tamper = true
	if _, err = c.Binary(ctx, release, selected); err == nil {
		t.Fatal("accepted corrupted asset")
	}
	r.Assets[0].Digest = ""
	if _, err = c.Manifest(ctx, r, "darwin", "arm64"); err == nil {
		t.Fatal("accepted missing API digest")
	}
	for _, change := range []func(*Manifest){func(m *Manifest) { m.StateSchema = 2 }, func(m *Manifest) { m.APIMajor = 2 }, func(m *Manifest) { m.Runtime.Images["xauth"] = "ghcr.io/example/xauth:latest" }, func(m *Manifest) { m.Runtime.BinaryVersion = "other" }} {
		value := fixture(binary)
		change(&value)
		if value.Validate("darwin", "arm64") == nil {
			t.Fatal("accepted incompatible manifest")
		}
	}
}
func TestRedirectAuthAndScheme(t *testing.T) {
	c := NewClient("synthetic")
	req, _ := http.NewRequest("GET", "https://release-assets.githubusercontent.com/a", nil)
	req.Header.Set("Authorization", "Bearer synthetic")
	if err := c.HTTP.CheckRedirect(req, nil); err != nil || req.Header.Get("Authorization") != "" {
		t.Fatal("forwarded API token", err)
	}
	for _, target := range []string{"http://api.github.com/a", "https://example.com/a"} {
		req, _ = http.NewRequest("GET", target, nil)
		if c.HTTP.CheckRedirect(req, nil) == nil {
			t.Fatal("unsafe redirect")
		}
	}
}

func TestIKEv2RuntimeImageRoleIsImmutableAndOptionalForOldReleases(t *testing.T) {
	m := fixture([]byte("synthetic binary"))
	if m.Validate("darwin", "arm64") != nil {
		t.Fatal("legacy release refused")
	}
	m.Runtime.Images["ikev2"] = "ghcr.io/" + Repository + "/ikev2@sha256:" + strings.Repeat("b", 64)
	if m.Validate("darwin", "arm64") != nil {
		t.Fatal("IKEv2 release refused")
	}
	m.Runtime.Images["ikev2"] = "ghcr.io/" + Repository + "/ikev2:mutable"
	if m.Validate("darwin", "arm64") == nil {
		t.Fatal("mutable IKEv2 image accepted")
	}
}

func TestVPNToolsImageReferenceValidation(t *testing.T) {
	for _, tc := range []struct {
		ref string
		ok  bool
	}{
		{"ghcr.io/" + Repository + "/vpntools@sha256:" + strings.Repeat("b", 64), true},
		{"ghcr.io/" + Repository + "/application@sha256:" + strings.Repeat("b", 64), true},
		{"ghcr.io/" + Repository + "/vpntools:latest", false},
		{"ghcr.io/" + Repository + "/controller@sha256:" + strings.Repeat("b", 64), false},
		{"ghcr.io/other/project/vpntools@sha256:" + strings.Repeat("b", 64), false},
	} {
		t.Run(tc.ref, func(t *testing.T) {
			m := fixture([]byte("synthetic binary"))
			m.Runtime.Images["application"] = tc.ref
			if got := m.Validate("darwin", "arm64") == nil; got != tc.ok {
				t.Fatalf("valid = %t, want %t", got, tc.ok)
			}
		})
	}
}
