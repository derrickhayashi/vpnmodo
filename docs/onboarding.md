# Install vpnmodo on a Mac and personal VM

Use this path with a verified private release candidate. Each teammate supplies
their own local or remote Ubuntu 24.04 VM. VPN profile creation has explicit JSON
configuration and hidden credential/MFA prompts; there is no creation wizard.
Existing deployments keep their current launcher until an explicit cutover.
The qualified first-release path uses macOS arm64 and Ubuntu 24.04 arm64.
Intel/amd64 artifacts may be cross-built but have no native runtime validation
requirement for this release.

## 1. Install the workstation command

Ask the maintainer for the macOS archive matching the Mac (`uname -m`: arm64 or
x86_64/amd64), its trusted checksum, and the matching source/docs and VM image
bundle. Check the checksum before extracting. For example:

```sh
shasum -a 256 vpnmodo-v0.1.0-rc.1-darwin-arm64.tar.gz
```

Compare the printed hash with the matching entry in the trusted
`SHA256SUMS-cli` file before extracting. You need only your Mac architecture
archive; the complete checksum file also lists the other architecture and source.
If it matches, create a private extraction directory and install:

```sh
mkdir -m 700 /absolute/private/cli
tar -xzf vpnmodo-v0.1.0-rc.1-darwin-arm64.tar.gz -C /absolute/private/cli
cd /absolute/private/cli
sh install.sh --prefix "$HOME/.local"
"$HOME/.local/bin/vpnmodo" version
```

The installer needs macOS command-line
utilities (`shasum`, `install`, `mktemp`) and no Go or Homebrew runtime. It checks
the binary checksum and architecture again and refuses unexpected symlinks.
Ensure `$HOME/.local/bin` is on `PATH`, or use the absolute executable path.
Use `--replace` only for an intended upgrade; no existing executable is silently
overwritten. The CLI is not currently Developer ID signed/notarized. Follow the
team's approved macOS software policy; the installer does not remove quarantine
attributes or change Gatekeeper policy. Do not run a mismatched architecture.

## 2. Prepare the VM and verify SSH

The VM needs rootful Docker Engine, Compose v2, Python 3, and an SSH account with
Docker access. XAuth needs Linux XFRM/IPsec/netfilter support; L2TP also needs PPP
and `/dev/ppp`. See [VM prerequisites](deployment.md). VM creation and Docker/SSH
installation are outside `vpnmodo host setup`. Members of the Docker group have
administrative authority over the VM.

Create a normal SSH alias, `personal-vm`, with your own key and trusted host-key
verification. Verify its host key through a trusted VM console or administrator.
Keep your private key on the Mac. `vpnmodo` requires strict host-key checking;
agent forwarding and a controller HTTP listener are unnecessary.

Transfer the verified image bundle matching the **VM** architecture. The Mac and
VM architectures can differ. On the VM, verify its matching checksum before
extracting, then run the loader from the extracted directory:

```sh
sha256sum -c SHA256SUMS-linux-arm64
mkdir -m 700 /absolute/private/vpnmodo-release
tar -xzf vpnmodo-v0.1.0-rc.1-linux-arm64-images.tar.gz \
  -C /absolute/private/vpnmodo-release
cd /absolute/private/vpnmodo-release
python3 install-vm.py > installed-release.json
```

Copy the resulting JSON receipt to a private path on the Mac. The loader reports
`activated: false`: it only loads verified images. Keep all release files outside
source and customer credential directories. You do not need registry credentials
or a Go toolchain to install the bundle.

## 3. Register and explicitly activate the controller

On the Mac:

```sh
vpnmodo host add personal-vm --ssh personal-vm
vpnmodo host setup personal-vm --release /absolute/private/installed-release.json --json
vpnmodo host check personal-vm --json
```

