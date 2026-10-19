package l2tp

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"strings"
	"time"

	"github.com/derrickhayashi/vpn-sidecars-next/internal/jsonstrict"
	"github.com/derrickhayashi/vpn-sidecars-next/internal/lifecycle"
	"github.com/derrickhayashi/vpn-sidecars-next/internal/operation"
	"github.com/derrickhayashi/vpn-sidecars-next/internal/privatefile"
	"github.com/derrickhayashi/vpn-sidecars-next/internal/profile"
	"github.com/derrickhayashi/vpn-sidecars-next/internal/profilestore"
	"github.com/derrickhayashi/vpn-sidecars-next/internal/secret"
)

var imagePattern = regexp.MustCompile(`^[a-z0-9][a-z0-9._/:@-]{0,254}$`)
var tokenPattern = regexp.MustCompile(`^[a-f0-9]{32}$`)
var namespacePattern = regexp.MustCompile(`^net:\[[0-9]+\]$`)
var containerPattern = regexp.MustCompile(`^[a-f0-9]{64}$`)

type Adapter struct {
	root, image, application string
	store                    *profilestore.Store
	docker                   Runner
}

func New(root, image, application string, store *profilestore.Store, docker Runner) (*Adapter, error) {
	if image == "" {
		image = DefaultImage
	}
	if application == "" {
		application = DefaultApplicationImage
	}
	if !imagePattern.MatchString(image) || !imagePattern.MatchString(application) || store == nil || docker == nil || privatefile.Dir(root) != nil {
		return nil, ErrRuntime
	}
	return &Adapter{root, image, application, store, docker}, nil
}
func (a *Adapter) dir(p profile.Profile) string { return filepath.Join(a.root, p.Name) }
func (a *Adapter) valid(p profile.Profile) error {
	if p.Validate() != nil || p.Backend.Type != profile.L2TP {
		return lifecycle.ErrUnavailable
	}
	return nil
}
func (a *Adapter) saved(p profile.Profile) (*deployment, error) {
	if err := a.valid(p); err != nil {
		return nil, err
	}
	dir := a.dir(p)
	if privatefile.Dir(dir) != nil {
		return nil, ErrRuntime
	}
	data, err := privatefile.Read(filepath.Join(dir, "deployment.json"), 2*profile.MaxProfileBytes)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, ErrRuntime
	}
	var d deployment
	if jsonstrict.Decode(data, &d, 2*profile.MaxProfileBytes) != nil || d.SchemaVersion != 1 || d.Revision == 0 || d.Profile.Name != p.Name || d.Profile.Validate() != nil || d.Profile.Backend.Type != profile.L2TP || !tokenPattern.MatchString(d.Token) || !imagePattern.MatchString(d.VPNImage) || !imagePattern.MatchString(d.ApplicationImage) {
		return nil, ErrRuntime
	}
	return &d, nil
}

type container struct {
	ID     string `json:"Id"`
	Config struct {
		Labels map[string]string
		Image  string
	}
	State struct {
		Running, Paused, Restarting, OOMKilled bool
		Status                                 string
		ExitCode                               int
		StartedAt                              time.Time
	}
}

