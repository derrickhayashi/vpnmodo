package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"path/filepath"
	"time"

	"github.com/derrickhayashi/vpnmodo/internal/host"
	"github.com/derrickhayashi/vpnmodo/internal/privatefile"
)

func managedReference(path, name string) (host.Reference, error) {
	ref, err := host.FindReference(path, name)
	if err != nil || ref.Host != host.ManagedName {
		return host.Reference{}, errors.New("profile is not configured in this environment; run vpnmodo setup")
	}
	return ref, nil
}

// startManagedVM returns only fixed error codes suitable for an access report.
// It resumes an existing setup-owned VM without provisioning or changing images.
func startManagedVM(base context.Context, registry string, h host.Host, out io.Writer) (bool, error) {
	ctx, cancel := context.WithTimeout(base, 5*time.Minute)
	defer cancel()
	dir := filepath.Join(filepath.Dir(registry), "setup", host.ManagedName)
	if h.Name != host.ManagedName || h.SSH != "lima-"+host.ManagedName || !h.Sudo || h.SSHConfig == "" {
		return false, errors.New("vm_reference_conflict_run_setup")
	}
	if _, err := privatefile.Read(filepath.Join(dir, "lima.yaml"), 64*1024); err != nil {
		return false, errors.New("vm_setup_unavailable_run_setup")
	}
	// Different profiles share one VM. Also serialize with setup before checking
	// its state so concurrent up commands do not race to start it.
	unlock, err := privatefile.Lock(ctx, filepath.Join(dir, "setup.lock"))
	if err != nil {
		return false, errors.New("vm_start_unavailable")
	}
	defer unlock()
	instance, err := findLima(ctx, host.ManagedName)
	if err != nil {
		return false, errors.New("vm_lima_unavailable_run_setup")
	}
	if instance.Name == "" {
		return false, errors.New("vm_missing_run_setup")
	}
	if instance.SSHConfig != h.SSHConfig {
		return false, errors.New("vm_reference_conflict_run_setup")
	}
	switch instance.Status {
	case "Running":
		return false, nil
	case "Stopped":
		fmt.Fprintln(out, "Starting the VM...")
		// Lima waits for guest readiness. Discard its raw output so local paths
		// and provisioner diagnostics never enter reports or machine JSON.
		if exec.CommandContext(ctx, "limactl", "start", "--tty=false", host.ManagedName).Run() != nil {
			return false, errors.New("vm_start_failed")
		}
	default:
		return false, errors.New("vm_state_unavailable")
	}
	return true, nil
}
