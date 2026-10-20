package upgrade

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"time"

	"github.com/derrickhayashi/vpn-sidecars-next/internal/host"
	"github.com/derrickhayashi/vpn-sidecars-next/internal/jsonstrict"
	"github.com/derrickhayashi/vpn-sidecars-next/internal/privatefile"
)

var ErrPending = errors.New("upgrade incomplete; run vpnmodo upgrade --resume or --rollback")

type Remote func(context.Context, host.Host, host.UpgradeParams) (host.UpgradeResult, error)
type Pending struct {
	Token    string    `json:"token"`
	Target   string    `json:"target"`
	Host     host.Host `json:"host"`
	Manifest Manifest  `json:"manifest"`
}
type Installer struct {
	Root   string
	Remote Remote
}

func (i Installer) Pending() (Pending, error) {
	var p Pending
	b, err := privatefile.Read(filepath.Join(i.Root, "pending.json"), 16384)
	if err != nil {
		return p, err
	}
	if jsonstrict.Decode(b, &p, 16384) != nil || len(p.Token) != 32 || !tokenPattern.MatchString(p.Token) || !filepath.IsAbs(p.Target) || p.Host.Validate() != nil || p.Manifest.Validate("darwin", "arm64") != nil {
		return p, privatefile.ErrStorage
	}
	return p, nil
}

// AtomicExecutable never truncates a running binary and retains the previous file
// on every failure before rename. Destination ownership is checked on each call.
func AtomicExecutable(path string, data []byte) error {
	if len(data) == 0 || len(data) > MaxBinary || !filepath.IsAbs(path) {
		return privatefile.ErrStorage
	}
	if info, err := os.Lstat(path); err == nil {
		if !info.Mode().IsRegular() || !privatefile.Owned(info) {
			return privatefile.ErrStorage
		}
	} else if !os.IsNotExist(err) {
		return privatefile.ErrStorage
	}
	f, err := os.CreateTemp(filepath.Dir(path), ".vpnmodo-")
	if err != nil {
		return privatefile.ErrStorage
	}
	defer os.Remove(f.Name())
	if err = f.Chmod(0700); err == nil {
		_, err = f.Write(data)
	}
	if err == nil {
		err = f.Sync()
	}
	closed := f.Close()
	if err != nil || closed != nil {
		return privatefile.ErrStorage
	}
	if os.Rename(f.Name(), path) != nil {
		return privatefile.ErrStorage
	}
	return privatefile.SyncDir(filepath.Dir(path))
}
func (i Installer) Start(ctx context.Context, h host.Host, target string, m Manifest, binary []byte) error {
	if _, err := i.Pending(); !os.IsNotExist(err) {
		if err != nil {
			return err
		}
		return ErrPending
	}
	if m.Validate("darwin", "arm64") != nil || h.Validate() != nil {
		return ErrRelease
	}
	// Recheck the descriptor even when callers inject a downloader.
	if !matches(binary, m.CLI) {
		return ErrRelease
	}
	resolved, err := filepath.EvalSymlinks(target)
	if err != nil {
		return privatefile.ErrStorage
	}
	target = resolved
	info, err := os.Lstat(target)
	if err != nil || !info.Mode().IsRegular() || !privatefile.Owned(info) || info.Size() > MaxBinary {
		return privatefile.ErrStorage
	}
	// Confirm the installation directory supports an atomic replacement before SSH.
	probe, err := os.CreateTemp(filepath.Dir(target), ".vpnmodo-probe-")
	if err != nil {
		return privatefile.ErrStorage
	}
	probe.Close()
	os.Remove(probe.Name())
	token, err := privatefile.Token()
	if err != nil {
		return err
	}
	dir := filepath.Join(i.Root, token)
	if err = privatefile.Dir(dir); err != nil {
		return err
	}
	old, err := os.ReadFile(target)
	if err != nil {
		return privatefile.ErrStorage
	}
	if err = AtomicExecutable(filepath.Join(dir, "previous"), old); err != nil {
		return err
	}
	if err = AtomicExecutable(filepath.Join(dir, "candidate"), binary); err != nil {
		return err
	}
	check, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	cmd := exec.CommandContext(check, filepath.Join(dir, "candidate"), "version")
	cmd.Stderr = io.Discard
	output, err := cmd.Output()
	if err != nil || string(output) != m.CLIVersion+"\n" {
		return ErrRelease
	}
	p := Pending{token, target, h, m}
	raw, _ := json.Marshal(p)
	if _, err = privatefile.Write(filepath.Join(i.Root, "pending.json"), raw); err != nil {
		return err
	}
	runtime, _ := json.Marshal(m.Runtime)
	result, err := i.Remote(ctx, h, host.UpgradeParams{Action: "prepare", Token: token, Runtime: runtime})
	if err != nil || result.Phase != "prepared" {
		return ErrPending
	}
	return i.finish(ctx, p)
}
func (i Installer) finish(ctx context.Context, p Pending) error {
	binary, err := privatefile.Read(filepath.Join(i.Root, p.Token, "candidate"), MaxBinary)
	if err != nil || !matches(binary, p.Manifest.CLI) {
		return ErrPending
	}
	if err = AtomicExecutable(p.Target, binary); err != nil {
		// No commit was requested, so rollback remains safe.
		if _, rollbackErr := i.Remote(ctx, p.Host, host.UpgradeParams{Action: "rollback", Token: p.Token}); rollbackErr == nil {
			old, readErr := privatefile.Read(filepath.Join(i.Root, p.Token, "previous"), MaxBinary)
			if readErr == nil && AtomicExecutable(p.Target, old) == nil {
				_ = i.clear()
			}
		}
		return ErrPending
	}
	result, err := i.Remote(ctx, p.Host, host.UpgradeParams{Action: "commit", Token: p.Token})
	if err != nil || result.Phase != "committed" {
		return ErrPending
	}
	return i.clear()
}
func (i Installer) Recover(ctx context.Context, rollback bool) error {
	p, err := i.Pending()
	if err != nil {
		return errors.New("no readable pending upgrade")
	}
	status, err := i.Remote(ctx, p.Host, host.UpgradeParams{Action: "status", Token: p.Token})
	if err != nil {
		return ErrPending
	}
	if rollback {
		if status.Phase == "committed" {
			return errors.New("upgrade already committed; pending rollback is unavailable")
		}
		if _, err = i.Remote(ctx, p.Host, host.UpgradeParams{Action: "rollback", Token: p.Token}); err != nil {
			return ErrPending
		}
		old, err := privatefile.Read(filepath.Join(i.Root, p.Token, "previous"), MaxBinary)
		if err != nil {
			return ErrPending
		}
		if AtomicExecutable(p.Target, old) != nil {
			return ErrPending
		}
		return i.clear()
	}
	if status.Phase != "prepared" && status.Phase != "committed" {
		return errors.New("upgrade was not prepared; run vpnmodo upgrade --rollback before retrying")
	}
	return i.finish(ctx, p)
}
func (i Installer) clear() error {
	if os.Remove(filepath.Join(i.Root, "pending.json")) != nil {
		return ErrPending
	}
	return privatefile.SyncDir(i.Root)
}
