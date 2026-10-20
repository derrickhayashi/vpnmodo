// Package upgrade verifies repository release assets before installing code.
package upgrade

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/derrickhayashi/vpn-sidecars-next/internal/jsonstrict"
)

var ErrRelease = errors.New("upgrade release unavailable or invalid; check the version, release assets and repository access")
var tagPattern = regexp.MustCompile(`^v[0-9]+\.[0-9]+\.[0-9]+(?:-[0-9A-Za-z]+(?:[.-][0-9A-Za-z]+)*)?$`)
var digestPattern = regexp.MustCompile(`^sha256:[a-f0-9]{64}$`)

const Repository = "derrickhayashi/vpn-sidecars-next"
const MaxBinary = 64 * 1024 * 1024

type Runtime struct {
	SchemaVersion int               `json:"schema_version"`
	Version       string            `json:"version"`
	BinaryVersion string            `json:"binary_version"`
	OS            string            `json:"os"`
	Arch          string            `json:"arch"`
	Images        map[string]string `json:"images"`
}
type Binary struct {
	Asset  string `json:"asset"`
	SHA256 string `json:"sha256"`
	Size   int64  `json:"size"`
}
type Manifest struct {
	SchemaVersion int     `json:"schema_version"`
	Version       string  `json:"version"`
	CLIVersion    string  `json:"cli_version"`
	APIMajor      int     `json:"api_major"`
	StateSchema   int     `json:"state_schema"`
	CLI           Binary  `json:"cli"`
	Runtime       Runtime `json:"runtime"`
}

func (m Manifest) Validate(osname, arch string) error {
	if m.SchemaVersion != 1 || !tagPattern.MatchString(m.Version) || !strings.HasPrefix(m.CLIVersion, m.Version+"+git.") || len(m.CLIVersion) > 128 || m.APIMajor != 1 || m.StateSchema != 1 {
		return ErrRelease
	}
	if osname != "darwin" || arch != "arm64" || m.CLI.Asset != "vpnmodo-"+osname+"-"+arch || !digestPattern.MatchString("sha256:"+m.CLI.SHA256) || m.CLI.Size <= 0 || m.CLI.Size > MaxBinary {
		return ErrRelease
	}
	r := m.Runtime
	if r.SchemaVersion != 1 || r.Version != m.Version || r.BinaryVersion != m.CLIVersion || !tagPattern.MatchString(r.Version) || r.BinaryVersion == "" || len(r.BinaryVersion) > 128 || r.OS != "linux" || r.Arch != arch || len(r.Images) != 4 {
		return ErrRelease
	}
	for _, role := range []string{"controller", "l2tp", "xauth", "application"} {
		ref := r.Images[role]
		prefix := "ghcr.io/" + Repository + "/" + role + "@"
		if !strings.HasPrefix(ref, prefix) || !digestPattern.MatchString(strings.TrimPrefix(ref, prefix)) {
			return ErrRelease
		}
	}
	return nil
}

type Asset struct {
	ID     int64  `json:"id"`
	Name   string `json:"name"`
	Size   int64  `json:"size"`
	Digest string `json:"digest"`
}
type Release struct {
	Tag        string  `json:"tag_name"`
	Draft      bool    `json:"draft"`
	Prerelease bool    `json:"prerelease"`
	Assets     []Asset `json:"assets"`
}
type Client struct {
	HTTP  *http.Client
	Base  string
	Token string
}