// Query the whole Compose project, including unlabeled/foreign services, and all
// uses of this profile label. Filtering by managed labels alone could hide a
// competing watchdog. No mutation adopts a container discovered this way.
func (a *Adapter) owned(ctx context.Context, p profile.Profile) (*deployment, map[string]container, error) {
	d, err := a.saved(p)
	if err != nil {
		return nil, nil, err
	}
	id, _ := profile.Identity(p.Name)
	ids := map[string]bool{}
	for _, filter := range []string{"label=com.docker.compose.project=" + id.Project, "label=" + profile.ProfileLabel + "=" + p.Name} {
		raw, err := a.docker.Run(ctx, "ps", "--all", "--no-trunc", "--filter", filter, "--format", "{{.ID}}")
		if err != nil {
			return nil, nil, ErrRuntime
		}
		for _, v := range strings.Fields(string(raw)) {
			if !containerPattern.MatchString(v) {
				return nil, nil, ErrRuntime
			}
			ids[v] = true
		}
	}
	out := map[string]container{}
	for cid := range ids {
		raw, err := a.docker.Run(ctx, "inspect", cid)
		if err != nil {
			return nil, nil, ErrRuntime
		}
		var values []container
		if json.Unmarshal(raw, &values) != nil || len(values) != 1 || values[0].ID != cid {
			return nil, nil, ErrRuntime
		}
		c := values[0]
		labels := c.Config.Labels
		service := labels["com.docker.compose.service"]
		if d == nil || !id.Matches(labels["com.docker.compose.project"], labels) || (service != "vpn" && service != "application") || labels[backendLabel] != string(profile.L2TP) || labels[revisionLabel] != d.labels()[revisionLabel] || out[service].ID != "" {
			return nil, nil, ErrOwnership
		}
		expected := d.VPNImage
		if service == "application" {
			expected = d.ApplicationImage
		}
		if c.Config.Image != expected {
			return nil, nil, ErrOwnership
		}
		out[service] = c
	}
	raw, err := a.docker.Run(ctx, "network", "ls", "--filter", "name=^"+id.Project+"_default$", "--format", "{{.Name}}")
	if err != nil {
		return nil, nil, ErrRuntime
	}
	if strings.TrimSpace(string(raw)) != "" {
		if strings.TrimSpace(string(raw)) != id.Project+"_default" {
			return nil, nil, ErrOwnership
		}
		raw, err = a.docker.Run(ctx, "network", "inspect", id.Project+"_default")
		if err != nil {
			return nil, nil, ErrRuntime
		}
		var nets []struct{ Labels map[string]string }
		if json.Unmarshal(raw, &nets) != nil || len(nets) != 1 || !id.Matches(nets[0].Labels["com.docker.compose.project"], nets[0].Labels) || nets[0].Labels[backendLabel] != string(profile.L2TP) {
			return nil, nil, ErrOwnership
		}
	}
	return d, out, nil
}
func (a *Adapter) exec(ctx context.Context, c container, command ...string) ([]byte, error) {
	if !c.State.Running || c.State.Paused || c.State.Restarting {
		return nil, ErrRuntime
	}
	args := append([]string{"exec", c.ID, "timeout", "6"}, command...)
	return a.docker.Run(ctx, args...)
}
func (a *Adapter) tunnel(ctx context.Context, c container) (bool, error) {
	_, err := a.exec(ctx, c, "/usr/local/bin/vpn-tunnelcheck")
	if err == nil {
		return true, nil
	}
	var exit commandError
	if errors.As(err, &exit) && exit.code == 1 {
		return false, nil
	}
	return false, ErrRuntime
}
func (a *Adapter) Observe(ctx context.Context, p profile.Profile) (bool, error) {
	_, cs, err := a.owned(ctx, p)
	if err != nil {
		return false, err
	}
	c := cs["vpn"]
	if c.ID == "" || !c.State.Running {
		return false, nil
	}
	return a.tunnel(ctx, c)
}
func (a *Adapter) Verify(ctx context.Context, p profile.Profile) error {
	_, cs, err := a.owned(ctx, p)
	if err != nil {
		return err
	}
	if ok, err := a.tunnel(ctx, cs["vpn"]); err != nil || !ok {
		return ErrRuntime
	}
	if _, err = a.exec(ctx, cs["vpn"], "/usr/local/bin/vpn-servicecheck"); err != nil {
		return ErrRuntime
	}
	return nil
}
func (a *Adapter) Check(ctx context.Context, p profile.Profile) (operation.HealthState, error) {
	d, cs, err := a.owned(ctx, p)
	if errors.Is(err, ErrOwnership) {
		return operation.HealthOwnerConflict, nil
	}
	if err != nil {
		return "", err
	}
	c := cs["vpn"]
	if c.ID == "" {
		if d == nil {
			return operation.HealthStopped, nil
		}
		return operation.HealthTunnelDown, nil
	}
	if c.State.Paused || c.State.Restarting {
		return operation.HealthUnavailable, nil
	}
	if !c.State.Running {
		if c.State.ExitCode == 0 && !c.State.OOMKilled {
			return operation.HealthStopped, nil
		}
		return operation.HealthTunnelDown, nil
	}
	ok, err := a.tunnel(ctx, c)
	if err != nil {
		return "", err
	}
	if !ok {
		_, ready := a.exec(ctx, c, "test", "-f", "/run/vpn/ready")
		if ready != nil && time.Since(c.State.StartedAt) < 150*time.Second {
			return operation.HealthStarting, nil
		}
		return operation.HealthTunnelDown, nil
	}
	if _, err = a.exec(ctx, c, "/usr/local/bin/vpn-servicecheck"); err != nil {
		return operation.HealthServiceUnavailable, nil
	}
	app := cs["application"]
	if app.ID == "" {
		return operation.HealthDetached, nil
	}
	if !app.State.Running {
		if app.State.ExitCode == 0 && !app.State.OOMKilled {
			return operation.HealthStopped, nil
		}
		return operation.HealthDetached, nil
	}
	vpnNet, err := a.exec(ctx, c, "readlink", "/proc/self/ns/net")
	if err != nil {
		return "", ErrRuntime
	}
	appNet, err := a.exec(ctx, app, "readlink", "/proc/self/ns/net")
	if err != nil {
		return "", ErrRuntime
	}
	if !namespacePattern.MatchString(strings.TrimSpace(string(vpnNet))) || !namespacePattern.MatchString(strings.TrimSpace(string(appNet))) {
		return "", ErrRuntime
	}
	if string(vpnNet) != string(appNet) {
		return operation.HealthDetached, nil
	}
	return operation.HealthHealthy, nil
}
func (a *Adapter) CanRecover(p profile.Profile) bool { return p.Backend.Type == profile.L2TP }
func (a *Adapter) Quiesce(ctx context.Context, p profile.Profile) error {
	_, cs, err := a.owned(ctx, p)
	if err != nil {
		return err
	}
	if c := cs["application"]; c.ID != "" {
		_, err = a.docker.Run(ctx, "rm", "--force", c.ID)
	}
	return err
}
func (a *Adapter) Disconnect(ctx context.Context, p profile.Profile) error {
	if err := a.Quiesce(ctx, p); err != nil {
		return err
	}
	_, cs, err := a.owned(ctx, p)
	if err != nil {
		return err
	}
	if c := cs["vpn"]; c.ID != "" {
		if c.State.Running {
			if _, err = a.docker.Run(ctx, "stop", "--time", "2", c.ID); err != nil {
				return ErrRuntime
			}
		}
		if _, err = a.docker.Run(ctx, "rm", c.ID); err != nil {
			return ErrRuntime
		}
	}
	// A retained nonsecret deployment marker distinguishes a lost managed VPN
	// from a profile that has never been started. Intent lives in the journal.
	return a.removeCredentials(p)
}
func (a *Adapter) removeCredentials(p profile.Profile) error {
	root := filepath.Join(a.dir(p), "credentials")
	if _, err := os.Lstat(root); errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if privatefile.Dir(root) != nil {
		return ErrRuntime
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		return ErrRuntime
	}
	for _, e := range entries {
		if !tokenPattern.MatchString(e.Name()) {
			return ErrRuntime
		}
		dir := filepath.Join(root, e.Name())
		if privatefile.Dir(dir) != nil {
			return ErrRuntime
		}
		files, err := os.ReadDir(dir)
		if err != nil {
			return ErrRuntime
		}
		for _, f := range files {
			if f.Name() != "username" && f.Name() != "password" && f.Name() != "psk" {
				return ErrRuntime
			}
			path := filepath.Join(dir, f.Name())
			if _, err = privatefile.Read(path, 8192); err != nil {
				return ErrRuntime
			}
			if os.Remove(path) != nil {
				return ErrRuntime
			}
		}
		if os.Remove(dir) != nil {
			return ErrRuntime
		}
	}
	return privatefile.SyncDir(root)
}
func (a *Adapter) prepare(ctx context.Context, p profile.Profile) (*deployment, error) {
	var out deployment
	err := a.store.WithCredentials(ctx, p.Name, func(snapshot profile.Snapshot, c profile.Credentials) error {
		if !reflect.DeepEqual(snapshot.Profile, p) {
			return ErrRuntime
		}
		token, err := privatefile.Token()
		if err != nil {
			return ErrRuntime
		}
		root := filepath.Join(a.dir(p), "credentials")
		if privatefile.Dir(root) != nil {
			return ErrRuntime
		}
		dir := filepath.Join(root, token)
		if privatefile.Dir(dir) != nil {
			return ErrRuntime
		}
		for key, value := range map[string]secret.Value{"username": c.Username, "password": c.Password, "psk": c.PSK} {
			if _, err := privatefile.Write(filepath.Join(dir, key), value.BytesForProtectedFile()); err != nil {
				return ErrRuntime
			}
		}
		if _, err := privatefile.Write(filepath.Join(a.dir(p), "resolv.conf"), []byte("nameserver 127.0.0.1\noptions ndots:0\n")); err != nil {
			return ErrRuntime
		}
		out = deployment{1, p, snapshot.Revision, token, a.image, a.application}
		data, err := manifest(out, a.dir(p))
		if err != nil {
			return ErrRuntime
		}
		if _, err = privatefile.Write(filepath.Join(a.dir(p), "compose.json"), data); err != nil {
			return ErrRuntime
		}
		data, _ = json.Marshal(out)
		_, err = privatefile.Write(filepath.Join(a.dir(p), "deployment.json"), data)
		return err
	})
	if err != nil {
		if errors.Is(err, profile.ErrCredentials) {
			return nil, lifecycle.ErrCredentials
		}
		return nil, ErrRuntime
	}
	return &out, nil
}
func (a *Adapter) compose(ctx context.Context, p profile.Profile, service string) error {
	id, _ := profile.Identity(p.Name)
	_, err := a.docker.Run(ctx, "compose", "--project-name", id.Project, "--project-directory", a.dir(p), "--env-file", "/dev/null", "-f", filepath.Join(a.dir(p), "compose.json"), "up", "-d", "--no-deps", "--pull", "never", "--force-recreate", service)
	if err != nil {
		return ErrRuntime
	}
	return nil
}
func (a *Adapter) Connect(ctx context.Context, p profile.Profile, _ func(context.Context) (secret.Value, error)) error {
	d, cs, err := a.owned(ctx, p)
	if err != nil {
		return err
	}
	// Already-starting sessions get their remaining bounded startup time, not a
	// parallel login. Healthy sessions are handled by Observe/Verify instead.
	if c := cs["vpn"]; c.ID != "" && c.State.Running {
		if d == nil || !reflect.DeepEqual(d.Profile, p) {
			return ErrOwnership
		}
		health, err := a.Check(ctx, p)
		if err != nil {
			return err
		}
		if health != operation.HealthStarting {
			return ErrRuntime
		}
	} else {
		for _, image := range []string{a.image, a.application} {
			if _, err = a.docker.Run(ctx, "image", "inspect", image); err != nil {
				return ErrRuntime
			}
		}
		if err = a.Disconnect(ctx, p); err != nil {
			return err
		}
		if _, err = a.prepare(ctx, p); err != nil {
			return err
		}
		if _, _, err = a.owned(ctx, p); err != nil {
			return err
		}
		if err = a.compose(ctx, p, "vpn"); err != nil {
			return err
		}
	}
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		_, cs, err = a.owned(ctx, p)
		if err != nil {
			return err
		}
		c := cs["vpn"]
		if c.ID == "" || !c.State.Running {
			return ErrRuntime
		}
		if ok, err := a.tunnel(ctx, c); err != nil {
			return err
		} else if ok {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
	}
}
func (a *Adapter) RepairForwarding(ctx context.Context, p profile.Profile) error {
	health, err := a.Check(ctx, p)
	if err != nil {
		return err
	}
	if health == operation.HealthHealthy {
		return nil
	}
	if err = a.Quiesce(ctx, p); err != nil {
		return err
	}
	succeeded := false
	defer func() {
		if !succeeded {
			cleanup, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			_ = a.Quiesce(cleanup, p)
		}
	}()
	if err = a.Verify(ctx, p); err != nil {
		return err
	}
	if err = a.compose(ctx, p, "application"); err != nil {
		return err
	}
	health, err = a.Check(ctx, p)
	if err == nil && health == operation.HealthHealthy {
		succeeded = true
		return nil
	}
	return ErrRuntime
}

