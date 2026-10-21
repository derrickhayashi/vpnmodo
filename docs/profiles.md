# VPN profiles and proposal settings

For managed installation and complete profile JSON examples, start with
[VPN configuration](../README.md#vpn-configuration). Create a profile on the Mac
with `vpnmodo profile create work --file "$HOME/vpnmodo-private/work.json"`.
Keep this plaintext input file outside Git. To change an existing profile, use
the [profile update workflow](onboarding.md#update-a-profile-or-rotate-credentials).

## IKE and ESP proposals

Both `ipsec-xauth` and `l2tp-ipsec` accept optional `ike_proposals` and
`esp_proposals` arrays inside `backend.config`. The complete
[XAuth and L2TP JSON examples](../README.md#vpn-configuration) show both lists.
These backends use IKEv1: XAuth uses Aggressive Mode with PSK/XAuth and emailed
MFA; L2TP uses Main Mode with PSK and IPsec transport mode around L2TP/PPP.
IKEv2 support is separate planned backend work; these settings do not change
the IKE version or authentication method.

IKE proposals negotiate protection for the IKE control connection: encryption,
integrity/hash and Diffie-Hellman (DH) group. For example,
`aes256-sha256-modp2048` means AES-CBC with a 256-bit key, SHA-256 and a 2048-bit
MODP DH group; strongSwan derives the matching PRF from the hash.
ESP proposals negotiate protection for the IPsec data traffic: encryption and
integrity, plus an optional DH group for a separate Perfect Forward Secrecy
(PFS) exchange during IKEv1 Quick Mode setup and rekeying. Thus `aes256-sha256`
omits a separate ESP DH exchange, while `aes128-sha1-modp1536` requests one.
See the [strongSwan cipher-suite reference](https://docs.strongswan.org/docs/latest/config/proposals.html)
and [IKEv1 option semantics](https://raw.githubusercontent.com/strongswan/strongswan/5.9.14/man/ipsec.conf.5.in).

### Profile defaults

Each row lists the vpnmodo defaults in order. Omitting a list, or supplying
`[]`, uses that row. `null` is rejected by the JSON decoder. A nonempty list
**replaces that profile default list**, independently of the other list. It
does not append to the row.

| Backend | Setting | Ordered profile defaults |
| --- | --- | --- |
| `ipsec-xauth` | `ike_proposals` | `aes128-sha1-modp1536`, `aes256-sha256-modp1536` |
| `ipsec-xauth` | `esp_proposals` | `aes128-sha1-modp1536`, `aes256-sha1-modp1536` |
| `l2tp-ipsec` | `ike_proposals` | `aes256-sha256-modp2048`, `aes256-sha1-modp2048`, `aes256-sha1-modp1024`, `aes128-sha1-modp1024`, `3des-sha1-modp1024` |
| `l2tp-ipsec` | `esp_proposals` | `aes256-sha256`, `aes256-sha1`, `aes128-sha1`, `3des-sha1` |

These are compatibility defaults, including legacy algorithms. Get an approved
list from the gateway administrator rather than treating the defaults as a
security recommendation. Engine defaults can extend the L2TP lists as described
below.

### Syntax and ordering

Use one dash-separated strongSwan proposal per JSON array entry, in preferred
order. For these IKEv1 backends, put one algorithm of each kind in each proposal;
use separate entries for alternative combinations. For example,
`["aes256-sha256-modp2048", "aes128-sha256-modp2048"]` offers two IKE proposals.
Do not put commas or spaces inside an entry. Names are lowercase; the managed
validator permits only letters, digits and single separating hyphens, with an
optional final `!`. It allows at most 32 entries per list and 256 characters per
entry. Some strongSwan keywords containing underscores are therefore unavailable
through this profile format.

Profile validation checks the string format, not whether an algorithm name,
combination or plugin is usable. strongSwan parses the proposals at connection
time; crypto plugins, the Linux kernel and the gateway must support them.
Valid JSON alone does not prove that negotiation will succeed.

vpnmodo preserves array order when passing proposals to the client. Ordering
expresses preference; the gateway still selects a mutually acceptable proposal
according to its policy. A strongSwan responder normally prefers its own
configured order. For XAuth Aggressive Mode, the initial DH exchange uses the
first configured IKE DH group; put the gateway's required group first rather
than expecting later groups to provide automatic fallback. See the pinned
[Aggressive Mode exchange](https://github.com/strongswan/strongswan/blob/5.9.14/src/libcharon/sa/ikev1/tasks/aggressive_mode.c)
and [DH selection](https://github.com/strongswan/strongswan/blob/5.9.14/src/libcharon/config/ike_cfg.c).

### Engine defaults and strict lists

The invocation matters; the two backends do not interpret `!` identically:

- **XAuth:** the managed wrapper passes each entry as a repeated
  `charon-cmd --ike-proposal` or `--esp-proposal` option with
  `--profile ikev1-xauth-psk-am`. In the pinned strongSwan 5.9.14 client,
  nonempty option lists replace the engine defaults too. vpnmodo always supplies
  both lists, using the profile defaults above if needed, so the client does
  **not** append extra default proposals. Do not add `!`: although the managed
  profile validator accepts it, `charon-cmd` passes it to the algorithm parser
  and rejects it. See the [pinned client implementation](https://github.com/strongswan/strongswan/blob/5.9.14/src/charon-cmd/cmd/cmd_connection.c).
- **L2TP:** the entrypoint joins each array with commas and writes it directly
  to `ike=` or `esp=` in `ipsec.conf`, then uses `ipsec start` / `ipsec up`.
  strongSwan's stroke configuration appends engine defaults unless the **whole
  comma-separated list ends in `!`**. This applies even when you supply an
  explicit profile list. Engine defaults are separate from vpnmodo's table and
  depend on the installed strongSwan version and crypto support; the backend
  installs Ubuntu 24.04's strongSwan package. To restrict either list, append
  `!` only to its final array entry. For example,
  `"ike_proposals": ["aes256-sha256-modp2048!"]` and
  `"esp_proposals": ["aes256-sha256!"]` suppress extra defaults for both.
  With multiple entries use `["aes256-sha256", "aes128-sha256!"]`, not an
  exclamation mark on every entry. See the [stroke parser](https://github.com/strongswan/strongswan/blob/5.9.13/src/libcharon/plugins/stroke/stroke_config.c)
  and [ipsec.conf reference](https://raw.githubusercontent.com/strongswan/strongswan/5.9.14/man/ipsec.conf.5.in).

The local XAuth patches do not change proposal parsing or default selection.
For the runtime mapping, see [profile defaults and validation](../internal/profile/profile.go),
the [managed manifest](../internal/managedvpn/manifest.go), the
[XAuth invocation](../containers/ipsec-xauth/managed/vpn-xauth-expect) and the
[L2TP entrypoint](../containers/l2tp-ipsec/scripts/docker-entrypoint.sh).

## Legacy launcher profiles

For standalone Linux launcher deployment, follow the
[Linux VM deployment guide](deployment.md). The remaining sections describe its
`profile.env` format, rather than managed profile JSON.

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
an explicit image value. `VPN_IKE_PROPOSALS` and `VPN_ESP_PROPOSALS` are the
comma-separated equivalents of the managed JSON arrays. Use literal values
without quotes, for example:

```text
VPN_IKE_PROPOSALS=aes256-sha256-modp2048,aes256-sha1-modp2048
VPN_ESP_PROPOSALS=aes256-sha256,aes256-sha1
```

Both legacy backends use the same proposal defaults and engine behavior
described [above](#ike-and-esp-proposals). To change proposals, disconnect the
legacy instance, edit its consumer-owned `profile.env`, run `validate`, then
`connect` again. Launcher validation does not check algorithm availability or
prove negotiation. Use managed `profile update` only for managed JSON profiles.

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
