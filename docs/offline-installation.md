# Existing VM and offline installation

Use this path for an existing local or remote Ubuntu VM, or to transfer release
images without pulling them from GHCR inside the VM. Install the Mac CLI first
using the download and binary installation commands in
[README installation](../README.md#installation). Skip its `vpnmodo setup` command
and follow this guide instead. No registry login is needed for the bundle.

Obtain the matching `vpnmodo-v0.1.0-rc.7-linux-arm64-images.tar.gz` and trusted
`SHA256SUMS-linux-arm64` from the maintainer. Put them in the same private release
directory as the CLI archive. On the **Mac**, set:

```sh
artifact_dir="$HOME/Downloads/vpnmodo-v0.1.0-rc.7"
setup_dir="$HOME/vpnmodo-private/setup"
umask 077
mkdir -p "$setup_dir"
```

## 1. Prepare the Ubuntu VM and SSH access

Follow [prepare an Ubuntu VM](vm-setup.md) to establish verified SSH and run
the dedicated `vpn_sidecar.client.vm_prepare` Ansible role from your Mac. You
supply an existing Ubuntu 24.04 ARM64 VM with Python 3 and administrative access.
The role prepares rootful Docker Engine, Compose, utilities, management-account
access and kernel support without VPN settings or credentials. Ansible host
preparation does not need Docker Desktop on the Mac.

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
noninteractive sudo instead, see [sudo mode](onboarding.md#sudo-mode) before
loading images or registering the controller. Substitute that guide's loader
and `host add` commands for the ordinary versions below; do not run both modes.

## 2. Load the container images on the VM

On the **Mac**, transfer the bundle and checksum file:

```sh
ssh vpn-vm 'umask 077; mkdir -p "$HOME/vpnmodo-install/v0.1.0-rc.7"'
scp "$artifact_dir/vpnmodo-v0.1.0-rc.7-linux-arm64-images.tar.gz" \
  "$artifact_dir/SHA256SUMS-linux-arm64" \
  vpn-vm:vpnmodo-install/v0.1.0-rc.7/
ssh vpn-vm
```

In that **VM shell**, verify the checksum before extracting. Continue only if
`sha256sum` reports `OK`:

```sh
umask 077
cd "$HOME/vpnmodo-install/v0.1.0-rc.7"
sha256sum -c SHA256SUMS-linux-arm64
```

After the checksum passes, load the images:

```sh
mkdir -m 700 release
tar -xzf vpnmodo-v0.1.0-rc.7-linux-arm64-images.tar.gz -C release
cd release
python3 install-vm.py > installed-release.json
```

The loader verifies and imports all four images. Continue only if it exits
successfully. Its receipt records the VM's immutable image IDs and
`"activated": false`: loading images has not started a controller or VPN.
Return to the **Mac** with `exit`, then copy the receipt back:

```sh
scp vpn-vm:vpnmodo-install/v0.1.0-rc.7/release/installed-release.json \
  "$setup_dir/installed-release.json"
vpnmodo host add vpn-vm --ssh vpn-vm
vpnmodo host setup vpn-vm --release "$setup_dir/installed-release.json"
vpnmodo host check vpn-vm
```

Expect `storage ready: true` and `Docker ready: true`. If the controller is still
starting, repeat `host check`. Setup uses the receipt to select all four verified
images; it does not import existing profiles or change legacy VPN deployments.
No controller firewall port or registry credentials are needed.

Continue with [creating a VPN profile](../README.md#create-a-vpn-profile),
then follow [everyday usage](../README.md#usage).