func (a *Adapter) Supports(b profile.BackendType) bool { return b == profile.L2TP }

// Forget is called only under the inactive profile-deletion transaction. It does
// not reenter the profile store or infer ownership from names alone.
func (a *Adapter) Forget(ctx context.Context, name string) error {
	if !profile.ValidName(name) {
		return ErrRuntime
	}
	root := filepath.Join(a.root, name)
	data, err := privatefile.Read(filepath.Join(root, "deployment.json"), 2*profile.MaxProfileBytes)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return ErrRuntime
	}
	var d deployment
	if json.Unmarshal(data, &d) != nil || d.Profile.Name != name || d.Profile.Validate() != nil {
		return ErrRuntime
	}
	_, cs, err := a.owned(ctx, d.Profile)
	if err != nil {
		return err
	}
	if len(cs) != 0 {
		return ErrOwnership
	}
	id, _ := profile.Identity(name)
	raw, err := a.docker.Run(ctx, "network", "ls", "--filter", "name=^"+id.Project+"_default$", "--format", "{{.Name}}")
	if err != nil {
		return ErrRuntime
	}
	if strings.TrimSpace(string(raw)) != "" {
		if _, err = a.docker.Run(ctx, "network", "rm", id.Project+"_default"); err != nil {
			return ErrRuntime
		}
	}
	if err = a.removeCredentials(d.Profile); err != nil {
		return err
	}
	for _, filename := range []string{"compose.json", "deployment.json", "resolv.conf"} {
		path := filepath.Join(root, filename)
		if _, err := privatefile.Read(path, 2*profile.MaxProfileBytes); err != nil && !errors.Is(err, os.ErrNotExist) {
			return ErrRuntime
		}
		if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
			return ErrRuntime
		}
	}
	if err = os.Remove(filepath.Join(root, "credentials")); err != nil && !errors.Is(err, os.ErrNotExist) {
		return ErrRuntime
	}
	if os.Remove(root) != nil {
		return ErrRuntime
	}
	return privatefile.SyncDir(a.root)
}
