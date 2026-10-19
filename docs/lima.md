# Create a local VM with Lima

On an Apple Silicon Mac, Lima can create the Ubuntu 24.04 ARM64 VM used by
vpnmodo. It handles the account, SSH key and connection details. The template
installs Python, rootful Docker, Compose and the IPsec/PPP prerequisites.
Existing UTM and remote Ubuntu VMs can use [Ansible preparation](vm-setup.md).

## Create the VM

Install Lima 2.2.1 or later, then run these commands from this source checkout:

```sh
brew install lima
mkdir -p "$HOME/vpnmodo-private"
cp examples/lima/vpnmodo.yaml "$HOME/vpnmodo-private/lima.yaml"
limactl start --tty=false --name vpn-vm "$HOME/vpnmodo-private/lima.yaml"
```

Edit CPU, memory or disk in your copy before creating the VM. The default user
is `vpnadmin` with passwordless sudo; use vpnmodo's sudo mode. Docker group
membership is unnecessary. Host folders and automatic service forwarding are
disabled. No controller or VPN is started during this step.

For an existing VM, use `limactl start vpn-vm`. Its saved configuration stays in
Lima; editing the original YAML does not change an existing instance.

## Connect and register

Use Lima's generated SSH configuration directly. Lima manages the local VM's
SSH identity, user, key and current endpoint, so no fixed VM IP is required:

```sh
ssh -F "$HOME/.lima/vpn-vm/ssh.config" lima-vpn-vm
vpnmodo host add vpn-vm --ssh lima-vpn-vm \
  --ssh-config "$HOME/.lima/vpn-vm/ssh.config" --sudo
```

If `LIMA_HOME` is customized, find the configuration path with
`limactl list --format '{{.SSHConfigFile}}' vpn-vm` and use that absolute path.
To use `ssh lima-vpn-vm` directly, add `Include ~/.lima/*/ssh.config` near the
top of your Mac's `~/.ssh/config`.

Continue with [loading the release bundle](../README.md#3-load-the-container-images-on-the-vm).
Use `lima-vpn-vm` as the SSH/SCP target and pass `-F` with the generated config,
or include it as above. Load the images with `sudo -n python3 install-vm.py`, as
described in [sudo mode](onboarding.md#sudo-mode), then copy the receipt to the Mac.
Skip the walkthrough's `host add` command: the Lima host is already registered.
Continue with:

```sh
vpnmodo host setup vpn-vm --release /absolute/path/to/installed-release.json
vpnmodo host check vpn-vm
```

Create profiles and access services through the existing vpnmodo commands.
The registered SSH settings also carry proxy and service traffic; no extra
Lima forwarding rules are needed. A VM restart interrupts active connections;
restart the service forwards afterward.

## Stop the VM

```sh
limactl stop vpn-vm
```

The template pins an Ubuntu image URL and SHA256 checksum. Update both together
when refreshing the image. Bootstrap installs missing packages without changing
the running kernel; optional `l2tp_ppp` acceleration is not required.

See [Lima SSH](https://lima-vm.io/docs/usage/ssh/) and
[VZ support](https://lima-vm.io/docs/config/vmtype/vz/).
