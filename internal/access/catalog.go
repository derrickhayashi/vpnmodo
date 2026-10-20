// Package access owns workstation service definitions and OpenSSH forwards.
// Neither workstation files nor SSH errors may be copied to the controller.
package access

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"os"
	"path/filepath"
	"regexp"

	"github.com/derrickhayashi/vpnmodo/internal/host"
	"github.com/derrickhayashi/vpnmodo/internal/jsonstrict"
	"github.com/derrickhayashi/vpnmodo/internal/privatefile"
	"github.com/derrickhayashi/vpnmodo/internal/profile"
)

var ErrConfig = errors.New("invalid or unavailable private service configuration")
var endpointPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9.-]{0,252}$`)

type Service struct {
	Name       string `json:"name"`
	SSHTarget  string `json:"ssh_target"`
	SSHConfig  string `json:"ssh_config,omitempty"`
	LocalPort  int    `json:"local_port"`
	RemoteHost string `json:"remote_host"`
	RemotePort int    `json:"remote_port"`
}
type Definition struct {
	SchemaVersion int       `json:"schema_version"`
	Services      []Service `json:"services"`
}

func (s Service) Validate() error {
	if !profile.ValidName(s.Name) || (host.Host{Name: "destination", SSH: s.SSHTarget, SSHConfig: s.SSHConfig}).Validate() != nil || s.LocalPort < 1 || s.LocalPort > 65535 || s.RemotePort < 1 || s.RemotePort > 65535 || !Endpoint(s.RemoteHost) {
		return ErrConfig
	}
	return nil
}
func Endpoint(h string) bool {
	return endpointPattern.MatchString(h) && (net.ParseIP(h) == nil || net.ParseIP(h).To4() != nil)
}
func (d Definition) Validate() error {
	if d.SchemaVersion != 1 || d.Services == nil || len(d.Services) > 32 {
		return ErrConfig
	}
	names := map[string]bool{}
	ports := map[int]bool{}
	for _, s := range d.Services {
		if s.Validate() != nil || names[s.Name] || ports[s.LocalPort] {
			return ErrConfig
		}
		names[s.Name] = true
		ports[s.LocalPort] = true
	}
	return nil
}
func Load(path string) (Definition, error) {
	data, err := privatefile.ReadInput(path, 64*1024)
	return decode(data, err)
}
func decode(data []byte, err error) (Definition, error) {
	if err != nil {
		return Definition{}, ErrConfig
	}
	var d Definition
	if jsonstrict.Decode(data, &d, 64*1024) != nil || d.Validate() != nil {
		return Definition{}, ErrConfig
	}
	return d, nil
}
func catalogPath(registry, alias string) string {
	return filepath.Join(registry+".access", alias+".json")
}
func Read(registry, alias string) (Definition, error) {
	if !profile.ValidName(alias) {
		return Definition{}, ErrConfig
	}
	data, err := privatefile.Read(catalogPath(registry, alias), 64*1024)
	d, err := decode(data, err)
	if err != nil {
		if _, e := os.Lstat(catalogPath(registry, alias)); errors.Is(e, os.ErrNotExist) {
			return Definition{1, []Service{}}, nil
		}
	}
	return d, err
}
func Configure(ctx context.Context, registry, alias string, d Definition) error {
	if !profile.ValidName(alias) || d.Validate() != nil || privatefile.Dir(registry+".access") != nil {
		return ErrConfig
	}
	unlock, err := privatefile.Lock(ctx, filepath.Join(registry+".access", alias+".lock"))
	if err != nil {
		return ErrConfig
	}
	defer unlock()
	// Definitions cannot change underneath an owned master, including stale ones;
	// stop first so teardown continues using its original destination and socket.
	states, err := readStates(registry, alias)
	if err != nil || len(states) != 0 {
		return errors.New("stop owned services before replacing their configuration")
	}
	b, _ := json.Marshal(d)
	if len(b) > 64*1024 {
		return ErrConfig
	}
	_, err = privatefile.Write(catalogPath(registry, alias), b)
	return err
}
