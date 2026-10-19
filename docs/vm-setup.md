# Prepare an Ubuntu VM for vpnmodo

Each user supplies an existing local or remote Ubuntu 24.04 ARM64 VM. UTM is one
option on an Apple Silicon Mac; a remotely hosted Ubuntu VM is also suitable.
Install Ubuntu and create your own administrator/SSH account through your chosen
VM provider first. These steps prepare that guest OS; vpnmodo does not create it.
The tested platform is ARM64. No Intel VM is needed for first-release validation.

The VM needs an address reachable from the Mac over SSH. The VM also needs
outbound access to the VPN gateway on UDP 500/4500 and the VPN's approved private
TCP health endpoint through the tunnel. Gateways without NAT traversal can also
require ESP (IP protocol 50). No controller HTTP port needs to be opened.

## 1. Install prerequisites on a fresh VM

Run these commands in the **Ubuntu VM console** as the account that will own the
controller. The Docker installation below is for a fresh VM. If Docker already
works, skip its installation and use the checks in the next section; do not
replace or restart an existing shared daemon just to follow this guide.

```sh
sudo apt-get update
sudo apt-get install -y openssh-server python3 ca-certificates curl
sudo systemctl enable --now ssh
```

For rootful Docker Engine and the Compose v2 plugin, use the
[official Docker Ubuntu installation instructions](https://docs.docker.com/engine/install/ubuntu/).
The repository setup and package install for a fresh Ubuntu VM are:

```sh
sudo install -m 0755 -d /etc/apt/keyrings
sudo curl -fsSL https://download.docker.com/linux/ubuntu/gpg \
  -o /etc/apt/keyrings/docker.asc
sudo chmod a+r /etc/apt/keyrings/docker.asc
sudo tee /etc/apt/sources.list.d/docker.sources > /dev/null <<EOF
Types: deb
URIs: https://download.docker.com/linux/ubuntu
Suites: $(. /etc/os-release && echo "${UBUNTU_CODENAME:-$VERSION_CODENAME}")
Components: stable
Architectures: $(dpkg --print-architecture)
Signed-By: /etc/apt/keyrings/docker.asc
EOF
sudo apt-get update
sudo apt-get install -y docker-ce docker-ce-cli containerd.io docker-buildx-plugin docker-compose-plugin
sudo systemctl enable --now docker
```

To use the walkthrough's direct-Docker mode on your personal VM:

```sh
sudo usermod -aG docker "$(id -un)"
```

Docker group membership gives administrative authority over the VM. Log out and
log back in so the new group applies. If the VM instead already authorizes
noninteractive sudo for these management commands, use
[sudo mode](onboarding.md#sudo-mode) consistently; vpnmodo does not grant it.

## 2. Check Docker and kernel support

In a new **VM login session**, run:

```sh
python3 --version
docker --host unix:///var/run/docker.sock info --format '{{.OSType}} {{.Architecture}}'
docker compose version
sudo modprobe xfrm_user
```

Expect Python 3.9 or later (Ubuntu 24.04 supplies Python 3.12), Docker `linux`
on ARM64/aarch64, and Docker Compose v2. Docker Desktop, rootless Docker and a
remote Docker context are outside this setup path. The controller uses the
VM's rootful `/var/run/docker.sock`.

Both managed backends require Linux IPsec/XFRM and netfilter support. XAuth
additionally requires XFRM output marks and IPv6-disable sysctls. These are
checked by the backend when it starts; loading `xfrm_user` alone is not a full
packet-enforcement test.

For **L2TP/IPsec**, also prepare PPP:

```sh
sudo modprobe ppp_generic
sudo modprobe ppp_async
test -c /dev/ppp
printf '%s\n' ppp_generic ppp_async | sudo tee /etc/modules-load.d/vpnmodo-ppp.conf
```

`test` should exit successfully. Optional `l2tp_ppp` kernel acceleration may be
loaded if your kernel provides it; the backend supports userspace L2TP without
that optional module. Containers share the VM's kernel, so fix missing module
packages/device support in the VM before connecting. See the
[Ubuntu L2TP kernel regression note](kernel-regression.md) if negotiation succeeds
but PPP does not come up; the documented `6.8.0-146-generic` regression is not
fixed by rebuilding a container.

## 3. Set up verified SSH from the Mac

Keep your private SSH key on the **Mac**. If you do not already have a suitable
key, create a dedicated one on the Mac and protect it with a passphrase:

```sh
install -d -m 700 "$HOME/.ssh"
install -d -m 700 "$HOME/.ssh"
ssh-keygen -t ed25519 -f "$HOME/.ssh/vpnmodo_ed25519"
ssh-add "$HOME/.ssh/vpnmodo_ed25519"
cat "$HOME/.ssh/vpnmodo_ed25519.pub"
```

Through the trusted **VM console**, append that public key as one line to the VM
account's `~/.ssh/authorized_keys`. Create the directory/file with these modes:

```sh
install -d -m 700 "$HOME/.ssh"
touch "$HOME/.ssh/authorized_keys"
chmod 600 "$HOME/.ssh/authorized_keys"
nano "$HOME/.ssh/authorized_keys"
ssh-keygen -lf /etc/ssh/ssh_host_ed25519_key.pub
```

Record the displayed **host-key fingerprint** through this trusted console.
Create the `vpn-vm` SSH alias on the Mac using the
[README example](../README.md#2-prepare-the-ubuntu-vm-and-ssh-access). If you created
the dedicated key above, set `IdentityFile ~/.ssh/vpnmodo_ed25519` in that alias.
Then, on the **Mac**:

```sh
ssh -o StrictHostKeyChecking=ask vpn-vm
```

Compare the offered fingerprint with the VM console before accepting. If they
differ, stop and resolve the identity mismatch. Return to the Mac with `exit`,
then verify access without prompts:

```sh
ssh -o BatchMode=yes vpn-vm 'python3 --version && docker --host unix:///var/run/docker.sock info --format "{{.OSType}}" && docker compose version'
```

All three commands must succeed. Load your key into your Mac SSH agent if
necessary; agent forwarding to the VM is not required. Unknown or changed host
keys fail in vpnmodo. Continue with
[loading the container images](../README.md#3-load-the-container-images-on-the-vm).
