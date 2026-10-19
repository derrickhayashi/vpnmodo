# Isolated local VPN lab

Run acceptance on an existing Ubuntu 24.04 VM instead of GitHub Actions. The
outer test container has its own Docker daemon, network namespace and storage.
It receives no VM Docker socket, host network, customer profiles or credentials.
It is privileged to run nested containers and IPsec/PPP, so use trusted committed
source on an authorized VM. This isolates Docker resources; it is not a security
boundary against hostile code and still shares the VM kernel and resources.

The workstation needs the project Go toolchain, Python 3.9+ and Git. The VM needs
Docker, sudo, OpenSSH and PPP/XFRM kernel support. It needs no host Go installation.
The first run downloads Ubuntu packages and container images. Only synthetic
fixtures are used. Test SSH keys and controller state are created inside the
outer container and removed by the acceptance harness.

## Prepare on the workstation

Commit the intended source first. The helper refuses a dirty checkout and
packages only the Git archive of HEAD, never ignored or customer runtime files.
Choose the VM architecture (arm64 for an ARM Ubuntu guest):

    bundle=$(python3 scripts/prepare-local-lab.py --arch arm64)

The helper prints a temporary directory outside the checkout containing committed
source, Linux binaries, runtime Docker build contexts and the lab image recipe.
It derives the production controller runtime stage from the existing Dockerfile;
it does not test the Go builder stage of that Dockerfile. Go race/vet/unit checks
remain separate workstation checks. Keep the generated bundle outside the repo.

Transfer this directory to a private temporary directory on the VM. Verify and pin
its SSH host key through a trusted source such as UTM before copying. Explicitly
use StrictHostKeyChecking=yes and a private UserKnownHostsFile when the existing
SSH alias disables verification. Keep the alias, address and host key outside
this repository.

## Run on the VM

Set bundle to the absolute transferred directory, and choose a unique name. The
following commands access the VM Docker daemon only to build and manage the
outer lab. All acceptance containers and networks live in its nested daemon.
Record the existing VM container IDs and running/start states before the run and
compare them afterwards; do not inspect or copy their configuration or logs.

    bundle=/absolute/path/to/transferred-bundle
    name=vpnmodo-testlab-$(date +%s)-$$
    sudo -n modprobe ppp_generic
    sudo -n modprobe ppp_async
    sudo -n modprobe xfrm_user
    test -c /dev/ppp
    sudo -n docker build -t vpnmodo-testlab:local "$bundle"
    sudo -n docker run -d --name "$name" --privileged --device /dev/ppp \
      --label io.vpnmodo.testlab=local --log-driver none \
      --mount "type=bind,src=$bundle/work,dst=/work,readonly" \
      --mount "type=bind,src=$bundle/artifacts,dst=/opt/lab,readonly" \
      vpnmodo-testlab:local
    trap 'sudo -n docker rm -f "$name" >/dev/null' EXIT
    sudo -n docker exec "$name" sh -c \
      'i=0; until test -f /run/test-lab-ready; do i=$((i+1)); test "$i" -lt 130 || exit 1; sleep 1; done'
    sudo -n docker exec --user labuser --env HOME=/home/labuser \
      --env USER=labuser --workdir /work "$name" sh /opt/lab/run

Use these commands in a dedicated shell script with set -eu so failure stops the
run and the EXIT trap removes the exact outer container. Do not use broad Docker
prune commands, stop existing containers, restart Docker or change VM routing.
Removing the outer container also removes its nested containers, images and
runtime state. The outer lab image remains cached for reuse. Remove only the
bundle directory you created when finished.

The run covers controller setup/repair and profile handling, persistent operations
and simulated MFA/recovery, and the real IPsec/L2TP/PPP gateway/client suite with
DNS, application network sharing and recovery. XAuth protocol MFA and packet
observed fail-closed acceptance remain the next implementation stage.
