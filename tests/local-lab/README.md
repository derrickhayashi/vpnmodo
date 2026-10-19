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
and simulated MFA/recovery, the real IPsec/L2TP/PPP gateway/client suite, and the
real aggressive PSK/XAUTH_PASSCODE suite. XAuth uses a separate reachable escape
sink with a deliberate capture positive control before zero-plaintext assertions.
It tests DNS/IPv6, rekey, quiet sessions, active TCP/UDP flows after SA/policy loss,
MFA failure/expiry/repetition and controller restart. Only the test binary shortens
the MFA window to 45 seconds; production keeps its normal deadline. The quiet
session interval exceeds 90 seconds while controller probes are paused.

The synthetic observer runs the tcpdump binary under a lab-specific executable
path. Ubuntu's host tcpdump AppArmor attachment otherwise prevents the nested
container's helper from signalling it, even with a container unconfined setting.
This keeps capture lifecycle deterministic without changing VM AppArmor policy
or production container configuration.

The runner writes its final exit code to /tmp/vpnmodo-lab.exit inside the outer
container. For remote automation, capture output and poll this file so a held
output stream cannot hide the result. An exit code of zero is required to claim
the whole suite passed.

## Service access and targeted diagnosis

Both real VPN suites now create a synthetic private OpenSSH server in the
lab gateway's network namespace and a separate destination-loopback HTTP service.
They exercise the workstation's managed two-hop proxy, named loopback forwards,
port conflicts/partial success, independent VM/controller/service failures,
strict private SSH keys/authentication, stale ownership, master-process crash,
controller replacement and composed disconnect. XAuth also exercises composed
JSON `up` with a fresh real MFA challenge and separate secure code submission.
No private keys are copied into the VPN/application containers.

The default runner keeps the full protocol/recovery and packet tests. After its
images are built, a focused L2TP/service subset can shorten diagnosis inside the
same outer lab:

```sh
VPNMODO_ISOLATED_TEST_HOST=1 python3 tests/smoke_controller.py \
  --binary /opt/lab/vpnmodo --image vpnmodo-controller:l2tp-acceptance \
  --l2tp --services-only
```

This subset does not establish that the full recovery/packet suite passed.
A test-only L2TP client image redirects the unchanged production entrypoint's
startup output to a private runtime file. On failure, the harness retains that
synthetic evidence outside the checkout and prints only allowlisted flags.
Production images and logs retain their existing behavior.

## Current validation

The complete production controller Dockerfile also builds successfully on the
local ARM64 VM. Go race tests and vet pass; the CLI cross-builds for macOS/Linux
arm64/amd64 and the controller for Linux arm64/amd64.

Ubuntu 24.04 ARM64 local acceptance passes verified SSH/controller/profile
handling and the full real IKEv1/IPsec/L2TP/PPP/TCP/DNS suite, including endpoint
outage, namespace repair, tunnel-loss recovery, failed recovery, explicit
stop/reconnect, controller recreation and credential disposal.

The XAuth suite also passes real aggressive PSK/XAUTH_PASSCODE over NAT-T,
private TCP/DNS, authenticated CHILD rekey and quiet sessions. Physical-veth
positive controls observe 2 unprivileged IKE-port probes before login, 4 ordinary
plaintext probes, and 10 packets after policy/routing loss with enforcement
intentionally opened. Every protected phase observes zero client plaintext,
including existing TCP/UDP flows with SAs/policies removed while the controller
and client are suspended. Kernel counters also require zero received plaintext
and no dropped capture packets. The suite covers IPv6/Docker DNS bypass refusal,
SO_MARK forgery refusal, expired/incorrect/repeated MFA, actual controller-process
crash, hidden terminal input, session-preserving recreation and credential cleanup.

Application repair now uses a separate application-only manifest pinned to the
verified VPN container ID. It cannot start/recreate a VPN through Compose
service-dependency reconciliation. The L2TP namespace test waits for its external
replacement to become healthy before resuming the controller, then requires
application-only repair. The full real L2TP suite and three stop/reconnect cycles
pass with that precondition and without external namespace monitors.

A foreground idle sleep deferred the VPN shell's TERM handler beyond Docker's
two-second stop limit. The container could be force-killed before telling the
peer to close its session. The idle wait is now interruptible. The regression
stops a real VPN just after a new sleep starts, requires a clean exit and peer
session cleanup within the existing limit, then reconnects three times.
Neither the service-probe timeout nor the recovery policy was relaxed.

The XAuth observer captures the host side of the VPN veth in the outer lab's
stable namespace, before bridge routing. Positive controls verify it sees both
the pre-login direct path and the post-login route through the gateway. Each
observer is stopped and reaped after its phase; it holds no VPN namespace
reference. Avoid external nsenter captures in a dynamic VPN namespace: they can
retain a retired namespace and change reconnect behavior. Keep all captures and
runtime diagnostics outside the checkout.
