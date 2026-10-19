# vpnmodo

Manage VPN connections from your Mac using your own local or remote Ubuntu VM.
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

The tested platform pair is **macOS ARM64 → Ubuntu 24.04 ARM64**. For a local
VM on Apple Silicon, Lima creates Ubuntu, sets up SSH and installs the release
images. Existing local or remote VMs can use Ansible preparation and an offline
image bundle. Docker Desktop and Go are not needed on the user's Mac.

This is a private release candidate. See the [validation record](docs/release-validation.md)
for qualification and the unresolved intermittent startup/session observations
[#19](https://github.com/derrickhayashi/vpn-sidecars-next/issues/19) and
[#23](https://github.com/derrickhayashi/vpn-sidecars-next/issues/23).

- [Installation](#installation): install the CLI, prepare the VM and create a profile once.
- [Usage](#usage): connect, access private services and disconnect.

## Installation

Run the following commands on your **Mac**, unless stated otherwise.

### 1. Install the CLI

Ask the maintainer for the matching release files and trusted SHA-256 checksums.
The current Lima image manifest uses `v0.1.0-rc.7`. The images are published in
private GHCR; there is no Homebrew tap or downloadable GitHub release yet.

| File | Purpose |
| --- | --- |
| `vpnmodo-v0.1.0-rc.7-darwin-arm64.tar.gz` | Mac CLI and installer |
| `SHA256SUMS-cli` | CLI archive checksums |
| `lima.yaml` | Ubuntu VM configuration |
| `install-vm.py` | Pulls and checks the VM release images |
| `registry-release.json` | Exact image digests for that release |

Put the files in `~/Downloads/vpnmodo-v0.1.0-rc.7`, then:

```sh
artifact_dir="$HOME/Downloads/vpnmodo-v0.1.0-rc.7"
setup_dir="$HOME/vpnmodo-private/setup"
umask 077
mkdir -p "$setup_dir/cli"
chmod 700 "$HOME/vpnmodo-private" "$setup_dir"
shasum -a 256 "$artifact_dir/vpnmodo-v0.1.0-rc.7-darwin-arm64.tar.gz"
```

Compare the hash with that archive's entry in the trusted `SHA256SUMS-cli`.
After it matches:

```sh
tar -xzf "$artifact_dir/vpnmodo-v0.1.0-rc.7-darwin-arm64.tar.gz" \
  -C "$setup_dir/cli"
sh "$setup_dir/cli/install.sh" --prefix "$HOME/.local"
export PATH="$HOME/.local/bin:$PATH"
vpnmodo version
```

Add `export PATH="$HOME/.local/bin:$PATH"` to `~/.zshrc` once to retain it in new
terminals. Installation refuses an existing binary; use the explicit
[upgrade procedure](docs/releases.md#installation-and-upgrades) for replacement.
The candidate is unsigned and not notarized; use your team's approved macOS
software policy. The installer does not change Gatekeeper settings.

### 2. Create the VM and start the controller

Install Lima 2.2.1 or later and copy the matching provisioning files:

```sh
brew install lima
cp "$artifact_dir/lima.yaml" "$artifact_dir/install-vm.py" \
  "$artifact_dir/registry-release.json" "$setup_dir/"
```

The same provisioning files are available in the source checkout; see the
[Lima guide](docs/lima.md#prepare-the-release-inputs) for those copy commands.

The GHCR images are currently private. Follow the
[registry authentication instructions](docs/lima.md#prepare-the-release-inputs)
to create `ghcr-config.json` beside `lima.yaml`. Your GitHub account needs package
read access and a token with `read:packages`. Keep the token outside Git.

Create the VM, register Lima's SSH settings and start the controller:

```sh
limactl start --tty=false --name vpn-vm "$setup_dir/lima.yaml"
scp -F "$HOME/.lima/vpn-vm/ssh.config" \
  lima-vpn-vm:/var/lib/vpnmodo/installed-release.json "$setup_dir/installed-release.json"
vpnmodo host add vpn-vm --ssh lima-vpn-vm \
  --ssh-config "$HOME/.lima/vpn-vm/ssh.config" --sudo
vpnmodo host setup vpn-vm --release "$setup_dir/installed-release.json"
vpnmodo host check vpn-vm
```

Lima creates the `vpnadmin` account and SSH access, installs Docker and required
kernel support, and pulls all four release images. `host setup` starts the
controller using those installed images. No fixed VM IP is needed. For a custom
`LIMA_HOME`, use the SSH config path described in the [Lima guide](docs/lima.md#ssh-and-restart).

**Using an existing VM or an offline bundle?** Follow
[existing VM and offline installation](docs/offline-installation.md) instead of
this Lima step. It uses the Ansible role to prepare the VM and loads images from
a release bundle. Both paths finish with a registered host named `vpn-vm` and a
running controller. Continue below after either path succeeds.

### 3. Create a VPN profile

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
vpnmodo profile create work --host vpn-vm \
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

If adopting an existing deployment, follow the [cutover notes](docs/onboarding.md#adopting-an-existing-deployment)
before connecting. Do not run two recovery owners for the same VPN session.

## Usage

Installation is complete. Use these commands on the **Mac** for everyday access.

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

For a Lima VM, stop it with `limactl stop vpn-vm`. Start it again with
`limactl start vpn-vm`, then run `vpnmodo up work` to reconnect and restore your
configured service forwards.

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
If `host check` fails, check VM Docker access and controller setup before trying
a VPN login. If the VPN is healthy but private SSH fails, check destination
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

Existing standalone deployments remain supported and are adopted explicitly.
For their original setup, use the [legacy deployment guide](docs/deployment.md),
[launcher profiles](docs/profiles.md), [Ansible role](ansible/README.md),
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
