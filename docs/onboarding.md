# Onboarding, profile maintenance and adoption

Start with [README installation](../README.md#installation) to install the Mac
CLI, create and provision a local Ubuntu VM with Lima, and create your VPN
profile. Then use [README usage](../README.md#usage) to connect and access private
services. The platform pair is macOS ARM64 and Ubuntu 24.04 ARM64; each user owns
their Mac and VM.

For an existing VM, use [existing VM and offline installation](offline-installation.md),
which includes Ansible preparation and loading the release image bundle. The
bundle contains four Docker images, not a VM disk image. See
[release packaging](releases.md) for maintainer builds and explicit upgrades.

## Sudo mode

The README's Lima path already uses `--sudo` for its `vpnadmin` account. The
offline guide defaults to direct access to rootful Docker. For an existing VM
account authorized to use noninteractive sudo, verify:

```sh
ssh -o BatchMode=yes vpn-vm 'sudo -n python3 --version && sudo -n docker --host unix:///var/run/docker.sock info --format "{{.OSType}}" && sudo -n docker compose version'
```

All checks must succeed. When loading the verified images on the VM, run this
from the extracted bundle directory instead of the ordinary loader command:

```sh
sudo -n python3 install-vm.py > installed-release.json
```

On the Mac, register the host with sudo mode **before initial setup**:

```sh
vpnmodo host add vpn-vm --ssh vpn-vm --sudo
vpnmodo host setup vpn-vm --release "$HOME/vpnmodo-private/setup/installed-release.json"
vpnmodo host check vpn-vm
```

Fixed VM management commands then use noninteractive sudo, including bootstrap,
the bridge and forwarding. This does not change group membership or sudo policy.
Bootstrap owns state under root's home in this mode; use the same mode for later
access and upgrades. Do not enable sudo mode for a pre-existing user-owned
controller without an explicit state migration. Bootstrap refuses mismatched
storage mounts.

For a separate SSH configuration, add `--ssh-config /absolute/path/to/config` to
`host add`. The path must be absolute. Registration creates a workstation
reference; setup starts the controller and provisions protected VM storage.

## Credential input for automation

Interactive `profile create` and `profile rotate-credentials` use hidden prompts.
Automated callers can use `--credentials-stdin` or `--credentials-fd N`. The input
is a JSON object with `username`, `password` and `psk` fields, supplied through a
protected pipe or an owner-only regular file. For example, if your approved
secret-management process has already created a mode-0600 file outside Git:

```sh
vpnmodo profile create work --host vpn-vm \
  --file "$HOME/vpnmodo-private/work.json" \
  --credentials-stdin < "$HOME/vpnmodo-private/credentials.json"
```

This file is an additional credential copy that you must manage under your
team's policy. Prefer hidden prompts for manual use. Never put secrets in shell
arguments, environment variables, profile JSON, issues or logs. The VM vault is
plaintext protected by filesystem permissions and is readable by VM/Docker
administrators; SSH encrypts transport. The Mac registry does not retain a VPN
credential copy.

`--json` operation watching does not prompt for MFA. Deliver the current code
through protected stdin for the matching operation/challenge using
`operation submit-mfa`, as described in the [XAuth MFA guide](design/006-managed-xauth.md).
A disconnected watcher does not cancel the VM-owned operation. Inspect/watch it
before starting a new login.

## Update a profile or rotate credentials

First inspect the current revision:

```sh
vpnmodo profile inspect work
```

Edit your private profile JSON while preserving its `name`, then use the returned
revision number in place of `N`:

```sh
vpnmodo down work
vpnmodo profile update work --file "$HOME/vpnmodo-private/work.json" --revision N
vpnmodo profile inspect work
```

To rotate credentials, inspect again and pass the **current** revision. The
command prompts for the new values; connect explicitly afterwards:

```sh
vpnmodo profile rotate-credentials work --revision N
vpnmodo up work
```

Mutations require the managed profile to be inactive. Revisions prevent stale
updates; do not reuse an old revision after a successful mutation. See
[profile storage](design/002-profile-storage.md) for lifecycle and disposal rules.

## Adopting an existing deployment

Create the inactive new profile with approved settings and credentials first.
There is no automatic import of standalone launcher directories. Schedule an
explicit cutover: stop the selected legacy recovery owner and session before
starting the managed replacement if parallel login is unsafe. Never let two
recovery owners manage one session, and do not stop unrelated deployments.
Verify authenticated private SSH/application access after `vpnmodo up`.

Retaining the previous configuration as an optional fallback is an operator
choice. Rehearsing legacy rollback is outside the agreed release scope. Loading
an image bundle does not perform adoption or stop existing VPNs. Earlier real
ARM64 adoption/cutover results are in the [validation record](release-validation.md).

## Removal

Run `vpnmodo down work` while the CLI is still installed. This removes owned
workstation forwards and disconnects the profile; credentials remain on the VM.
To dispose of the inactive VM profile, inspect its current revision and run:

```sh
vpnmodo profile delete work --revision N
```

`profile unregister` only removes a workstation reference; it does not delete a
VM profile. To remove the workstation executable, remove `<prefix>/bin/vpnmodo`
after stopping owned forwards. Removing the binary does not disconnect a VM
session or dispose of VM secrets. Preserve local registry/service files if you
intend to reinstall. Do not delete the controller's storage while other profiles
still need it.
