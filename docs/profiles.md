# Consumer-owned VPN profiles

For a fresh installation, follow the [Linux VM deployment guide](deployment.md).

The shared repository contains reusable backends, a launcher and generic
examples. Each consumer project owns its connection profiles, SSH aliases,
inventories and service mappings. Credentials and runtime state stay outside
the shared source checkout on the Linux host.

| Backend | Source | Authentication | Recovery |
| --- | --- | --- | --- |
| `l2tp-ipsec` | `containers/l2tp-ipsec/` | PSK and PPP username/password | Shared watchdog, auto mode |
| `ipsec-xauth` | `containers/ipsec-xauth/` | PSK, XAuth password, emailed code | Shared watchdog, manual MFA recovery |

Both backend directories contain `Dockerfile`, `compose.yaml`, and
`compose.build.yaml`. Runtime Compose uses an image; local builds explicitly
include the build override.

## Layout and commands

A consumer project can use this layout:

```text
consumer-project/vpn/
  profiles/example/profile.env        non-secret connection settings
  instances/example/secrets/          private credentials, ignored
  instances/example/run/              XAuth logs, lock, temporary MFA file, ignored
```

The launcher requires an explicit consumer profile directory. It stores runtime
state under `instances/` beside that directory. Watchdog state uses a Docker
named volume scoped to the Compose project. It does not search the shared
checkout for profiles. Choose profile names unique on each Docker host because
each profile owns the Compose project `vpn-<profile>`.

```sh
bash /path/to/vpn-sidecar/scripts/vpn --profiles-dir /path/to/consumer/vpn/profiles list
bash /path/to/vpn-sidecar/scripts/vpn --profiles-dir /path/to/consumer/vpn/profiles example validate
bash /path/to/vpn-sidecar/scripts/vpn --profiles-dir /path/to/consumer/vpn/profiles example build
bash /path/to/vpn-sidecar/scripts/vpn --profiles-dir /path/to/consumer/vpn/profiles example connect
bash /path/to/vpn-sidecar/scripts/vpn --profiles-dir /path/to/consumer/vpn/profiles example status
bash /path/to/vpn-sidecar/scripts/vpn --profiles-dir /path/to/consumer/vpn/profiles example disconnect
```

`validate` checks profile data without connecting or reading credentials.
`build` builds the selected VPN and shared watchdog images on the Linux host. `--local` forces execution directly
on the current Linux Docker host.

## Launcher diagnostics

The launcher's `main` function shows the execution order: parse arguments, load
and validate the profile, hand off over SSH when configured, prepare local
runtime paths, acquire the operation lock, render the connection manifest,
and dispatch the action. Each phase has a named helper in `scripts/vpn`.
`validate` finishes before SSH or runtime setup; `status` skips locking.

Profile syntax, unknown-field and duplicate-field errors include the file and
line number. Unexpected local command failures report the phase, profile,
action and exit code while preserving the command's exit status. Explicit
validation errors retain their specific messages. SSH and XAuth handoffs use
`exec`, so their diagnostics and exit status come from the delegated process.
Shell tracing is disabled, including when invoked with `bash -x`; the launcher
does not print profile values or command text in its failure summary.

## Profile fields

Copy a template from `examples/profiles/` into a consumer's
`profiles/<name>/profile.env` and set its gateway and a stable private TCP
health endpoint. Profiles contain literal, unquoted `KEY=value` lines; shell
expressions are not executed. Empty lines and lines beginning with `#` are
allowed. Unknown and duplicate fields are errors. Names use lowercase
letters/digits separated by single hyphens, up to 48 characters. There is no
case folding or key normalization. User-defined Compose names are rejected.

Supported keys are `VPN_BACKEND`, `VPN_SERVER`, `VPN_HEALTH_HOST`,
`VPN_HEALTH_PORT`, `VPN_IMAGE`, `VPN_DNS`, `VPN_FORCE_ENCAPS`,
`VPN_IKE_PROPOSALS`, `VPN_ESP_PROPOSALS`, `VPN_SSH_HOST`, `VPN_REMOTE_ROOT`
`VPN_REMOTE_PROFILES_DIR`, `VPN_WATCHDOG_IMAGE`, `VPN_WATCHDOG_INTERVAL`
and `VPN_WATCHDOG_COOLDOWN`.

The XAuth backend requires an IPv4 gateway literal and uses Aggressive Mode
and the gateway-assigned address. Its default local image tag is
`vpn-sidecar-ipsec-xauth:local`. L2TP uses transport mode and PPP and requires
an explicit image value. IKE/ESP proposals are comma separated.

