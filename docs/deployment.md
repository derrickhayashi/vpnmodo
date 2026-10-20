# Deploy to a Linux VM

Use the Ansible role below to provision either backend. L2TP/IPsec starts during
deployment; IPsec/XAuth is prepared for a separate interactive login. The manual
launcher flow also supports both backends with consumer-owned profiles. Run workstation
commands on your laptop and VM commands over SSH.
Replace the example gateway, private health endpoint and SSH alias before
connecting. Documentation addresses below are placeholders.

Each profile starts three containers: its selected VPN, an application sharing
that VPN's network namespace, and the shared watchdog. Select either L2TP/IPsec
or IPsec/XAuth. The two VPN images run independently.

## Recommended: provision with Ansible

Use `vpn_sidecar.client.vpn_sidecar` on Ubuntu. It installs host utilities, Docker,
Compose and Buildx as needed; checks the running kernel and required modules;
transfers pinned source; deploys protected credentials; and builds or pulls the
VPN and watchdog images. L2TP/IPsec starts all three services during deployment.
XAuth provisioning stops before login and supplies a profile for the interactive
launcher. There is no separate profile upload or manual credential-copy step.

The consumer project owns the inventory, settings, encrypted vault and playbook.
The role consumes Ansible variables rather than launcher `profile.env` files.
For L2TP, use the role to manage deployment and updates. For XAuth, use the role
for provisioning and its generated profile with the launcher for login/logout.

### Prepare the controller and inventory

The controller needs Git, Docker Compose v2 for configuration rendering, and
Ansible in its project environment. Docker does not need to be running on the
controller. Commands below run from a consumer project managed with `uv`, with
Ansible installed in that environment. The Ubuntu VM must already have SSH,
Python 3 and sudo access; VM creation remains outside this role.

Create `inventory/vpn/hosts.yaml` in the consumer project:

```yaml
all:
  children:
    vpn_sidecars:
      hosts:
        vpn-host:
          ansible_host: REPLACE_WITH_VM_ADDRESS
          ansible_user: REPLACE_WITH_VM_SSH_USER
```

Use a direct management connection to the VM, independent of the VPN being
deployed. If your inventory uses an SSH alias, ensure the controller can resolve
it through its SSH configuration.

### Pin the collection and runtime source

Create `meta/vpn-sidecar-requirements.yml` in the consumer project:

```yaml
collections:
  - name: git+file:///absolute/path/to/l2tp-ipsec-vpn-sidecar
    type: git
    version: REPLACE_WITH_FULL_COMMIT
```

Use a reviewed commit containing the selected `containers/<backend>/`, shared
`scripts/vpn` launcher and `watchdog/` assets.
The role archives committed source; uncommitted checkout changes are excluded.
Use a hosted Git URL when appropriate. Install the dependency:

```sh
uv run ansible-galaxy collection install -r meta/vpn-sidecar-requirements.yml -p .
```

Include `collections_path = .` in the consumer's existing `[defaults]` section
of `ansible.cfg`, preserving any other required collection paths. Ignore the
installed `ansible_collections/` dependency and `.cache/` in that project.

### Supply consumer settings and encrypted credentials

Create `inventory/vpn/settings.yaml`:

```yaml
vpn_sidecar_backend: l2tp-ipsec
vpn_sidecar_package_dir: /absolute/path/to/l2tp-ipsec-vpn-sidecar
vpn_sidecar_package_version: REPLACE_WITH_SAME_FULL_COMMIT
vpn_sidecar_instance: example
vpn_sidecar_project_name: vpn-example
vpn_sidecar_runtime_dir: /srv/example-project/vpn/instances/example
vpn_sidecar_credentials_dir: /srv/example-project/vpn/instances/example/secrets
vpn_sidecar_server: 203.0.113.10
vpn_sidecar_health_host: 192.0.2.10
vpn_sidecar_health_port: 22
```

Replace both documentation addresses with the real gateway and private TCP
health endpoint. The package directory is on the **controller** and must contain
the pinned commit. The runtime and credential directories are on the **VM**.
Keep the collection and package revision pins aligned. Profile/Compose names
must be unique on the Docker host. Override DNS and IKE/ESP proposals when your
gateway requires them; see the [backend prerequisites](../roles/vpn_sidecar/tasks/prerequisites.yaml).

Create the vault with an interactive editor:

```sh
uv run ansible-vault create inventory/vpn/secrets.vault.yaml
```

In that editor, supply the following mapping with actual credentials:

```yaml
vpn_sidecar_credentials:
  username: REPLACE_WITH_USERNAME
  password: REPLACE_WITH_PASSWORD
  psk: REPLACE_WITH_PRE_SHARED_KEY
```

Save only the encrypted vault in the consumer project. Keep its vault password
outside source control. Credential deployment tasks suppress output and diffs.

### Deploy and verify

Create `playbooks/deploy-vpn.yaml`:

```yaml
- name: Deploy the consumer VPN
  hosts: vpn_sidecars
  become: true
  gather_facts: true
  vars_files:
    - ../inventory/vpn/settings.yaml
    - ../inventory/vpn/secrets.vault.yaml
  roles:
    - vpn_sidecar.client.vpn_sidecar
```

From the consumer project, check syntax, then deploy explicitly to this VM:

```sh
uv run ansible-playbook -i inventory/vpn/hosts.yaml playbooks/deploy-vpn.yaml --limit vpn-host --ask-vault-pass --syntax-check
uv run ansible-playbook -i inventory/vpn/hosts.yaml playbooks/deploy-vpn.yaml --limit vpn-host --ask-vault-pass
```

Add `--ask-become-pass` if the VM's sudo policy requires a password. The role
waits for VPN health before starting the watchdog. Enable the optional
`vpn_sidecar_verify_connection` SSH check described in the
[backend role](../roles/vpn_sidecar/tasks/main.yaml), or independently verify authenticated
access to the intended private destination. Repeat the playbook unchanged and
check that it reuses the existing containers. Updates and credential rotations
use this same playbook with reviewed pins/settings and the encrypted vault.

### Provision XAuth instead

Set `vpn_sidecar_backend: ipsec-xauth`. Keep the runtime layout from the example:
`.../instances/example`, credentials in its `secrets/` directory, and project
name `vpn-example`. XAuth uses the same vault dictionary (`username`, `password`,
`psk`); the role writes its backend-specific credential file privately. Supply a
numeric IPv4 gateway and compatible IKE/ESP proposals.

The role prepares a consumer profile at
`/srv/example-project/vpn/profiles/example/profile.env` and reports an interactive
connection command. Run the reported command in a terminal on the VM; it uses
the pinned source launcher, the generated profile directory, `--local`, and
`example connect`. The emailed MFA code is entered only into that terminal's
hidden prompt. Ansible does not request, store or submit it. The launcher uses
noninteractive `sudo -n` on the VM; arrange that access under the host policy
for the SSH user before login. Ansible privilege escalation alone does not
configure this access.

Run provisioning and launcher operations one at a time for each instance; do
not run provisioning concurrently with login, logout or another provisioning
process. The role does not hold the launcher lock across its Ansible tasks.

An unchanged provisioning run preserves an existing session. Disconnect the
selected instance with the launcher before changing an active XAuth deployment,
then provision and reconnect interactively. The watchdog reports failed XAuth
connections but cannot supply fresh MFA.

### Running kernel requirements

Containers share the VM kernel. Installing a different container image cannot
repair a host kernel crash. The role checks IPsec/XFRM support, loads PPP modules
and verifies `/dev/ppp` for L2TP, and can install missing module packages for the
**running** kernel. It does not upgrade or select a kernel or reboot the VM.

