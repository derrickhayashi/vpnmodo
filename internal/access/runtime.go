package access

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/derrickhayashi/vpnmodo/internal/host"
	"github.com/derrickhayashi/vpnmodo/internal/jsonstrict"
	"github.com/derrickhayashi/vpnmodo/internal/privatefile"
	"github.com/derrickhayashi/vpnmodo/internal/profile"
)

type Descriptor struct {
	Alias   string            `json:"alias"`
	Service Service           `json:"service"`
	Host    host.Host         `json:"vm"`
	Request host.ProxyRequest `json:"request"`
}
type state struct {
	Alias     string  `json:"alias"`
	Service   Service `json:"service"`
	Directory string  `json:"directory"`
}
type Result struct {
	ApplicationID       string `json:"-"`
	Endpoint            string `json:"endpoint,omitempty"`
	Name                string `json:"name"`
	Code                string `json:"code"`
	LocalPort           int    `json:"local_port"`
	ListenerReady       bool   `json:"listener_ready"`
	DestinationSSHReady bool   `json:"destination_ssh_ready"`
	RemoteServiceReady  bool   `json:"remote_service_ready"`
}

var tokenPattern = regexp.MustCompile(`^[a-f0-9]{32}$`)
var containerIDPattern = regexp.MustCompile(`^[a-f0-9]{64}$`)

func statePath(registry, alias string) string {
	return filepath.Join(registry+".access", alias+".state.json")
}
func checkDir(path string) error {
	prefix := "/tmp/vpnmodo-" + strconv.Itoa(os.Geteuid()) + "-"
	if !strings.HasPrefix(path, prefix) || !tokenPattern.MatchString(strings.TrimPrefix(path, prefix)) {
		return ErrConfig
	}
	i, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if !i.IsDir() || i.Mode().Perm()&0077 != 0 || !privatefile.Owned(i) {
		return ErrConfig
	}
	return nil
}
func readStates(registry, alias string) ([]state, error) {
	if !profile.ValidName(alias) {
		return nil, ErrConfig
	}
	b, err := privatefile.Read(statePath(registry, alias), 128*1024)
	if errors.Is(err, os.ErrNotExist) {
		return []state{}, nil
	}
	if err != nil {
		return nil, ErrConfig
	}
	var states []state
	if jsonstrict.Decode(b, &states, 128*1024) != nil || states == nil || len(states) > 32 {
		return nil, ErrConfig
	}
	seen := map[string]bool{}
	for _, s := range states {
		if s.Alias != alias || s.Service.Validate() != nil || seen[s.Service.Name] {
			return nil, ErrConfig
		}
		seen[s.Service.Name] = true
		if err := checkDir(s.Directory); err != nil && !errors.Is(err, os.ErrNotExist) {
			return nil, ErrConfig
		}
	}
	return states, nil
}
func saveStates(registry, alias string, states []state) error {
	if states == nil {
		states = []state{}
	}
	b, _ := json.Marshal(states)
	if len(b) > 128*1024 {
		return ErrConfig
	}
	_, err := privatefile.Write(statePath(registry, alias), b)
	return err
}
func shellQuote(s string) string { return "'" + strings.ReplaceAll(s, "'", "'\"'\"'") + "'" }
func boundedRun(ctx context.Context, args []string, input []byte) ([]byte, error) {
	child, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(child, "ssh", args...)
	cmd.WaitDelay = time.Second
	cmd.Stderr = io.Discard
	if input != nil {
		cmd.Stdin = bytes.NewReader(input)
	}
	var out cappedOutput
	cmd.Stdout = &out
	err := cmd.Run()
	return out.Bytes(), err
}

type cappedOutput struct{ bytes.Buffer }

