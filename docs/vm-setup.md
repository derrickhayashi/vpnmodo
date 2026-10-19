# Prepare an Ubuntu VM for vpnmodo

Each teammate supplies an existing **Ubuntu 24.04 ARM64 (aarch64)** VM and owns
its management account. UTM on Apple Silicon or a remotely hosted VM are suitable
choices. Install Ubuntu, create an administrator account, and provide SSH and
Python 3 first. VM provisioning is outside this workflow.

The dedicated `vpn_sidecar.client.vm_prepare` Ansible role prepares that guest
OS for vpnmodo. It installs VM utilities, rootful Docker Engine, Compose v2 and
Buildx when needed, enables Docker at boot, prepares explicit account access,
and checks IPsec/XFRM and selectable L2TP/PPP support. It needs no customer
settings, credentials, MFA, profile or runtime source pin. It does not load
container images, activate a controller, or deploy VPN/application/watchdog
containers. Those steps follow through vpnmodo.

Use a fresh or dedicated VM. Preparation can install packages, start an inactive
Docker service, change Docker group membership and load/persist kernel modules.
It does not restart a running Docker daemon, change the boot kernel, or reboot.
Do not apply it to a shared live deployment without reviewing planned changes
with that host's owner.

## 1. Establish verified SSH and administration

In the **Ubuntu VM console**, use your existing administrator account to install
the connection prerequisites if needed:

```sh
sudo apt-get update
sudo apt-get install -y openssh-server python3 python3-apt
sudo systemctl enable --now ssh
```

Keep your private SSH key on the **Mac**. If you need a dedicated key, create
one with a passphrase on the Mac:

```sh
install -d -m 700 "$HOME/.ssh"
ssh-keygen -t ed25519 -f "$HOME/.ssh/vpnmodo_ed25519"
ssh-add "$HOME/.ssh/vpnmodo_ed25519"
cat "$HOME/.ssh/vpnmodo_ed25519.pub"
```

Through the trusted **VM console**, append that public key as one line to the
management account's `~/.ssh/authorized_keys`:

```sh
install -d -m 700 "$HOME/.ssh"
touch "$HOME/.ssh/authorized_keys"
chmod 600 "$HOME/.ssh/authorized_keys"
nano "$HOME/.ssh/authorized_keys"
ssh-keygen -lf /etc/ssh/ssh_host_ed25519_key.pub
```

Record the displayed host-key fingerprint. On the **Mac**, put an alias in
`~/.ssh/config`, replacing the uppercase values with the existing VM address and
account and selecting your actual key:

```sshconfig
Host vpn-vm
    HostName VM_ADDRESS
    User VM_USER
    IdentityFile ~/.ssh/vpnmodo_ed25519
    IdentitiesOnly yes
    StrictHostKeyChecking yes
    ForwardAgent no
```

Enroll the host key from the **Mac**:

```sh
ssh -o StrictHostKeyChecking=ask vpn-vm
```

Compare the offered fingerprint with the trusted VM console before accepting.
Resolve any mismatch before proceeding. Return to the Mac with `exit`, then:

```sh
ssh -o BatchMode=yes vpn-vm 'python3 --version'
ssh -t vpn-vm 'sudo -v'
```

Your account must already have administrative authorization; the role does not
grant sudo policy. Ansible can prompt for that account's sudo password with
`-K` below.

The VM needs outbound package-repository access during preparation. Later VPN
connections need UDP 500/4500 to the approved gateway (and possibly ESP/IP
protocol 50 without NAT traversal), plus the private TCP health endpoint through
the tunnel. No controller HTTP port is required.

## 2. Install pinned Ansible and the collection on the Mac

Install `uv` through your approved Mac software process if it is unavailable.
Run these commands on the **Mac**; Docker Desktop and Go are not required:

```sh
uv tool install --python 3.12 ansible-core==2.21.5
export PATH="$HOME/.local/bin:$PATH"
ansible --version
umask 077
prepare_dir="$HOME/vpnmodo-private/vm-prepare"
install -d -m 700 "$prepare_dir"
cd "$prepare_dir"
```

Keep inventory, playbooks and installation files in this private directory
outside every Git checkout. Your Mac's Git/SSH must already authenticate to the
private repository; do not place access tokens in requirements or commands.

Save `requirements.yaml` here:

```yaml
collections:
  - name: git+ssh://git@github.com/derrickhayashi/vpn-sidecars-next.git
    type: git
    version: 78bfd940d1b06403bce1ac5bcf86f85babd1e004
```

