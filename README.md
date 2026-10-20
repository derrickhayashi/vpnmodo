# vpnmodo

Manage VPN connections from your Mac through one local Ubuntu VM created by vpnmodo.
The Mac runs the `vpnmodo` CLI. A containerized `vpnmodo-controller` on the VM
stores profiles and credentials, starts the VPN containers, and monitors them.
Control traffic uses SSH; there is no HTTP API port to expose.

```text
Mac: vpnmodo CLI / OpenSSH
  └─ SSH → your Ubuntu VM
             ├─ controller container + private profile/credential storage
             └─ VPN container ← shared network → application helper container
                                                    └─ private network
```

Managed profiles support **L2TP/IPsec** with username/password and PSK, and
**IPsec/XAuth** with username/password, PSK and emailed MFA. XAuth fails closed
when IPsec state is lost and requires an explicit login with fresh MFA to recover.

The supported path is **Apple Silicon Mac → vpnmodo-managed Lima VM**. Setup
creates Ubuntu, Docker and the controller. Docker Desktop, Go and
Ansible are not needed on the Mac.

This is a private release candidate. See the [validation record](docs/release-validation.md)
for qualification and the unresolved intermittent startup/session observations
[#19](https://github.com/derrickhayashi/vpn-sidecars-next/issues/19) and
[#23](https://github.com/derrickhayashi/vpn-sidecars-next/issues/23).

- [Installation](#installation): install the CLI, prepare the VM and create a profile once.
- [Usage](#usage): connect, access private services and disconnect.

## Installation

On an **Apple Silicon Mac**, download one file:
[**vpnmodo-darwin-arm64**](https://github.com/derrickhayashi/vpn-sidecars-next/releases/download/v0.1.0-rc.9/vpnmodo-darwin-arm64).
Sign in to GitHub to download this private release.

Install it and create your environment:

```sh
mkdir -p "$HOME/.local/bin"
install -m 755 "$HOME/Downloads/vpnmodo-darwin-arm64" "$HOME/.local/bin/vpnmodo"
export PATH="$HOME/.local/bin:$PATH"
vpnmodo setup
```

Add `export PATH="$HOME/.local/bin:$PATH"` to `~/.zshrc` once for new terminals.

Setup installs Lima through Homebrew if needed, creates the managed Ubuntu VM,
installs Docker and the release images, starts the controller and checks readiness.
Running setup again reuses this same environment. There is no VM selection,
manual host registration or separate provisioning-file download.

If Homebrew is missing, install it from [brew.sh](https://brew.sh) and run setup
again. The images are private, so setup asks for a GitHub username and a token
with `read:packages`. Your account needs package access.

When setup reports ready, create a VPN profile from JSON below. Profiles always
use the managed VM; no `--host` option is needed.

## VPN configuration

On the **Mac**, create a private directory outside any Git checkout:

```sh
umask 077
mkdir -p "$HOME/vpnmodo-private"
chmod 700 "$HOME/vpnmodo-private"
nano "$HOME/vpnmodo-private/work.json"
```

Save **one** of the following JSON profiles as `work.json`. Replace the example
server, DNS and health addresses with your VPN's approved settings before
creating the profile. `health` must identify a TCP service reachable through the
VPN, such as a private SSH server; it is used to check tunnel readiness.

**L2TP/IPsec:** the gateway may be a DNS name or IPv4 address.

```json
{
  "schema_version": 1,
  "name": "work",
  "backend": {
    "type": "l2tp-ipsec",
    "config": {
      "server": "vpn.example.com",
      "dns": ["192.0.2.53"],
      "force_encapsulation": true
    }
  },
  "health": {"host": "192.0.2.10", "port": 22}
}
```

**IPsec/XAuth:** the gateway must be an IPv4 address. Use this backend for the
supported emailed-code MFA flow.

```json
{
  "schema_version": 1,
  "name": "work",
  "backend": {
    "type": "ipsec-xauth",
    "config": {
      "server": "203.0.113.10",
      "dns": ["192.0.2.53"]
    }
  },
  "health": {"host": "192.0.2.10", "port": 22}
}
```

Then, on the **Mac**:

```sh
chmod 600 "$HOME/vpnmodo-private/work.json"
vpnmodo profile create work \
  --file "$HOME/vpnmodo-private/work.json"
vpnmodo profile inspect work
```

Enter username, password and pre-shared key at the hidden prompts. Credentials
are separate from the profile JSON. The VM stores them in protected files
(0700 directories, 0600 files); SSH encrypts transport. Storage is plaintext at
rest and readable by the VM/Docker administrator. The Mac registry stores
references rather than a VPN credential copy. Keep real configuration and
credentials out of this repository. See [automation and profile maintenance](docs/onboarding.md)
for protected stdin/file-descriptor input and updates.

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
prompt. L2TP does not ask for an emailed code. An already healthy managed session
is reused. XAuth will not silently start another MFA login after session loss.

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
current healthy application container; it does not start a VPN login or ask for
MFA. Run `vpnmodo up work` first if the VPN is disconnected.

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
chmod 600 "$HOME/vpnmodo-private/services.json"
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
| `vpnmodo service stop work` | Stop owned local forwards, keeping the VPN connected |
| `vpnmodo down work` | Stop owned forwards and disconnect this VPN; keep its profile/credentials |
| `vpnmodo profile help` | Show profile creation, update and credential-rotation commands |

### Troubleshooting

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
[issue #23](https://github.com/derrickhayashi/vpn-sidecars-next/issues/23).

## Existing launchers, architecture and development

Backend and legacy implementation references include the [legacy deployment guide](docs/deployment.md),
[launcher profiles](docs/profiles.md),
[watchdog](watchdog/README.md) and [legacy service forwards](docs/remote-services.md).
[Tailscale](containers/tailscale/README.md) is a standalone Compose backend;
it is not a managed `vpnmodo` profile type.

For contributors, see [development boundaries](docs/development.md),
[implementation requirements](docs/implementation-requirements.md),
[controller internals](containers/controller/README.md),
[profile storage](docs/design/002-profile-storage.md),
[recovery policy](docs/design/004-recovery-policy.md),
[release builds/upgrades](docs/releases.md) and the
[isolated Linux lab](tests/local-lab/README.md). The bundled
[ensure-vpn skill](.agents/skills/ensure-vpn/SKILL.md) covers recovery of existing
consumer deployments.

Run local checks from a source checkout with Go 1.27 and `uv` installed:

```sh
go test -race ./...
go vet ./...
uv run --no-project --with pyyaml --with jinja2 --with ansible-core python -m unittest discover -s tests -v
scripts/check-secrets
```

Compose checks require Docker Compose; protocol acceptance uses the isolated
Linux lab separately. GitHub Actions remains disabled. Customer configuration,
credentials, logs and runtime state belong outside this reusable repository.