func NewClient(token string) *Client {
	return &Client{Base: "https://api.github.com", Token: token, HTTP: &http.Client{Timeout: 2 * time.Minute, CheckRedirect: func(req *http.Request, via []*http.Request) error {
		if len(via) > 5 || req.URL.Scheme != "https" {
			return ErrRelease
		}
		host := req.URL.Hostname()
		if host != "api.github.com" && host != "github.com" && !strings.HasSuffix(host, ".githubusercontent.com") {
			return ErrRelease
		}
		if host != "api.github.com" {
			req.Header.Del("Authorization")
		}
		return nil
	}}}
}
func (c *Client) get(ctx context.Context, path, accept string, limit int64) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.Base+path, nil)
	if err != nil {
		return nil, ErrRelease
	}
	req.Header.Set("Accept", accept)
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	req.Header.Set("User-Agent", "vpnmodo-upgrade")
	if c.Token != "" {
		req.Header.Set("Authorization", "Bearer "+c.Token)
	}
	response, err := c.HTTP.Do(req)
	if err != nil {
		return nil, ErrRelease
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return nil, ErrRelease
	}
	b, err := io.ReadAll(io.LimitReader(response.Body, limit+1))
	if err != nil || int64(len(b)) > limit {
		return nil, ErrRelease
	}
	return b, nil
}
func (c *Client) Resolve(ctx context.Context, tag string, prerelease bool) (Release, error) {
	path := "/repos/" + Repository + "/releases/latest"
	if tag != "" {
		if !tagPattern.MatchString(tag) {
			return Release{}, ErrRelease
		}
		path = "/repos/" + Repository + "/releases/tags/" + url.PathEscape(tag)
	} else if prerelease {
		path = "/repos/" + Repository + "/releases?per_page=100"
	}
	b, err := c.get(ctx, path, "application/vnd.github+json", 2*1024*1024)
	if err != nil {
		return Release{}, err
	}
	var r Release
	if tag == "" && prerelease {
		var list []Release
		if json.Unmarshal(b, &list) != nil {
			return r, ErrRelease
		}
		for _, item := range list {
			if !item.Draft && tagPattern.MatchString(item.Tag) {
				r = item
				break
			}
		}
	} else if json.Unmarshal(b, &r) != nil {
		return r, ErrRelease
	}
	if r.Draft || !tagPattern.MatchString(r.Tag) || tag != "" && r.Tag != tag || r.Prerelease && !prerelease && tag == "" {
		return Release{}, ErrRelease
	}
	return r, nil
}
func asset(r Release, name string) (Asset, error) {
	var found Asset
	for _, a := range r.Assets {
		if a.Name == name {
			if found.ID != 0 {
				return Asset{}, ErrRelease
			}
			found = a
		}
	}
	if found.ID <= 0 || !digestPattern.MatchString(found.Digest) || found.Size <= 0 {
		return Asset{}, ErrRelease
	}
	return found, nil
}
func (c *Client) download(ctx context.Context, a Asset, limit int64) ([]byte, error) {
	if a.Size > limit {
		return nil, ErrRelease
	}
	b, err := c.get(ctx, "/repos/"+Repository+"/releases/assets/"+strconv.FormatInt(a.ID, 10), "application/octet-stream", limit)
	if err != nil {
		return nil, err
	}
	sum := sha256.Sum256(b)
	if int64(len(b)) != a.Size || "sha256:"+hex.EncodeToString(sum[:]) != a.Digest {
		return nil, ErrRelease
	}
	return b, nil
}
func (c *Client) Manifest(ctx context.Context, r Release, osname, arch string) (Manifest, error) {
	var m Manifest
	a, err := asset(r, "upgrade-"+osname+"-"+arch+".json")
	if err != nil {
		return m, err
	}
	b, err := c.download(ctx, a, 16384)
	if err != nil {
		return m, err
	}
	if jsonstrict.Decode(b, &m, 16384) != nil || m.Version != r.Tag || m.Validate(osname, arch) != nil {
		return m, ErrRelease
	}
	return m, nil
}
func (c *Client) Binary(ctx context.Context, r Release, m Manifest) ([]byte, error) {
	a, err := asset(r, m.CLI.Asset)
	if err != nil {
		return nil, err
	}
	if a.Size != m.CLI.Size || a.Digest != "sha256:"+m.CLI.SHA256 {
		return nil, ErrRelease
	}
	return c.download(ctx, a, MaxBinary)
}
func ValidateTag(tag string) error {
	if !tagPattern.MatchString(tag) {
		return fmt.Errorf("invalid release version")
	}
	return nil
}

var tokenPattern = regexp.MustCompile(`^[0-9a-f]{32}$`)

func matches(data []byte, b Binary) bool {
	sum := sha256.Sum256(data)
	return int64(len(data)) == b.Size && hex.EncodeToString(sum[:]) == b.SHA256
}
