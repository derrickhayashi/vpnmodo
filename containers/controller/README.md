# Controller container and workstation host commands

This stage supplies runnable `vpnmodo` and `vpnmodo-controller` binaries, verified
SSH transport, schema discovery and controller/Docker readiness. VPN profile
management is now available through the [profile guide](../../docs/design/002-profile-storage.md).
Connection operations, recovery and XAuth enforcement are later stages.
The status field `runtime_enabled` is false; backend descriptors describe schema
requirements, not usable VPN sessions.

## Prerequisites and build

The target is an existing Ubuntu 24.04 VM with SSH, Python 3, rootful Docker
Engine and Compose v2 or later. The SSH account must already have Docker access.
The commands use the VM's `/var/run/docker.sock` explicitly; Docker contexts on
the workstation/VM do not redirect this management connection. VM creation and
installing Docker/SSH are outside this skeleton's setup command. Existing Ansible
deployment tooling remains available for prerequisites and legacy VPN instances.

Build the workstation binary with Go 1.27:

```sh
go build -trimpath -o /tmp/vpnmodo ./cmd/vpnmodo
```

Build the controller image on the VM, or explicitly transfer a compatible built
image using Docker's normal image tooling. There is no published release image
yet, and `host setup` does not upload source or image archives:

```sh
docker build -t vpnmodo-controller:dev -f containers/controller/Dockerfile .
```

The build context allowlists only the controller's Go source packages and module
files. The runtime image is scratch plus a static Go binary; it contains no shell,
Docker CLI, SSH server, customer configuration or credentials.

## Register, set up and check

Use an existing SSH alias with trusted host keys and key-based authentication:

```sh
/tmp/vpnmodo host add my-vm --ssh vpn-host
/tmp/vpnmodo host setup my-vm --image vpnmodo-controller:dev
/tmp/vpnmodo host check my-vm
/tmp/vpnmodo host check my-vm --json
```

`host add` stores only the name, SSH target and optional `--ssh-config` path. It
does not connect/provision. The private registry is under `os.UserConfigDir()`:
`Library/Application Support/vpnmodo/hosts.json` on macOS. `--config` before `host`
selects another absolute private registry path for automation/tests. Secret input
does not belong in that registry. Registry updates lock and replace atomically.

`host setup` uses fixed Python code sent over SSH; operator inputs are JSON stdin
and never interpolated into shell syntax. It requires the image to exist on the
VM or be pullable with that host's existing registry authentication. It creates
private storage at `~/.local/share/vpnmodo` on the VM, writes a protected resolved
controller Compose manifest, and starts only project `vpnmodo-controller`.
It serializes bootstrap updates and refuses foreign containers with the same
name, a different data mount, symlink storage or public storage permissions.

An existing matching controller is reused or started if stopped. A changed image
requires `--replace`. Use that explicit option to recreate/repair the controller
while retaining persistent storage. No VPN or legacy Compose project is stopped:

```sh
/tmp/vpnmodo host setup my-vm --image vpnmodo-controller:dev --replace
```

Setup reports created/reused/started/replaced, not end-to-end readiness. Follow it
with `host check`; the container may need a moment to start. Setup can repair a
stopped controller without relying on the controller's own API. Its current
private manifest format is implementation-owned, not a user profile schema.

## Process and permission model

The service runs as the VM SSH user's UID/GID with the Docker socket's numeric
group added. It has a read-only image filesystem, no network attachment/published
ports, no added capabilities, and a private owner-matched tmpfs for its Unix
socket. Persistent host data is mounted at the same absolute path inside the
controller. Docker socket access remains administrative authority over the VM.

The workstation invokes a fixed non-PTY `docker exec -i` bridge over SSH. SSH
requires existing host-key trust and noninteractive authentication. The bridge
forwards bounded frames to the persistent service's private Unix socket. Closing
stdin drains replies; closing a bridge leaves the service running. The service
requires `system.hello` negotiation per connection and exposes `system.status`,
`backend.list` and `backend.describe`. Profile management methods validate and provision protected VM storage;
connection/operation methods remain reserved. Request/response logging is disabled.

The service bounds connections, outstanding calls, frame size/depth, idle reads
and writes. Docker readiness uses the local `GET /_ping` endpoint. Profile mutations also
query exactly labeled managed containers to verify inactivity. These read-only
checks do not change existing VPN deployments. Container health checks verify
the management process; status separately reports Docker readiness.

## Validation

`go test -race ./...` exercises real temporary Unix sockets, negotiation, strict
input/error handling, bridge EOF, service lifetime, registry updates and secret
handling. Bootstrap unit tests fake Docker and never touch live resources.

The `Controller SSH acceptance` workflow uses a fresh Ubuntu runner. It generates
ephemeral SSH keys outside the checkout, pins its own server key, rejects unknown
keys, creates the controller through actual SSH/Compose, checks private mounts and
permissions, repairs a stopped controller, recreates it with state retained, and
checks an unrelated synthetic legacy container is unchanged. It removes its
resources. Do not run that smoke test on a customer or shared deployment host.

The supplied `compose.yaml` is a manual equivalent for an explicitly prepared
host; set image, data-root and numeric UID/GID/socket group values first. It is
independent from all VPN profile projects.