## Remote execution

When `VPN_SSH_HOST` is set, also set both paths:

```text
VPN_SSH_HOST=vpn-host
VPN_REMOTE_ROOT=/srv/vpn-sidecar/source
VPN_REMOTE_PROFILES_DIR=/srv/example-project/vpn/profiles
```

Provision the reusable source at `VPN_REMOTE_ROOT` and the consumer profiles at
`VPN_REMOTE_PROFILES_DIR`. These are separate directories. The launcher sends
only the selected profile name and operation over SSH; it loads the remote
consumer's profile on that host. Keep local and remote profile settings in
sync. Remote paths must be absolute without spaces. Runtime state is under
`/srv/example-project/vpn/instances/`, outside the shared checkout.

## Credentials and connection lifecycle

For XAuth, create `instances/<name>/secrets/vpn.credentials` on the Linux host
using `containers/ipsec-xauth/vpn.credentials.example` as a template. Set literal
`USERNAME`, `PASSWORD` and `PRE_SHARED_KEY` fields. Restrict the secrets
directory to mode 700 and the credential file to 600. For L2TP, create
`username`, `password` and `psk` files under that same secrets directory.
Never put credentials in source control, profile settings or shell history.
Add the consumer's `instances/` directory to its ignore files.

The XAuth code is requested through a hidden terminal prompt. `connect` reuses
a healthy session or waits for MFA, verifies private TCP access, and starts the
application. Failed new connections are cleaned up. Docker logging is disabled;
sanitized logs and the temporary MFA file belong to the instance's `run/`.
The application shares `service:vpn` networking.

XAuth uses Alpine 3.22 and locally patched strongSwan 5.9.14. It has no
post-disconnection kill switch or unattended MFA recovery. The shared watchdog
reports failed health/private TCP probes and requests manual reconnection; it
does not restart XAuth containers or initiate MFA. The Ansible role provisions both backends; XAuth login remains interactive. See the [L2TP backend guide](../containers/l2tp-ipsec/README.md)
for host modules, DNS, kill switch and deployment prerequisites. Both
backends require a Linux Docker host. Health does not prove authenticated SSH.

## Shared watchdog lifecycle

The launcher includes the [watchdog overlay](../watchdog/README.md) and saves a
fully resolved, image-only `instances/<name>/compose.yaml` for recovery. Its
path contains non-secret connection settings and host mount paths. The watchdog
uses that manifest through the host Docker socket, with runtime, source and
credentials mounted read-only at the same absolute paths. Keep those paths
stable. It runs outside the VPN network namespace and needs no host PID mount.

Every `connect` includes the watchdog; `disconnect` removes the whole project.
Use Docker Compose with the runtime manifest to inspect `logs watchdog`, stop
the watchdog for maintenance, or restart it. Named volumes retain its cooldown
and pending-recovery state across recreation. `down --volumes` resets that
state and is suitable only when deliberately discarding the instance.

Watchdog mode follows the backend: automatic for L2TP, manual for XAuth. The
default interval is 30 seconds and cooldown 300 seconds. Profile image/interval/
cooldown fields override those values; mode is not user-overridable. The
watchdog reports status changes to Docker logs; it does not send notifications.

## Consumer SSH integration

See [accessing private services](remote-services.md) for a complete VM/destination
SSH configuration and an HTTP forwarding example using the application service.

Keep SSH aliases and ProxyCommands in the consumer project. Resolve the VPN
container by Compose project/service labels instead of a fixed container name:

```sh
sudo -n docker ps --filter label=com.docker.compose.project=vpn-example --filter label=com.docker.compose.service=vpn --format '{{.ID}}'
```

Use the returned single container ID with `docker exec -i ID nc HOST PORT`.
The workstation SSH client retains destination authentication and host-key
verification. Choose unique local listeners and service-forward names as well.
Consumer-specific installation and cutover instructions belong in that project.

## Verification

```sh
uv run --no-project --with pyyaml --with jinja2 python -m unittest discover -s tests -v
expect containers/ipsec-xauth/tests/run.exp local "$PWD/containers/ipsec-xauth"
expect containers/ipsec-xauth/tests/connect-lifetime.exp established
expect containers/ipsec-xauth/tests/connect-lifetime.exp pending
# The pending scenario must return status 2.
```

CLI regressions require Expect and Linux `flock`; on macOS run them on Linux
using `expect .../tests/run.exp SSH_HOST REMOTE_BACKEND_DIRECTORY`. Tests use fake
Docker and fake credentials. Live negotiation, MFA and authenticated access
require separate deployment checks.