If the account has passwordless sudo instead of direct Docker access, register
with `host add personal-vm --ssh personal-vm --sudo`. Fixed VM management commands
then use noninteractive sudo, including controller bootstrap, the bridge and
forwarding. This does not change group membership or sudo policy. Bootstrap in
this mode owns state under root's home; use that same mode for later access and
upgrades. Load the verified image bundle with
`sudo -n python3 install-vm.py > installed-release.json`.
Do not enable sudo mode for a pre-existing user-owned controller without an
explicit state migration; bootstrap refuses its mismatched storage mount.

For a separate SSH configuration, add `--ssh-config /absolute/path/to/config` to
`host add`. Registration records a workstation reference; setup explicitly starts
the controller and provisions its private state directory. It does not touch
legacy VPNs. The controller uses a private Unix socket and host Docker access;
there is no API port to expose. Use [release upgrade instructions](releases.md)
for a changed image, which requires explicit `--replace`.

## 4. Create a backend-specific profile

Copy an [L2TP](../examples/profiles/v1/example-l2tp.json) or
[XAuth](../examples/profiles/v1/example-xauth.json) JSON example to a private
location **outside Git**. Replace the documentation addresses and connection
settings with the approved values for your VPN. Set the JSON profile `name` to
`example`, and configure the private TCP health endpoint. The selected backend's
`config` section has its own attributes. Credentials are supplied separately:

```sh
vpnmodo profile create example --host personal-vm \
  --file /absolute/private/profile.json
vpnmodo profile inspect example
```

Enter username, password and PSK at the hidden prompts. They are encrypted over
SSH and stored in the protected VM vault (0700 directories/0600 files); the Mac
registry retains references, not a credential copy. The VM administrator/Docker
administrator can read VM secrets. There is no claim of encryption at rest.
For automation, use `--credentials-stdin` or `--credentials-fd`; do not put secrets
in shell arguments, environment variables, Git, issues or ordinary logs.

If replacing an existing deployment, create the inactive new profile first and
plan the explicit cutover with its owner. Preserve the old configuration and
credentials for rollback. Stop the selected legacy recovery owner and session
before starting the managed replacement if parallel login is unsafe. Never let
two recovery owners manage the same session. Do not stop unrelated deployments.
Real ARM64 adoption/cutover is recorded in #9. Legacy rollback testing is not a
release gate; the retained fallback instructions are optional. Loading a release
is not automatic adoption.

## 5. Connect, verify, and optionally expose a service

```sh
vpnmodo up example
vpnmodo doctor example
```

XAuth asks for the newest code for this login, through a hidden prompt. SSH loss
does not cancel a VM operation. Do not start another login just because the
workstation disconnected; inspect the existing operation. JSON mode never
prompts: submit a fresh code with `operation submit-mfa --code-stdin` for the
matching operation and challenge, as described in the [MFA guide](design/006-managed-xauth.md).
L2TP uses the provisioned credentials without an emailed code.

For optional service forwarding, place a private service JSON file outside Git
using [the example](../examples/services/v1/example.json). Configure the private
SSH target's trusted key and workstation authentication in your SSH config.
Configure named services and start them:

```sh
vpnmodo service configure example --file /absolute/private/services.json
vpnmodo up example
vpnmodo status example
vpnmodo doctor example --json
```

For an existing workstation SSH alias, its `ProxyCommand` may use
`/absolute/path/to/vpnmodo proxy example %h %p`. Each invocation resolves the
current healthy application container and rechecks its identity before opening
the stream. It never starts login or requests MFA. Keep destination authentication
and trusted host keys in the usual workstation SSH configuration.

The [service guide](design/007-service-access.md) explains the two verified SSH
hops, loopback listeners and destination checks. A healthy VPN alone is not proof
that an HTTP application or destination SSH login works. Verify the intended
private application. `doctor` separates VM, tunnel, service and local-port errors.

```sh
vpnmodo down example
```

Down removes owned workstation forwards and disconnects this managed profile.
Credentials remain for reuse. Explicit profile deletion is a separate action.
To remove only a workstation installation, stop its owned forwards first, then
remove `<prefix>/bin/vpnmodo`; removing the binary does not stop a VM session or
delete the VM vault. Preserve local registry/service files if you plan to reinstall.
