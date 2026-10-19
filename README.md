# vpnmodo

Manage VPN connections from your Mac using your own local or remote Ubuntu VM.
The Mac runs the `vpnmodo` CLI. A containerized `vpnmodo-controller` on the VM
stores profiles and credentials, starts the VPN containers, and monitors them.
Control traffic uses verified SSH; there is no HTTP API port to expose.

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

The tested installation path is **macOS ARM64 → Ubuntu 24.04 ARM64**. You supply
the VM, SSH account and approved VPN settings. The container image bundle runs
inside that VM; it does not contain or create a UTM/Ubuntu VM disk image. Docker
Desktop and Go are not needed on a teammate's Mac when installing the archives.

This is a private release candidate. See the [validation record](docs/release-validation.md)
for qualification and the unresolved intermittent startup/session observations
[#19](https://github.com/derrickhayashi/vpn-sidecars-next/issues/19) and
[#23](https://github.com/derrickhayashi/vpn-sidecars-next/issues/23).

## 1. Obtain the installation files

There is no published GitHub release, registry image or Homebrew tap yet. Ask the
maintainer for these private candidate files and their trusted SHA-256 checksums:

| File | Used on |
| --- | --- |
| `vpnmodo-v0.1.0-rc.6-darwin-arm64.tar.gz` | Mac: CLI and installer |
| `SHA256SUMS-cli` | Mac: CLI/source archive checksums |
| `vpnmodo-v0.1.0-rc.6-linux-arm64-images.tar.gz` | VM: controller, both VPN backends and application helper |
| `SHA256SUMS-linux-arm64` | VM: container bundle checksum |
| `vpnmodo-v0.1.0-rc.6-source.tar.gz` | Matching source and documentation |

Use the matching artifact set. The tested candidate reports
`v0.1.0-rc.6+git.990dc2fe7cae`. The maintainer can produce these files using the
[release build instructions](docs/releases.md); the commands below install them.
Checksums must come through a trusted channel, not just an unverified download.

On the **Mac**, put the files in `~/Downloads/vpnmodo-v0.1.0-rc.6`, then:

```sh
artifact_dir="$HOME/Downloads/vpnmodo-v0.1.0-rc.6"
setup_dir="$HOME/.local/share/vpnmodo-setup/v0.1.0-rc.6"
umask 077
mkdir -p "$setup_dir/cli"
shasum -a 256 "$artifact_dir/vpnmodo-v0.1.0-rc.6-darwin-arm64.tar.gz"
```

Compare the printed hash with that archive's entry in `SHA256SUMS-cli`. Stop if
it differs. The checksum file also lists other artifacts; you do not need the
Intel archive to install on an ARM64 Mac. After the hash matches:

```sh
tar -xzf "$artifact_dir/vpnmodo-v0.1.0-rc.6-darwin-arm64.tar.gz" \
  -C "$setup_dir/cli"
sh "$setup_dir/cli/install.sh" --prefix "$HOME/.local"
export PATH="$HOME/.local/bin:$PATH"
vpnmodo version
```

Add `export PATH="$HOME/.local/bin:$PATH"` to `~/.zshrc` once to retain it in new
Mac terminals. Installation refuses an existing binary; use the explicit
[upgrade procedure](docs/releases.md#installation-and-upgrades) for replacement.
The candidate is unsigned and not notarized; use your team's approved macOS
software policy. The installer does not change Gatekeeper settings.

## 2. Prepare the Ubuntu VM and SSH access

Follow [prepare an Ubuntu VM](docs/vm-setup.md) to establish verified SSH and run
the dedicated `vpn_sidecar.client.vm_prepare` Ansible role from your Mac. You
supply an existing Ubuntu 24.04 ARM64 VM with Python 3 and administrative access.
The role prepares rootful Docker Engine, Compose, utilities, management-account
access and kernel support without VPN settings or credentials. Ansible host
preparation does not need Docker Desktop on the Mac.

The guide pins the preparation collection separately from the runtime candidate:
runtime commit `990dc2fe7caec4e1b68cf2b54788b996937e66c5` predates the new role.
Preparation does not create a profile or start a controller or VPN. Continue here
after it succeeds; `vpnmodo host setup` installs the controller later.

On the **Mac**, add a VM alias to `~/.ssh/config`, replacing the uppercase values
and choosing your existing SSH identity:

```sshconfig
Host vpn-vm
    HostName VM_ADDRESS
    User VM_USER
    IdentityFile ~/.ssh/id_ed25519
    IdentitiesOnly yes
    StrictHostKeyChecking yes
    ForwardAgent no
```

Enroll the host key only after comparing its fingerprint with the VM console,
as described in the VM guide. Then verify noninteractive access from the **Mac**:

```sh
ssh -o BatchMode=yes vpn-vm 'python3 --version && docker --host unix:///var/run/docker.sock info --format "{{.OSType}}" && docker compose version'
```

All three checks must succeed: Python 3, Docker `linux`, and Compose v2. This
walkthrough uses direct Docker access for the VM account. Docker access gives
administrative authority over the VM. For an account already authorized to use
noninteractive sudo instead, see [sudo mode](docs/onboarding.md#sudo-mode) before
loading images or registering the controller. Substitute that guide's loader
and `host add` commands for the ordinary versions below; do not run both modes.

## 3. Load the container images on the VM

On the **Mac**, transfer the bundle and checksum file:

```sh
ssh vpn-vm 'umask 077; mkdir -p "$HOME/vpnmodo-install/v0.1.0-rc.6"'
scp "$artifact_dir/vpnmodo-v0.1.0-rc.6-linux-arm64-images.tar.gz" \
  "$artifact_dir/SHA256SUMS-linux-arm64" \
  vpn-vm:vpnmodo-install/v0.1.0-rc.6/
ssh vpn-vm
```

In that **VM shell**, verify the checksum before extracting. Continue only if
`sha256sum` reports `OK`:

```sh
umask 077
cd "$HOME/vpnmodo-install/v0.1.0-rc.6"
sha256sum -c SHA256SUMS-linux-arm64
```

After the checksum passes, load the images:

```sh
mkdir -m 700 release
tar -xzf vpnmodo-v0.1.0-rc.6-linux-arm64-images.tar.gz -C release
cd release
python3 install-vm.py > installed-release.json
```

The loader verifies and imports all four images. Continue only if it exits
successfully. Its receipt records the VM's immutable image IDs and
`"activated": false`: loading images has not started a controller or VPN.
Return to the **Mac** with `exit`, then copy the receipt back:

```sh
scp vpn-vm:vpnmodo-install/v0.1.0-rc.6/release/installed-release.json \
  "$setup_dir/installed-release.json"
vpnmodo host add vpn-vm --ssh vpn-vm
vpnmodo host setup vpn-vm --release "$setup_dir/installed-release.json"
vpnmodo host check vpn-vm
```

Expect `storage ready: true` and `Docker ready: true`. If the controller is still
starting, repeat `host check`. Setup uses the receipt to select all four verified
images; it does not import existing profiles or change legacy VPN deployments.
No controller firewall port or registry credentials are needed.

## 4. Create a VPN profile

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

## 5. Connect and check the VPN

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

## 6. Access a private SSH server

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

## 7. Optionally forward a private web service

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

## Everyday commands and troubleshooting

| Command on the Mac | Purpose |
| --- | --- |
| `vpnmodo up work` | Ensure the VPN and start configured local service forwards |
| `vpnmodo status work` | Inspect current VPN and service state |
| `vpnmodo doctor work` | Probe VM, controller, VPN and configured services |
| `vpnmodo service stop work` | Stop owned local forwards, keeping the VPN connected |
| `vpnmodo down work` | Stop owned forwards and disconnect this VPN; keep its profile/credentials |
| `vpnmodo profile help` | Show profile creation, update and credential-rotation commands |

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