func (b *cappedOutput) Write(p []byte) (int, error) {
	if b.Len()+len(p) > 64*1024 {
		return 0, ErrConfig
	}
	return b.Buffer.Write(p)
}
func startMaster(ctx context.Context, args []string) error {
	child, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	null, err := os.OpenFile(os.DevNull, os.O_RDWR, 0)
	if err != nil {
		return err
	}
	defer null.Close()
	cmd := exec.CommandContext(child, "ssh", args...)
	// A detached master must inherit real /dev/null FDs, not os/exec capture pipes
	// that would keep Run waiting for EOF for the lifetime of the forward.
	cmd.Stdout = null
	cmd.Stderr = null
	cmd.Stdin = null
	return cmd.Run()
}
func control(ctx context.Context, s state, action string, extra ...string) error {
	args := []string{"-T", "-F", "/dev/null", "-S", filepath.Join(s.Directory, "s"), "-o", "BatchMode=yes", "-o", "StrictHostKeyChecking=yes", "-o", "ForwardAgent=no", "-o", "ExitOnForwardFailure=yes", "-O", action}
	args = append(args, extra...)
	args = append(args, "--", s.Service.SSHTarget)
	_, err := boundedRun(ctx, args, nil)
	return err
}
func portAvailable(port int) bool {
	l, e := net.Listen("tcp4", net.JoinHostPort("127.0.0.1", strconv.Itoa(port)))
	if e != nil {
		return false
	}
	l.Close()
	return true
}
func cleanup(s state) error {
	if err := checkDir(s.Directory); errors.Is(err, os.ErrNotExist) {
		return nil
	} else if err != nil {
		return err
	}
	// Never recursively delete an unchecked path. An unknown entry preserves state
	// for inspection instead of deleting unrelated data or a live control socket.
	entries, err := os.ReadDir(s.Directory)
	if err != nil {
		return err
	}
	for _, e := range entries {
		if e.Name() != "proxy.json" {
			return ErrConfig
		}
	}
	if _, err = privatefile.Read(filepath.Join(s.Directory, "proxy.json"), 8192); err != nil {
		return err
	}
	if os.Remove(filepath.Join(s.Directory, "proxy.json")) != nil {
		return ErrConfig
	}
	return os.Remove(s.Directory)
}
func stopOne(ctx context.Context, s state) error {
	if checkDir(s.Directory) == nil {
		descriptor, err := ReadDescriptor(filepath.Join(s.Directory, "proxy.json"))
		if err != nil || descriptor.Alias != s.Alias || descriptor.Service != s.Service {
			return ErrConfig
		}
		if control(ctx, s, "check") == nil {
			if control(ctx, s, "exit") != nil {
				return ErrConfig
			}
			deadline := time.Now().Add(3 * time.Second)
			for time.Now().Before(deadline) {
				if _, err := os.Lstat(filepath.Join(s.Directory, "s")); errors.Is(err, os.ErrNotExist) {
					break
				}
				select {
				case <-ctx.Done():
					return ctx.Err()
				case <-time.After(30 * time.Millisecond):
				}
			}
		}
		path := filepath.Join(s.Directory, "s")
		if _, err := os.Lstat(path); err == nil {
			// A crashed master can leave a stale Unix socket. Only ECONNREFUSED proves
			// this socket is stale; an unresponsive live master is never killed by PID.
			conn, e := net.DialTimeout("unix", path, 300*time.Millisecond)
			if e == nil {
				conn.Close()
				return ErrConfig
			}
			if !errors.Is(e, os.ErrNotExist) && !errors.Is(e, syscall.ECONNREFUSED) {
				return ErrConfig
			}
			if os.Remove(path) != nil {
				return ErrConfig
			}
		}
	}
	return cleanup(s)
}

