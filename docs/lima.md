# Create a ready-to-use local VM with Lima

Lima creates Ubuntu 24.04 ARM64 on Apple Silicon, prepares Docker and IPsec/PPP,
and pulls the published vpnmodo release images. Use Lima's generated SSH config
directly. The Ansible role remains the preparation option for existing VMs.

## Single-file setup

Install the standalone CLI using [README installation](../README.md#installation),
then run:

```sh
vpnmodo setup
```

The CLI embeds the Lima YAML, VM image installer and pinned registry manifest.
It writes them under its private configuration directory, creates the VM using
native `limactl`, registers Lima's generated SSH configuration, starts the
controller and waits for readiness. It installs Lima with Homebrew if missing;
Homebrew itself must already be installed. The Mac does not need Python or Docker.

The GHCR packages are private, so setup prompts for a GitHub username and a token
with `read:packages`. The account must have package read access. Both prompts
hide input; credentials are saved with mode 0600 and copied into root's Docker
config in the VM. No token is passed in command arguments or environment variables.

The default instance and host name is `vpn-vm`. Use `vpnmodo setup --name NAME`
for another name. An existing instance created by setup is reused; an unrelated
instance or conflicting registered host is refused. Rerunning setup starts the
VM and reuses the controller without replacing a different controller release.
Explicit upgrades remain in the [release guide](releases.md#installation-and-upgrades).

Private setup files live beside the host registry under `setup/NAME/`, normally
`~/Library/Application Support/vpnmodo/setup/vpn-vm/` on macOS. If a token was
entered incorrectly, remove that directory's `ghcr-config.json` and run setup
again. Setup refreshes the VM credentials and retries image installation.

When setup reports ready, continue with
[creating a VPN profile](../README.md#create-a-vpn-profile) and
[everyday usage](../README.md#usage).

## Manual provisioning reference

The steps below expose the native Lima flow for maintainers. They are not
needed by users of `vpnmodo setup`.

## Prepare the release inputs

Install Lima 2.2.1 or later with `brew install lima`. Obtain `lima.yaml`,
`install-vm.py` and `registry-release.json` from the same published release and
place them in a private directory outside Git:

```sh
setup_dir="$HOME/vpnmodo-private/setup"
mkdir -p "$setup_dir"
chmod 700 "$setup_dir"
```

From the current source checkout, the pinned release inputs are also available
without building anything:

```sh
cp examples/lima/vpnmodo.yaml "$setup_dir/lima.yaml"
cp examples/lima/registry-release.json scripts/release/install-vm.py "$setup_dir/"
```

The images are private GHCR packages under `derrickhayashi/vpn-sidecars-next`.
Your GitHub account needs package read access. Create the adjacent
`ghcr-config.json`, replacing `GITHUB_USERNAME` with your GitHub username. Enter
a token with `read:packages` at the hidden prompt:

```sh
python3 - "$setup_dir/ghcr-config.json" GITHUB_USERNAME <<'PY'
import base64, getpass, json, os, sys
os.umask(0o077)
credential = sys.argv[2] + ':' + getpass.getpass('GHCR read token: ')
auth = base64.b64encode(credential.encode()).decode()
with open(sys.argv[1], 'w') as output:
    json.dump({'auths': {'ghcr.io': {'auth': auth}}}, output)
os.chmod(sys.argv[1], 0o600)
PY
```

Keep this credential file private. Lima copies it into root's Docker config
with mode 0600; the token does not belong in the YAML. No Mac Docker daemon or
Docker Desktop is required.

Edit CPU, memory or disk in `lima.yaml` before creating the VM. Its default
account is `vpnadmin` with passwordless sudo, so use vpnmodo's sudo mode.
Host folders and automatic service forwarding are disabled.

## Create and activate

```sh
limactl start --tty=false --name vpn-vm "$setup_dir/lima.yaml"
scp -F "$HOME/.lima/vpn-vm/ssh.config" \
  lima-vpn-vm:/var/lib/vpnmodo/installed-release.json "$setup_dir/installed-release.json"
vpnmodo host add vpn-vm --ssh lima-vpn-vm \
  --ssh-config "$HOME/.lima/vpn-vm/ssh.config" --sudo
vpnmodo host setup vpn-vm --release "$setup_dir/installed-release.json"
vpnmodo host check vpn-vm
```

Provisioning pulls the controller, L2TP, XAuth and application images using
their published digests. It verifies their architecture and controller version
and writes the existing installation receipt. `host setup` uses that receipt to
start the controller with the correct destination-local image IDs. No image
build, bundle transfer or manual image loading is needed on the user's VM.

The environment is ready for profile creation, credential entry and connection.
Existing vpnmodo proxy and service commands use the registered SSH settings.
No fixed VM IP or additional Lima forwarding rule is needed.

## SSH and restart

```sh
ssh -F "$HOME/.lima/vpn-vm/ssh.config" lima-vpn-vm
limactl stop vpn-vm
limactl start vpn-vm
```

For a custom `LIMA_HOME`, use the absolute config path printed by
`limactl list --format '{{.SSHConfigFile}}' vpn-vm`. To use `ssh lima-vpn-vm`
directly, include `~/.lima/*/ssh.config` in your Mac's SSH config.

Cached digest-pinned images are reused on subsequent boots. The controller's
Docker restart policy brings it back after a VM restart; reconnect workstation
service forwards afterward. Editing the original YAML does not modify an
existing instance. Release upgrades remain explicit through
[the upgrade workflow](releases.md#installation-and-upgrades).

See [Lima SSH](https://lima-vm.io/docs/usage/ssh/),
[VZ support](https://lima-vm.io/docs/config/vmtype/vz/), and
[private GHCR authentication](https://docs.github.com/en/packages/working-with-a-github-packages-registry/working-with-the-container-registry).