The L2TP crash signature on `6.8.0-146-generic` matches the tracked Ubuntu
regression [LP 2169477](https://bugs.launchpad.net/bugs/2169477), with related
reports in [LP 2169269](https://bugs.launchpad.net/bugs/2169269). Booting the
previously installed `6.8.0-142-generic` restored service in the investigated
ARM64 VM and passed initial checks. See the [kernel regression note](kernel-regression.md)
for evidence, workaround limits and the dated upstream status. The role warns
for the affected release by default; this does not implicate every 6.8 kernel
or establish an XAuth reproduction.

Consumer policy can enforce approved or blocked releases in its settings:

```yaml
vpn_sidecar_kernel_allowlist: []  # Set exact releases verified for your VM.
vpn_sidecar_kernel_blocklist:
  - 6.8.0-146-generic
vpn_sidecar_install_kernel_modules: true
```

The block list above is an example of a stricter consumer policy; the default
block list is empty. A nonempty allow list admits only listed releases. Resolve
policy failures by selecting a verified kernel through the VM's maintenance
process, then rerun provisioning after the planned boot. SSH, Python 3 and sudo
must already be available for Ansible to reach and configure the VM.

## Alternative: deploy with the profile launcher

The following steps apply to either backend when deploying without Ansible.
They provision source, profiles and credentials manually.

## 1. Prepare the VM

Use a Linux VM with kernel IPsec/XFRM support and SSH access. Install Docker
Engine, the Compose plugin and Buildx using the
[official Ubuntu installation guide](https://docs.docker.com/engine/install/ubuntu/).
Configure a workstation SSH alias named `vpn-host` for this VM.

The launcher uses rootful Docker through noninteractive `sudo`, including
supporting commands during connection. Have your administrator provision that
access according to the host's policy; Docker group membership alone does not
satisfy the launcher's `sudo -n` calls. On the **VM**, check:

```sh
sudo -n true
sudo -n docker info
sudo -n docker compose version
sudo -n docker buildx version
command -v bash flock
```

Allow the VM to reach the VPN gateway over UDP 500 and 4500. Connections that
do not use NAT traversal may also require ESP (IP protocol 50). The gateway
must support the selected backend and its configured IKE/ESP proposals.

For **L2TP only**, load the PPP modules on the VM and verify its PPP device:

```sh
sudo modprobe ppp_generic
sudo modprobe ppp_async
sudo modprobe l2tp_ppp
ls -l /dev/ppp
printf '%s\n' ppp_generic ppp_async l2tp_ppp | sudo tee /etc/modules-load.d/l2tp-vpn.conf
```

On the **VM**, create the source and consumer directories, owned by the account
used for deployment:

```sh
sudo install -d -m 755 -o "$(id -un)" -g "$(id -gn)" \
  /srv/vpn-sidecar/source /srv/example-project/vpn/profiles/example
```

## 2. Create a profile in the consumer project

On the **workstation**, set paths to your existing checkouts:

```sh
VPN_SOURCE=/absolute/path/to/l2tp-ipsec-vpn-sidecar
VPN_CONSUMER=/absolute/path/to/consumer-project
mkdir -p "$VPN_CONSUMER/vpn/profiles/example"
```

Choose a profile name unique on this Docker host. This guide uses `example`;
its Docker Compose project will be `vpn-example`.

Create `vpn/profiles/example/profile.env` in the consumer project with **one**
of the following configurations. Use literal, unquoted values; do not put
passwords, pre-shared keys or authentication codes here.

For **L2TP/IPsec**:

```text
VPN_BACKEND=l2tp-ipsec
VPN_SERVER=203.0.113.10
VPN_HEALTH_HOST=192.0.2.10
VPN_HEALTH_PORT=22
VPN_IMAGE=vpn-sidecar-l2tp:local
VPN_DNS=192.0.2.53
VPN_SSH_HOST=vpn-host
VPN_REMOTE_ROOT=/srv/vpn-sidecar/source
VPN_REMOTE_PROFILES_DIR=/srv/example-project/vpn/profiles
```

For **IPsec/XAuth with emailed MFA**:

```text
VPN_BACKEND=ipsec-xauth
VPN_SERVER=203.0.113.10
VPN_HEALTH_HOST=192.0.2.10
VPN_HEALTH_PORT=22
VPN_IMAGE=vpn-sidecar-ipsec-xauth:local
VPN_SSH_HOST=vpn-host
VPN_REMOTE_ROOT=/srv/vpn-sidecar/source
VPN_REMOTE_PROFILES_DIR=/srv/example-project/vpn/profiles
```

The health endpoint must be a stable private TCP service reachable through the
VPN. For L2TP, replace `VPN_DNS` with a reachable resolver, or omit it when the
VPN supplies suitable DNS. XAuth requires a numeric IPv4 gateway. Set optional
IKE/ESP proposals to match your gateway; see the
[profile reference](profiles.md) and backend guides linked from the README.

Add `/vpn/instances/` to the **consumer project's** `.gitignore`. Profiles can
be tracked in that project; runtime credentials and logs must stay untracked.
Validate the profile on the workstation:

```sh
bash "$VPN_SOURCE/scripts/vpn" --profiles-dir "$VPN_CONSUMER/vpn/profiles" example validate
```

Validation checks profile syntax and settings. It does not contact the VM or
validate the gateway's compatibility.

## 3. Copy source and profile to the VM

Use a reviewed, tested source checkout. These **workstation** commands copy
reusable source and the selected consumer profile separately:

```sh
rsync -a --exclude='.git' --exclude='.venv' --exclude='.ansible' \
  --exclude='__pycache__' --exclude='.pytest_cache' --exclude='.env' \
  --exclude='secrets' --exclude='instances' --exclude='*.log' \
  "$VPN_SOURCE/" vpn-host:/srv/vpn-sidecar/source/
rsync -a "$VPN_CONSUMER/vpn/profiles/example/" \
  vpn-host:/srv/example-project/vpn/profiles/example/
```

The launcher does **not** upload files. With `VPN_SSH_HOST` set, it sends an
operation and profile name over SSH, then reads the profile already on the VM.
Repeat the profile copy after changing local settings. Keep shared source and
consumer profiles in their separate directories.

## 4. Provision credentials on the VM

On the **VM**, create the instance and secrets directories:

```sh
umask 077
mkdir -p /srv/example-project/vpn/instances/example/secrets
chmod 700 /srv/example-project/vpn/instances \
  /srv/example-project/vpn/instances/example \
  /srv/example-project/vpn/instances/example/secrets
```

For **L2TP**, create three private files, then use an editor to enter the
username, password and pre-shared key into their respective files:

```sh
touch /srv/example-project/vpn/instances/example/secrets/username \
  /srv/example-project/vpn/instances/example/secrets/password \
  /srv/example-project/vpn/instances/example/secrets/psk
chmod 600 /srv/example-project/vpn/instances/example/secrets/{username,password,psk}
```

For **XAuth**, copy the credential template on first deployment:

```sh
cp /srv/vpn-sidecar/source/containers/ipsec-xauth/vpn.credentials.example \
  /srv/example-project/vpn/instances/example/secrets/vpn.credentials
chmod 600 /srv/example-project/vpn/instances/example/secrets/vpn.credentials
```

Edit that file privately and fill in `USERNAME`, `PASSWORD` and
`PRE_SHARED_KEY`. Do not repeat the template copy over existing credentials.
Keep secrets out of command arguments, shell history and source control.

## 5. Build and connect

From the **workstation**, build the selected VPN and shared watchdog images on
the VM, then connect:

```sh
bash "$VPN_SOURCE/scripts/vpn" --profiles-dir "$VPN_CONSUMER/vpn/profiles" example build
bash "$VPN_SOURCE/scripts/vpn" --profiles-dir "$VPN_CONSUMER/vpn/profiles" example connect
bash "$VPN_SOURCE/scripts/vpn" --profiles-dir "$VPN_CONSUMER/vpn/profiles" example status
```

Building does not initiate a VPN login. XAuth connection requests an emailed
code through a hidden terminal prompt; enter the current code when prompted.
A healthy existing XAuth session is reused. L2TP uses the saved credentials.

To run directly on the **VM**, add `--local` before the profile name:

```sh
bash /srv/vpn-sidecar/source/scripts/vpn \
  --profiles-dir /srv/example-project/vpn/profiles --local example status
```

A successful connection verifies private TCP access. Also test an authenticated
operation against your intended destination using the consumer project's SSH
or application configuration. The containers do not automatically route all
workstation traffic through the VPN. See
[private service access guide](remote-services.md) for a complete SSH
configuration and local port-forward example through the VPN container.

## 6. Operate and update

The first connection writes the resolved runtime manifest to
`/srv/example-project/vpn/instances/example/compose.yaml`. On the **VM**, inspect
the three services and watchdog logs after connecting:

```sh
sudo -n docker compose -f /srv/example-project/vpn/instances/example/compose.yaml ps
sudo -n docker compose -f /srv/example-project/vpn/instances/example/compose.yaml logs --tail=50 watchdog
```

The watchdog uses the VM's Docker socket mounted into its container. No host
watchdog service is needed. L2TP recovers automatically after failed probes,
subject to its cooldown. XAuth reports failures and requires an interactive
`connect` for fresh MFA. See the [watchdog guide](../watchdog/README.md).

Disconnect from the **workstation** with:

```sh
bash "$VPN_SOURCE/scripts/vpn" --profiles-dir "$VPN_CONSUMER/vpn/profiles" example disconnect
```

This removes the profile's three containers and network; credentials and
watchdog state remain. For a planned source update, disconnect the affected
profile, copy the reviewed source and profile again, rebuild, reconnect and
repeat destination verification. Keep the source and runtime paths stable:
the watchdog mounts them at the same absolute paths used by Docker on the VM.

For automated provisioning, use the
[backend role source](../roles/vpn_sidecar/tasks/main.yaml) described above. XAuth login
remains interactive after provisioning. The bundled [agent skill](../.agents/skills/ensure-vpn/SKILL.md)
covers connection and recovery after deployment.