// Start leaves successful services running if another selected service fails.
func Start(ctx context.Context, registry, alias string, h host.Host, ref host.Reference, selected, applicationID string) ([]Result, error) {
	if h.Validate() != nil || ref.Validate() != nil || ref.Name != alias || ref.Host != h.Name || !containerIDPattern.MatchString(applicationID) {
		return nil, ErrConfig
	}
	if privatefile.Dir(registry+".access") != nil {
		return nil, ErrConfig
	}
	unlock, err := privatefile.Lock(ctx, filepath.Join(registry+".access", alias+".lock"))
	if err != nil {
		return nil, err
	}
	defer unlock()
	d, err := Read(registry, alias)
	if err != nil {
		return nil, err
	}
	states, err := readStates(registry, alias)
	if err != nil {
		return nil, err
	}
	results := []Result{}
	found := selected == ""
	for _, svc := range d.Services {
		if selected != "" && selected != svc.Name {
			continue
		}
		found = true
		r := Result{Name: svc.Name, LocalPort: svc.LocalPort}
		var current *state
		for i := range states {
			if states[i].Service.Name == svc.Name {
				current = &states[i]
				break
			}
		}
		if current != nil {
			descriptor, e := ReadDescriptor(filepath.Join(current.Directory, "proxy.json"))
			expected, e2 := destination(ctx, svc)
			if e != nil || e2 != nil || current.Service != svc || descriptor.Alias != alias || descriptor.Service != svc || descriptor.Host != h || descriptor.Request.Profile != ref.Profile || descriptor.Request.Host != expected.Host || descriptor.Request.Port != expected.Port {
				r.Code = "service_binding_changed_stop_first"
				results = append(results, r)
				continue
			}
			if control(ctx, *current, "check") == nil {
				if descriptor.Request.ApplicationID != applicationID {
					r.Code = "service_binding_changed_stop_first"
					results = append(results, r)
					continue
				}
				results = append(results, inspectOne(ctx, *current))
				continue
			}
			if stopOne(ctx, *current) != nil {
				r.Code = "stop_failed"
				results = append(results, r)
				continue
			}
			for i := range states {
				if states[i].Service.Name == svc.Name {
					states = append(states[:i], states[i+1:]...)
					break
				}
			}
			if saveStates(registry, alias, states) != nil {
				return results, ErrConfig
			}
		}
		if !portAvailable(svc.LocalPort) {
			r.Code = "local_port_conflict"
			results = append(results, r)
			continue
		}
		request, err := destination(ctx, svc)
		if err != nil {
			r.Code = "destination_ssh_config_invalid"
			results = append(results, r)
			continue
		}
		request.Profile = ref.Profile
		request.ApplicationID = applicationID
		token, err := privatefile.Token()
		if err != nil {
			return results, err
		}
		dir := "/tmp/vpnmodo-" + strconv.Itoa(os.Geteuid()) + "-" + token
		if os.Mkdir(dir, 0700) != nil {
			return results, ErrConfig
		}
		s := state{Alias: alias, Service: svc, Directory: dir}
		data, _ := json.Marshal(Descriptor{Host: h, Request: request, Alias: alias, Service: svc})
		if _, err = privatefile.Write(filepath.Join(dir, "proxy.json"), data); err != nil {
			_ = os.Remove(dir)
			return results, ErrConfig
		}
		states = append(states, s)
		if saveStates(registry, alias, states) != nil {
			_ = cleanup(s)
			return results, ErrConfig
		}
		exe, err := os.Executable()
		if err != nil {
			return results, ErrConfig
		}
		proxy := strings.ReplaceAll(shellQuote(exe)+" __proxy "+shellQuote(filepath.Join(dir, "proxy.json")), "%", "%%")
		args := []string{"-f", "-N", "-T", "-M", "-S", filepath.Join(dir, "s"), "-o", "ControlPersist=no", "-o", "ClearAllForwardings=yes", "-o", "BatchMode=yes", "-o", "StrictHostKeyChecking=yes", "-o", "ForwardAgent=no", "-o", "PermitLocalCommand=no", "-o", "RemoteCommand=none", "-o", "ConnectTimeout=10", "-o", "ServerAliveInterval=15", "-o", "ServerAliveCountMax=3", "-o", "ProxyCommand=" + proxy}
		if svc.SSHConfig != "" {
			args = append(args, "-F", svc.SSHConfig)
		}
		args = append(args, "--", svc.SSHTarget)
		if err = startMaster(ctx, args); err != nil {
			r.Code = "destination_ssh_unavailable"
		} else {
			forward := fmt.Sprintf("127.0.0.1:%d:%s:%d", svc.LocalPort, svc.RemoteHost, svc.RemotePort)
			if control(ctx, s, "forward", "-L", forward) != nil {
				r.Code = "local_listener_unavailable"
			} else {
				results = append(results, inspectOne(ctx, s))
				continue
			}
		}
		if stopOne(ctx, s) == nil {
			states = states[:len(states)-1]
			if saveStates(registry, alias, states) != nil {
				return results, ErrConfig
			}
		}
		results = append(results, r)
	}
	if !found {
		return results, errors.New("named service is not configured")
	}
	return results, nil
}
func Stop(ctx context.Context, registry, alias, selected string) ([]Result, error) {
	if !profile.ValidName(alias) {
		return nil, ErrConfig
	}
	if privatefile.Dir(registry+".access") != nil {
		return nil, ErrConfig
	}
	unlock, err := privatefile.Lock(ctx, filepath.Join(registry+".access", alias+".lock"))
	if err != nil {
		return nil, err
	}
	defer unlock()
	states, err := readStates(registry, alias)
	if err != nil {
		return nil, err
	}
	remaining := []state{}
	results := []Result{}
	for _, s := range states {
		if selected != "" && s.Service.Name != selected {
			remaining = append(remaining, s)
			continue
		}
		r := Result{Name: s.Service.Name, LocalPort: s.Service.LocalPort, Code: "stopped"}
		if stopOne(ctx, s) != nil {
			r.Code = "stop_failed"
			remaining = append(remaining, s)
		}
		results = append(results, r)
	}
	if err = saveStates(registry, alias, remaining); err != nil {
		return results, err
	}
	return results, nil
}
func Status(ctx context.Context, registry, alias string) ([]Result, error) {
	if !profile.ValidName(alias) {
		return nil, ErrConfig
	}
	if privatefile.Dir(registry+".access") != nil {
		return nil, ErrConfig
	}
	unlock, err := privatefile.Lock(ctx, filepath.Join(registry+".access", alias+".lock"))
	if err != nil {
		return nil, err
	}
	defer unlock()
	d, err := Read(registry, alias)
	if err != nil {
		return nil, err
	}
	states, err := readStates(registry, alias)
	if err != nil {
		return nil, err
	}
	result := []Result{}
	ref, refErr := host.FindReference(host.ReferencePath(registry), alias)
	h, hostErr := host.Find(registry, ref.Host)
	seen := map[string]bool{}
	for _, svc := range d.Services {
		seen[svc.Name] = true
		r := Result{Name: svc.Name, LocalPort: svc.LocalPort, Code: "stopped"}
		for _, s := range states {
			if s.Service.Name == svc.Name {
				descriptor, e := ReadDescriptor(filepath.Join(s.Directory, "proxy.json"))
				if refErr != nil || hostErr != nil || e != nil || s.Service != svc || descriptor.Alias != alias || descriptor.Service != svc || descriptor.Host != h || descriptor.Request.Profile != ref.Profile {
					r.Code = "service_binding_changed_stop_first"
				} else {
					r = inspectOne(ctx, s)
				}
				break
			}
		}
		if r.Code == "stopped" && !portAvailable(svc.LocalPort) {
			r.Code = "local_port_conflict"
		}
		result = append(result, r)
	}
	for _, s := range states {
		if !seen[s.Service.Name] {
			result = append(result, Result{Name: s.Service.Name, LocalPort: s.Service.LocalPort, Code: "service_binding_changed_stop_first"})
		}
	}
	return result, nil
}
func ReadDescriptor(path string) (Descriptor, error) {
	var d Descriptor
	if checkDir(filepath.Dir(path)) != nil || filepath.Base(path) != "proxy.json" {
		return d, ErrConfig
	}
	b, e := privatefile.Read(path, 8192)
	if e != nil || jsonstrict.Decode(b, &d, 8192) != nil || d.Host.Validate() != nil || !profile.ValidName(d.Alias) || d.Service.Validate() != nil || !profile.ValidName(d.Request.Profile) || !containerIDPattern.MatchString(d.Request.ApplicationID) || !Endpoint(d.Request.Host) || d.Request.Port < 1 || d.Request.Port > 65535 {
		return Descriptor{}, ErrConfig
	}
	return d, nil
}
