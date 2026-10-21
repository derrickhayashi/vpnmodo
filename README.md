# vpnmodo

Ever needed to connect to multiple sites over VPN at the same time, while staying
connected to your corporate VPN? Then **vpnmodo is for you**.

Keep each site's VPN in its own isolated container inside a local VM, and reach
private services through SSH or local forwards. Site VPN routes and DNS stay
separate from your Mac's networking, so you can work across sites without
switching your computer's VPN connection.

Manage VPN connections from your Mac through one local Ubuntu VM created by vpnmodo.
The Mac runs the `vpnmodo` CLI. A containerized `vpnmodo-controller` on the VM
stores profiles and credentials, starts the VPN containers, and monitors them.
Control traffic uses SSH; there is no HTTP API port to expose.

```text
Mac: vpnmodo CLI / OpenSSH
  └─ SSH → your Ubuntu VM
             ├─ controller container + private profile/credential storage
             └─ VPN container ← shared network → vpntools container
                                                    └─ private network
```

Managed profiles support **L2TP/IPsec** with username/password and PSK,
**IPsec/XAuth** with username/password, PSK and emailed MFA, and
**IKEv2/EAP-MSCHAPv2** with username/password and a CA-verified server certificate.

The supported path is **Apple Silicon Mac → vpnmodo-managed Lima VM**. Setup
creates Ubuntu, Docker and the controller. You do not need Docker Desktop or
Go on the Mac.

This is a prerelease. See [supported platforms and limitations](docs/supported-platforms.md)
and [known issues](docs/known-issues.md) before installation.
For [IKEv2 with EAP-MSCHAPv2](docs/ikev2.md), choose a release that includes the
IKEv2 image.

