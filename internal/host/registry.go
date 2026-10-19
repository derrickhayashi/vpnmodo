// Package host stores workstation SSH references and bootstraps the controller.
// It never stores VPN credentials or copies SSH private keys.
package host

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"syscall"

	"github.com/derrickhayashi/vpn-sidecars-next/internal/jsonstrict"
	"github.com/derrickhayashi/vpn-sidecars-next/internal/profile"
)

var ErrRegistry = errors.New("invalid or unavailable private host registry")
var ErrDuplicate = errors.New("host already registered")
var targetPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.@:-]{0,252}$`)

type Host struct {
	Name      string `json:"name"`
	SSH       string `json:"ssh"`
	SSHConfig string `json:"ssh_config,omitempty"`
	Sudo      bool   `json:"sudo,omitempty"`
}

func (h Host) Validate() error {
	if !profile.ValidName(h.Name) || !targetPattern.MatchString(h.SSH) || (h.SSHConfig != "" && (!filepath.IsAbs(h.SSHConfig) || len(h.SSHConfig) > 4096)) {
		return ErrRegistry
	}
	return nil
}

type Registry struct {
	SchemaVersion int    `json:"schema_version"`
	Hosts         []Host `json:"hosts"`
}

func DefaultRegistry() (string, error) {
	root, err := os.UserConfigDir()
	if err != nil {
		return "", ErrRegistry
	}
	return filepath.Join(root, "vpnmodo", "hosts.json"), nil
}
func Read(path string) (Registry, error) {
	result := Registry{SchemaVersion: 1, Hosts: []Host{}}
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return result, nil
	}
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0 || info.Size() > 64*1024 {
		return Registry{}, ErrRegistry
	}
	data, err := os.ReadFile(path)
	if err != nil || jsonstrict.Decode(data, &result, 64*1024) != nil || result.SchemaVersion != 1 {
		return Registry{}, ErrRegistry
	}
	seen := map[string]bool{}
	for _, h := range result.Hosts {
		if h.Validate() != nil || seen[h.Name] {
			return Registry{}, ErrRegistry
		}
		seen[h.Name] = true
	}
	return result, nil
}
func Find(path, name string) (Host, error) {
	r, err := Read(path)
	if err != nil {
		return Host{}, err
	}
	for _, h := range r.Hosts {
		if h.Name == name {
			return h, nil
		}
	}
	return Host{}, errors.New("host is not registered")
}
func Add(path string, host Host) error {
	if host.Validate() != nil || !filepath.IsAbs(path) {
		return ErrRegistry
	}
	parent := filepath.Dir(path)
	if os.MkdirAll(parent, 0700) != nil {
		return ErrRegistry
	}
	info, err := os.Lstat(parent)
	if err != nil || !info.IsDir() || info.Mode().Perm()&0077 != 0 {
		return ErrRegistry
	}
	lock, err := os.OpenFile(path+".lock", os.O_CREATE|os.O_RDWR|syscall.O_NOFOLLOW, 0600)
	if err != nil {
		return ErrRegistry
	}
	defer lock.Close()
	if syscall.Flock(int(lock.Fd()), syscall.LOCK_EX) != nil {
		return ErrRegistry
	}
	defer syscall.Flock(int(lock.Fd()), syscall.LOCK_UN)
	registry, err := Read(path)
	if err != nil {
		return err
	}
	for _, h := range registry.Hosts {
		if h.Name == host.Name {
			return ErrDuplicate
		}
	}
	registry.Hosts = append(registry.Hosts, host)
	data, err := json.MarshalIndent(registry, "", "  ")
	if err != nil || len(data) > 64*1024 {
		return ErrRegistry
	}
	file, err := os.CreateTemp(parent, ".hosts-")
	if err != nil {
		return ErrRegistry
	}
	defer os.Remove(file.Name())
	if _, err = file.Write(append(data, '\n')); err == nil {
		err = file.Sync()
	}
	closeErr := file.Close()
	if err != nil || closeErr != nil {
		return ErrRegistry
	}
	if os.Rename(file.Name(), path) != nil {
		return ErrRegistry
	}
	return nil
}