This pins the reviewed preparation implementation in collection `0.4.0`. The runtime
candidate `990dc2fe7caec4e1b68cf2b54788b996937e66c5` **does not contain this role**;
use that candidate's matching artifacts for later image/CLI installation only.

```sh
ansible-galaxy collection install --force -r requirements.yaml -p ./collections
export ANSIBLE_COLLECTIONS_PATH="$prepare_dir/collections"
```

## 3. Define the private inventory and preparation playbook

Save `inventory.yaml` in the same directory. Set `ansible_user` and
`vpnmodo_vm_management_user` explicitly to the existing account from the SSH
alias; the role does not infer the management account from root/become facts or
create it. This walkthrough uses one account for SSH and vpnmodo management.

```yaml
all:
  hosts:
    vpn-vm:
      ansible_host: vpn-vm
      ansible_user: VM_USER
      ansible_python_interpreter: /usr/bin/python3
      vpnmodo_vm_management_user: VM_USER
```

Save `prepare.yaml`:

```yaml
---
- name: Prepare the existing vpnmodo VM
  hosts: all
  gather_facts: true
  become: true
  vars:
    vpnmodo_vm_docker_access: direct
    vpnmodo_vm_l2tp: true
    vpn_sidecar_l2tp_acceleration: false
  roles:
    - vpn_sidecar.client.vm_prepare
```

`direct` adds the selected account to the Docker group, which gives
administrative authority over the VM. For an account already authorized for
noninteractive sudo, choose `vpnmodo_vm_docker_access: sudo` instead. That mode
validates `sudo -n` management access as the selected account without installing
sudoers policy or adding Docker group membership. An Ansible `-K` password is
only for preparation; it does not satisfy vpnmodo's later noninteractive sudo
requirement. Follow [sudo mode](onboarding.md#sudo-mode) consistently for image
loading and `vpnmodo host add --sudo` afterwards.

L2TP is enabled by default and requires `ppp_generic`, `ppp_async` and a working
`/dev/ppp`. Set `vpnmodo_vm_l2tp: false` only for an IPsec/XAuth-only VM; both
backends still need IPsec/XFRM support. The managed L2TP runtime detects available
kernel acceleration and can fall back to userspace L2TP when it is unavailable.
Leave
`vpn_sidecar_l2tp_acceleration: false` unless you specifically require kernel
acceleration. This switch controls preparation requirements; `false` does not
force the runtime to avoid an already available kernel module. Setting it to
`true` makes `l2tp_ppp` a strict host prerequisite;
missing support fails preparation rather than silently falling back.

The [role input reference](../ansible/README.md#preparation-inputs) describes
package installation switches and exact kernel allow/block/warning lists.
Repository availability and support for the running kernel remain required.
See the [kernel regression note](kernel-regression.md) before selecting a kernel
for production L2TP; available modules alone do not prove kernel stability.

## 4. Preview, apply and verify an unchanged rerun

On the **Mac**, from the private directory:

```sh
chmod 600 requirements.yaml inventory.yaml prepare.yaml
ansible-playbook -i inventory.yaml prepare.yaml --syntax-check
ansible-playbook -i inventory.yaml prepare.yaml --check --diff -K
ansible-playbook -i inventory.yaml prepare.yaml --diff -K
ansible-playbook -i inventory.yaml prepare.yaml --diff -K
```

Omit `-K` if the existing administrator account already has passwordless sudo.
Review the preview before applying. Check mode runs read-only package, account,
Docker and module discovery, reports planned changes on an incomplete host, and
does not install packages, start services, load modules or change accounts. It
cannot prove the result of those proposed changes. Final daemon, account-access,
XFRM and PPP checks run during apply. A successful unchanged rerun should report
`changed=0`; investigate any continuing changes before moving on.

For direct access, close any existing SSH control connection and start a fresh
login after group membership changes:

```sh
ssh -O exit vpn-vm 2>/dev/null || true
ssh -o BatchMode=yes vpn-vm 'python3 --version && docker --host unix:///var/run/docker.sock info --format "{{.OSType}} {{.Architecture}}" && docker compose version'
```

Expect Python 3, Docker `linux` on ARM64/aarch64, and Compose v2. In sudo mode,
use the [sudo checks](onboarding.md#sudo-mode) instead. Rootless Docker and a
remote Docker context are outside this preparation path; vpnmodo uses the VM's
rootful `/var/run/docker.sock`.

Preparation ends here. Continue with [loading the container images and running
vpnmodo host setup](../README.md#3-load-the-container-images-on-the-vm), then
create your profile and connect with vpnmodo. Host preparation does not validate
VPN credentials, gateway compatibility, MFA or actual tunnel traffic.