- [Installation](#installation): install the CLI, prepare the VM and create a profile once.
- [Usage](#usage): connect, access private services and disconnect.
- [IKEv2 configuration](#ikev2eap-mschapv2): configure the gateway, server identity and CA trust.

## Installation

On an **Apple Silicon Mac**, open
[Releases](https://github.com/derrickhayashi/vpnmodo/releases), choose a version
and download **vpnmodo-darwin-arm64**. Use the latest stable release for normal
use; versions marked **Pre-release** are testing candidates.
The source and release downloads are public.

Install it:

```sh
mkdir -p "$HOME/.local/bin"
install -m 755 "$HOME/Downloads/vpnmodo-darwin-arm64" "$HOME/.local/bin/vpnmodo"
export PATH="$HOME/.local/bin:$PATH"
vpnmodo version
```

Add `export PATH="$HOME/.local/bin:$PATH"` to `~/.zshrc` once for new terminals.

Setup creates the managed Ubuntu VM, installs Docker and the release images,
starts the controller and checks readiness. It installs Lima through Homebrew
if needed; install [Homebrew](https://brew.sh) first if it is missing.

Prepare the VM with the public release images:

```sh
vpnmodo setup
```

Repeated setup reuses the same VM. Setup does not read VPN configuration or start
a VPN connection. There is no VM selection or separate setup-file download.
If you use a private registry configuration instead, supply a separate optional
`--registry-auth FILE` containing its GitHub `username` and `token`; keep that file
outside Git. Registry credentials are not part of a VPN profile.

## VPN configuration

On the **Mac**, create a directory outside any Git checkout:

```sh
mkdir -p "$HOME/vpnmodo-private"
nano "$HOME/vpnmodo-private/work.json"
```

Save **one** of the following JSON profiles as `work.json`. Replace the example
server, DNS and health addresses with your VPN's approved settings. Fill in the
VPN username and password, plus the pre-shared key for L2TP/XAuth or the CA
certificate bundle for IKEv2. All three use a `credentials` object; registry access
stays in the separate optional registry-auth file. `health` must identify a TCP
service reachable through the VPN, such as a private SSH server; it is used to
check tunnel readiness.

### L2TP/IPsec

The gateway may be a DNS name or IPv4 address.

```json
{
  "schema_version": 1,
  "name": "work",
  "backend": {
    "type": "l2tp-ipsec",
    "config": {
      "server": "vpn.example.com",
      "dns": ["192.0.2.53"],
      "force_encapsulation": true,
      "ike_proposals": ["aes256-sha256-modp2048", "aes256-sha1-modp2048"],
      "esp_proposals": ["aes256-sha256", "aes256-sha1"]
    }
  },
  "health": {"host": "192.0.2.10", "port": 22},
  "credentials": {
    "username": "YOUR_VPN_USERNAME",
    "password": "YOUR_VPN_PASSWORD",
    "psk": "YOUR_VPN_PSK"
  }
}
```

### IPsec/XAuth

The gateway must be an IPv4 address. Use this backend for the supported
emailed-code MFA flow.

```json
{
  "schema_version": 1,
  "name": "work",
  "backend": {
    "type": "ipsec-xauth",
    "config": {
      "server": "203.0.113.10",
      "dns": ["192.0.2.53"],
      "ike_proposals": ["aes128-sha1-modp1536", "aes256-sha256-modp1536"],
      "esp_proposals": ["aes128-sha1-modp1536", "aes256-sha1-modp1536"]
    }
  },
  "health": {"host": "192.0.2.10", "port": 22},
  "credentials": {
    "username": "YOUR_VPN_USERNAME",
    "password": "YOUR_VPN_PASSWORD",
    "psk": "YOUR_VPN_PSK"
  }
}
```

The optional `ike_proposals` and `esp_proposals` arrays select the algorithms
offered for IKE negotiation and ESP data protection. Use your gateway's approved
settings; these examples illustrate configuration, not a recommended security
policy. Omit either array to use that backend's defaults. See
[proposal settings](docs/profiles.md#ike-and-esp-proposals) for the defaults,
syntax, ordering and additional strongSwan defaults offered by L2TP. Both of
these backends use IKEv1. IKEv2 uses its own backend and proposal policy below.

### IKEv2/EAP-MSCHAPv2

Use `ipsec-ikev2` for username/password EAP-MSCHAPv2 with a certificate-authenticated
server. This backend requires a release containing the IKEv2 image. It supports
IPv4 and has no PSK, client-certificate authentication or interactive MFA.

The gateway may be a DNS name or IPv4 address. Set `server_identity` to the
expected IKE identity configured on the gateway and covered by its certificate;
it can differ from the gateway address. Client and EAP identities default to the
VPN username. Set `client_identity` or `eap_identity` only if your gateway requires
another identity.

```json
{
  "schema_version": 1,
  "name": "work",
  "backend": {
    "type": "ipsec-ikev2",
    "config": {
      "server": "vpn.example.com",
      "server_identity": "vpn.example.com",
      "dns": ["192.0.2.53"],
      "ike_proposals": ["aes256-sha256-modp2048"],
      "esp_proposals": ["aes256-sha256"]
    }
  },
  "health": {"host": "192.0.2.10", "port": 22},
  "credentials": {
    "username": "YOUR_VPN_USERNAME",
    "password": "YOUR_VPN_PASSWORD",
    "ca_cert": "REPLACED_FROM_CA_FILE_BELOW"
  }
}
```

Save your administrator-provided CA certificate bundle as
`$HOME/vpnmodo-private/ca.pem`. After saving the profile as `work.json`, copy the
PEM contents into `credentials.ca_cert` before importing. This command handles
JSON newline escaping without printing the profile or credentials:

```sh
python3 - <<'PYCODE'
import json
from pathlib import Path
private = Path.home() / "vpnmodo-private"
path = private / "work.json"
profile = json.loads(path.read_text())
profile["credentials"]["ca_cert"] = (private / "ca.pem").read_text()
path.write_text(json.dumps(profile, indent=2) + "\n")
PYCODE
```

The bundle must contain CA certificates only, with no private keys. IKEv2 trusts
only this bundle and verifies the server certificate and expected identity.
See the [IKEv2 guide](docs/ikev2.md) for CA limits, proposal policy, DNS,
troubleshooting and current limitations.

### Import your profile

Then, on the **Mac**, import the settings and credentials together:

```sh
vpnmodo profile create work \
  --file "$HOME/vpnmodo-private/work.json"
vpnmodo profile inspect work
```

Input JSON files can use normal file permissions; no permission setup commands
are required. vpnmodo stores its managed credentials and state with private
permissions automatically.

This VPN file supplies settings and credentials for profile creation. Keep it
private and outside Git: it contains plaintext secrets. Setup and profile creation
never prompt for credentials. The VM stores
VPN credentials separately from its profile metadata in protected files, and SSH
encrypts transport. Profile output and local references omit the secrets.
Only XAuth prompts for a fresh MFA code when connecting; L2TP and IKEv2 use the
saved credentials without a prompt.
See [profile maintenance](docs/onboarding.md) for updates and credential rotation.

## Usage

Use your configured profile name in place of `work` below. Run these commands on the **Mac**.

### Connect and check the VPN

On the **Mac**:

```sh
vpnmodo up work
vpnmodo status work
vpnmodo doctor work
```

For XAuth, `up` asks for the newest emailed code for this attempt through a hidden
prompt. L2TP and IKEv2 do not ask for an emailed code. An already healthy managed
session is reused. XAuth will not silently start another MFA login after session loss.

A successful VPN health check proves the configured TCP endpoint is reachable;
it does not establish an SSH login or HTTP application login. Without configured
services, reports show VPN readiness separately from end-to-end service readiness.

### Access a private SSH server

The VPN runs in the VM's container network. Your Mac does not automatically route
all traffic through it. On the **Mac**, add a private destination to
`~/.ssh/config`; replace the address, account and identity. `ProxyCommand` needs
the **absolute** path to your installed binary, as shown by
`command -v vpnmodo`; replace `/Users/YOUR_USER` below.

```sshconfig
Host private-server
    HostName 192.0.2.10
    User PRIVATE_USER
    IdentityFile ~/.ssh/id_ed25519
    IdentitiesOnly yes
    ProxyCommand /Users/YOUR_USER/.local/bin/vpnmodo proxy work %h %p
    StrictHostKeyChecking yes
    ForwardAgent no
```

Obtain this destination's host-key fingerprint from its administrator. Enroll it
once with `ssh -o StrictHostKeyChecking=ask private-server`, comparing the
fingerprint before accepting. Then verify normal noninteractive authentication:

```sh
ssh -o BatchMode=yes private-server hostname
```

Destination SSH keys and authentication stay on the Mac. `proxy` attaches to the
current healthy vpntools container; it does not start a VPN login or ask for
MFA. Run `vpnmodo up work` first if the VPN is disconnected.

See [how the proxy and controller work](docs/internals.md) for the profile lookup,
JSON-RPC requests over SSH, container health checks and traffic path.

### Optionally forward a private web service

For a dashboard listening on the **private SSH server's** `127.0.0.1:8081`, save
this as `~/vpnmodo-private/services.json` on the **Mac**:

```json
{
  "schema_version": 1,
  "services": [
    {
      "name": "dashboard",
      "ssh_target": "private-server",
      "local_port": 18081,
      "remote_host": "127.0.0.1",
      "remote_port": 8081
    }
  ]
}
```

The SSH alias must already authenticate with a verified host key. Python 3 on
that private server is required for endpoint verification. Then:

```sh
vpnmodo service configure work --file "$HOME/vpnmodo-private/services.json"
vpnmodo up work
vpnmodo doctor work
curl --fail http://127.0.0.1:18081/
```

Open `http://127.0.0.1:18081` in your Mac browser. Set the ports and HTTP/HTTPS
scheme to match your application. The forward binds only to Mac loopback.
A reachable endpoint does not replace application authentication. See the
[service guide](docs/design/007-service-access.md) for other targets and diagnostics.

### Disconnect and everyday commands

Disconnect without deleting the saved profile or credentials:

```sh
vpnmodo down work
```

| Command on the Mac | Purpose |
| --- | --- |
| `vpnmodo up work` | Ensure the VPN and start configured local service forwards |
| `vpnmodo status work` | Inspect current VPN and service state |
| `vpnmodo doctor work` | Probe VM, controller, VPN and configured services |
| `vpnmodo logs work --since 24h` | Read persistent connection history and sanitized diagnostics |
| `vpnmodo service stop work` | Stop owned local forwards, keeping the VPN connected |
| `vpnmodo down work` | Stop owned forwards and disconnect this VPN; keep its profile/credentials |
| `vpnmodo profile help` | Show profile creation, update and credential-rotation commands |

### Troubleshooting

Use `vpnmodo logs work --since 24h` for disconnect/recovery history across all
VPN backends. See [VPN logging](docs/logging.md) for storage, retention, privacy,
pagination and diagnostic limitations. Backend diagnostics require rebuilt
controller/backend images and a new explicit connection.

If VM SSH fails, check VM reachability, its verified host key and your SSH key.
If the environment is not ready, run `vpnmodo setup` again before trying a VPN login. If the VPN is healthy but private SSH fails, check destination
host-key trust and authentication separately. If `doctor` reports a local port
conflict, choose an unused port after stopping owned forwards.

If a CLI connection drops during login, inspect the existing operation before
starting another login: the VM operation may still be running. Use the
[operation guide](docs/design/003-persistent-operations.md) to inspect/watch it and
the [MFA guide](docs/design/006-managed-xauth.md) to submit a code to its existing
challenge. An XAuth post-MFA exit is a failed attempt, not proof the code was
wrong; known unresolved lifecycle failures are tracked in
[known issues guide](docs/known-issues.md#xauth-session-state-loss).

## More documentation

- [Onboarding and profile maintenance](docs/onboarding.md): update settings, rotate credentials and remove profiles.
- [IKEv2 with EAP-MSCHAPv2](docs/ikev2.md): server identity, CA trust, proposals and backend limitations.
- [Known issues and regressions](docs/known-issues.md): symptoms, affected versions and recovery steps.
- [VPN and SSH proxy internals](docs/internals.md): how the VM, controller and vpntools container carry traffic.
- [Setup configuration](docs/design/008-setup-configuration.md): optional `setup --config` input, currently ignored.
- [Releases](docs/releases.md): downloads, automatic builds and stable promotion.
- [Supported platforms](docs/supported-platforms.md): platform requirements and current limitations.
- [Local development](docs/development.md): build from source and run tests.

See [updating an existing installation](docs/upgrading.md) for the in-place CLI and container upgrade workflow.
