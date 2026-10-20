package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"time"

	assets "github.com/derrickhayashi/vpnmodo"
	"github.com/derrickhayashi/vpnmodo/internal/controller"
	"github.com/derrickhayashi/vpnmodo/internal/credentialinput"
	"github.com/derrickhayashi/vpnmodo/internal/host"
	"github.com/derrickhayashi/vpnmodo/internal/privatefile"
	"github.com/derrickhayashi/vpnmodo/internal/protocol"
)

func setupCommand(base context.Context, registry string, args []string, out io.Writer) error {
	if len(args) == 1 && (args[0] == "--help" || args[0] == "help") {
		fmt.Fprintln(out, "Usage: vpnmodo setup [--config FILE] [--registry-auth FILE]\nCreates the managed local environment and starts the controller.\nOptional setup config is reserved and currently ignored.\nOptional registry auth JSON contains username and token; omit it for public images.")
		return nil
	}
	flags := flag.NewFlagSet("setup", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	// Reserve the setup-local input until its configuration fields are defined.
	flags.String("config", "", "reserved setup configuration file (currently ignored)")
	file := flags.String("registry-auth", "", "")
	if flags.Parse(args) != nil || flags.NArg() != 0 {
		return errors.New("usage: vpnmodo setup [--config FILE] [--registry-auth FILE]")
	}
	var auth *registryCredentials
	if *file != "" {
		var err error
		auth, err = readRegistryFile(*file)
		if err != nil {
			return err
		}
	}
	if runtime.GOOS != "darwin" || runtime.GOARCH != "arm64" {
		return errors.New("setup requires an Apple Silicon Mac")
	}
	if registry == "" {
		var err error
		registry, err = host.DefaultRegistry()
		if err != nil {
			return err
		}
	}
	if !filepath.IsAbs(registry) {
		return host.ErrRegistry
	}
	ctx, cancel := context.WithTimeout(base, 30*time.Minute)
	defer cancel()
	if _, err := exec.LookPath("limactl"); err != nil {
		if _, err := exec.LookPath("brew"); err != nil {
			return errors.New("install Homebrew from https://brew.sh, then run vpnmodo setup again")
		}
		fmt.Fprintln(out, "Installing Lima with Homebrew...")
		if err := setupRun(ctx, out, "brew", "install", "lima"); err != nil {
			return err
		}
	}
	h, release, err := provisionLima(ctx, registry, auth, out)
	if err != nil {
		return err
	}
	fmt.Fprintln(out, "Starting the controller...")
	activation, stop := context.WithTimeout(ctx, 4*time.Minute)
	_, err = host.Setup(activation, h, host.SetupParams{Image: release.ControllerImage, BackendImages: release.BackendImages})
	stop()
	if err != nil {
		return err
	}
	fmt.Fprintln(out, "Checking controller readiness...")
	if err := waitSetupReady(ctx, h, release.Version); err != nil {
		return err
	}
	fmt.Fprintln(out, "Environment is ready.\nNext: vpnmodo profile create NAME --file PROFILE.json")
	return nil
}

func setupRun(ctx context.Context, out io.Writer, program string, args ...string) error {
	cmd := exec.CommandContext(ctx, program, args...)
	cmd.Stdout, cmd.Stderr = out, out
	if cmd.Run() != nil {
		return fmt.Errorf("%s failed; check the output above, then run setup again", program)
	}
	return nil
}

type limaInstance struct {
	Name      string `json:"name"`
	SSHConfig string `json:"sshConfigFile"`
	Status    string `json:"status"`
}

func findLima(ctx context.Context, name string) (limaInstance, error) {
	data, err := exec.CommandContext(ctx, "limactl", "list", "--json").Output()
	if err != nil {
		return limaInstance{}, errors.New("cannot list Lima instances")
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	for {
		var instance limaInstance
		if err := decoder.Decode(&instance); errors.Is(err, io.EOF) {
			return limaInstance{}, nil
		} else if err != nil {
			return limaInstance{}, errors.New("Lima returned invalid instance information")
		}
		if instance.Name == name {
			if !filepath.IsAbs(instance.SSHConfig) {
				return limaInstance{}, errors.New("Lima returned an invalid SSH config path")
			}
			return instance, nil
		}
	}
}

func limaSSHConfig(ctx context.Context, name string) (string, error) {
	instance, err := findLima(ctx, name)
	return instance.SSHConfig, err
}

// provisionLima uses native Lima and SCP; activation reuses host.Setup above.
func provisionLima(ctx context.Context, registry string, auth *registryCredentials, out io.Writer) (host.Host, host.InstalledRelease, error) {
	const name = host.ManagedName
	var empty host.InstalledRelease
	h := host.Host{Name: name, SSH: "lima-" + name, Sudo: true}
	if h.Validate() != nil || !filepath.IsAbs(registry) {
		return h, empty, host.ErrRegistry
	}
	dir := filepath.Join(filepath.Dir(registry), "setup", name)
	if err := privatefile.Dir(dir); err != nil {
		return h, empty, err
	}
	unlock, err := privatefile.Lock(ctx, filepath.Join(dir, "setup.lock"))
	if err != nil {
		return h, empty, err
	}
	defer unlock()
	registered, err := host.Read(registry)
	if err != nil {
		return h, empty, err
	}
	config, err := limaSSHConfig(ctx, name)
	if err != nil {
		return h, empty, err
	}
	for _, existing := range registered.Hosts {
		if existing.Name == name && (existing.SSH != h.SSH || !existing.Sudo || config != "" && existing.SSHConfig != config) {
			return h, empty, errors.New("the managed VM has conflicting saved SSH settings; inspect its setup configuration")
		}
	}
	yaml := filepath.Join(dir, "lima.yaml")
	if config != "" {
		if _, err := privatefile.Read(yaml, 64*1024); err != nil {
			return h, empty, errors.New("the managed Lima instance was created outside vpnmodo; inspect its setup configuration")
		}
	}
	if err := ensureRegistryAuth(filepath.Join(dir, "ghcr-config.json"), auth); err != nil {
		return h, empty, err
	}
	if config == "" {
		for name, data := range map[string][]byte{"lima.yaml": assets.LimaYAML, "registry-release.json": assets.RegistryRelease, "install-vm.py": assets.VMInstaller} {
			if _, err := privatefile.Write(filepath.Join(dir, name), data); err != nil {
				return h, empty, err
			}
		}
		fmt.Fprintln(out, "Creating and provisioning the Lima VM...")
		err = setupRun(ctx, out, "limactl", "start", "--tty=false", "--name", name, yaml)
	} else {
		fmt.Fprintln(out, "Starting the existing Lima VM...")
		err = setupRun(ctx, out, "limactl", "start", "--tty=false", name)
	}
	if err != nil {
		// A previous bad registry login can fail the boot's image step while
		// leaving this setup-owned VM running and reachable for repair.
		instance, lookup := findLima(ctx, name)
		if config == "" || ctx.Err() != nil || lookup != nil || instance.Status != "Running" {
			return h, empty, err
		}
		fmt.Fprintln(out, "VM is running; retrying image setup with saved credentials...")
	}
	h.SSHConfig, err = limaSSHConfig(ctx, name)
	if err != nil || h.SSHConfig == "" {
		return h, empty, errors.New("Lima did not provide an SSH configuration")
	}
	if config != "" {
		// Lima embeds data files at creation. Refresh auth and finish image setup
		// when retrying after a failed pull, using only stdin for the credentials.
		auth, err := privatefile.Read(filepath.Join(dir, "ghcr-config.json"), 16*1024)
		if err != nil {
			return h, empty, err
		}
		writeAuth := `python3 -c 'import os,sys; os.umask(0o077); os.makedirs("/root/.docker",exist_ok=True); f=open("/root/.docker/config.json","wb"); f.write(sys.stdin.buffer.read()); f.close(); os.chmod("/root/.docker/config.json",0o600)'`
		err = setupSSH(ctx, h, writeAuth, auth)
		clear(auth)
		if err != nil {
			return h, empty, err
		}
		finish := `sh -c 'python3 /usr/local/lib/vpnmodo/install-vm.py --registry /etc/vpnmodo/registry-release.json > /var/lib/vpnmodo/installed-release.json.tmp && chmod 644 /var/lib/vpnmodo/installed-release.json.tmp && mv /var/lib/vpnmodo/installed-release.json.tmp /var/lib/vpnmodo/installed-release.json'`
		if err := setupSSH(ctx, h, finish, nil); err != nil {
			return h, empty, err
		}
	}
	receipt := filepath.Join(dir, "installed-release.json")
	if err := setupRun(ctx, out, "scp", "-F", h.SSHConfig, h.SSH+":/var/lib/vpnmodo/installed-release.json", receipt); err != nil {
		return h, empty, err
	}
	release, err := host.ReadInstalledRelease(receipt)
	if err != nil {
		return h, empty, err
	}
	if err := host.Add(registry, h); err != nil {
		previous, lookup := host.Find(registry, name)
		if !errors.Is(err, host.ErrDuplicate) || lookup != nil || previous != h {
			return h, empty, err
		}
	}
	return h, release, nil
}

func setupSSH(ctx context.Context, h host.Host, command string, input []byte) error {
	args, err := host.SSHArgs(h, command)
	if err != nil {
		return err
	}
	cmd := exec.CommandContext(ctx, "ssh", args...)
	cmd.Stdin = bytes.NewReader(input)
	if cmd.Run() != nil {
		return errors.New("VM image preparation failed; private images need setup --registry-auth FILE")
	}
	return nil
}

func ensureRegistryAuth(path string, supplied *registryCredentials) error {
	if supplied != nil {
		data, err := credentialinput.RegistryConfig(supplied.Username, supplied.Token)
		if err != nil {
			return errors.New("invalid registry auth; JSON needs username and token")
		}
		defer clear(data)
		_, err = privatefile.Write(path, data)
		return err
	}
	data, err := privatefile.Read(path, 16*1024)
	if err == nil {
		defer clear(data)
		var config struct {
			Auths map[string]struct {
				Auth string `json:"auth"`
			} `json:"auths"`
		}
		if json.Unmarshal(data, &config) != nil || config.Auths == nil || len(config.Auths) != 0 && config.Auths["ghcr.io"].Auth == "" {
			return errors.New("invalid saved GHCR credentials; supply setup --registry-auth FILE")
		}
		return nil
	}
	if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	// Lima's data provisioner always needs a file, including anonymous pulls.
	_, err = privatefile.Write(path, []byte("{\"auths\":{}}\n"))
	return err
}

func waitSetupReady(base context.Context, h host.Host, version string) error {
	ctx, cancel := context.WithTimeout(base, time.Minute)
	defer cancel()
	for {
		attempt, stop := context.WithTimeout(ctx, 5*time.Second)
		client, err := host.Connect(attempt, h)
		if err == nil {
			var status controller.Status
			err = client.Call(attempt, protocol.SystemStatus, struct{}{}, &status)
			client.Close()
			if err == nil && status.StateReady && status.DockerReady && status.RuntimeEnabled && status.Version == version {
				stop()
				return nil
			}
		}
		stop()
		select {
		case <-ctx.Done():
			return errors.New("controller is not ready; check the Lima logs and run setup again")
		case <-time.After(time.Second):
		}
	}
}
