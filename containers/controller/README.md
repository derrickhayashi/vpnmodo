# Controller container and workstation host commands

The `vpnmodo` CLI manages a containerized `vpnmodo-controller` over verified SSH.
The controller provides profile management, connection operations, schema
discovery and Docker readiness checks. See the [profile guide](../../docs/design/002-profile-storage.md).
Persistent operation coordination is described in the
[operation guide](../../docs/design/003-persistent-operations.md). The
[managed L2TP adapter](../../docs/design/005-managed-l2tp.md) now supplies real
Docker/Compose lifecycle and controller-owned recovery. `runtime_enabled` reports
that adapter wiring; image/kernel prerequisites and a verified VPN are separate
checks. The [managed XAuth adapter](../../docs/design/006-managed-xauth.md) adds
secure MFA and mandatory kernel-enforced isolation, with explicit authentication
after tunnel loss.

For release binaries and images, use the
[release guide](../../docs/releases.md) and [onboarding guide](../../docs/onboarding.md).
The development build below remains available.

## Prerequisites and build

`vpnmodo setup` creates the managed Ubuntu 24.04 Lima VM and installs SSH,
Python 3, rootful Docker Engine and Compose. The controller uses the VM's
`/var/run/docker.sock` explicitly; workstation Docker contexts do not redirect
this management connection.

Build the workstation binary with Go 1.27:

```sh
go build -trimpath -o /tmp/vpnmodo ./cmd/vpnmodo
```

For controller development, build the image from source. Normal setup pulls the
published release images from GHCR:

```sh
docker build -t vpnmodo-controller:dev -f containers/controller/Dockerfile .
```

The build context allowlists only the controller's Go source packages and module
files. The runtime image bundles the static Go binary and Docker CLI/Compose.
It contains no customer configuration or credentials. Install the development
backend/application images explicitly on the VM before connection:

```sh
docker build -t vpnmodo-l2tp:dev containers/l2tp-ipsec
docker build -t vpnmodo-xauth:dev containers/ipsec-xauth
docker pull alpine:3.22
```

The VM needs kernel IPsec/netfilter support. XAuth requires native XFRM output
marks and IPv6-disable sysctls, tested on Ubuntu 24.04. L2TP additionally needs
PPP (`ppp_generic`, `ppp_async`, `/dev/ppp`).
xl2tpd can use userspace L2TP when optional kernel L2TP acceleration is absent.
The adapter never installs modules, creates host devices, builds or pulls images.

## Managed setup

Users install the standalone CLI and run `vpnmodo setup`. This prepares the one
managed Lima VM, starts its controller and checks readiness. VPN profiles remain
JSON and are created with `vpnmodo profile create NAME --file PROFILE.json`;
they automatically use that environment.

Internally, setup uses fixed Python sent over Lima's SSH connection. Parameters
are JSON stdin. It creates private storage, writes the controller Compose
manifest and starts the controller. Matching state is reused, while foreign
containers and mismatched storage are refused. This bootstrap is an internal
implementation detail, not an alternate host-management API.


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
connection/operation methods use a persistent manager with both managed adapters.
Request/response logging is disabled. Allowlisted lifecycle and backend events
are stored separately in private, rotating JSONL files; `connection.logs` exposes
bounded per-profile queries and status reports `logging_ready`. See
[VPN logging](../../docs/logging.md).

The service bounds connections, outstanding calls, frame size/depth, idle reads
and writes. Docker readiness uses the local `GET /_ping` endpoint. Profile mutations also
query exactly labeled managed containers to verify inactivity. These read-only
checks do not change existing VPN deployments. Container health checks verify
the management process; status separately reports Docker readiness.

## Validation

`go test -race ./...` exercises real temporary Unix sockets, negotiation, strict
input/error handling, bridge EOF, service lifetime, registry updates and secret
handling. Bootstrap unit tests fake Docker and never touch live resources.

The [local Ubuntu lab](../../tests/local-lab/README.md) uses an existing VM and a
disposable nested Docker daemon. It generates
ephemeral SSH keys outside the checkout, pins its own server key, rejects unknown
keys, creates the controller through actual SSH/Compose, checks private mounts and
permissions, repairs a stopped controller, recreates it with state retained, and
checks an unrelated synthetic legacy container is unchanged. It removes its
resources. Do not run that smoke test on a customer or shared deployment host.

The supplied `compose.yaml` is a manual equivalent for an explicitly prepared
host; set image, data-root and numeric UID/GID/socket group values first. It is
independent from all VPN profile projects.
