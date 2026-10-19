# Ansible VM preparation

Use `vpn_sidecar.client.vm_prepare` to prepare an existing Ubuntu 24.04 ARM64
(aarch64) VM for vpnmodo. The role reuses
`roles/vpn_sidecar/tasks/prerequisites.yaml` for shared host preparation and adds
an explicit management-account access policy. It requires SSH, Python 3 and
administrative access already working on the VM; it does not provision a VM.

The [Mac-to-VM guide](../docs/vm-setup.md) provides pinned Ansible installation,
private collection requirements, inventory, playbook, host-key verification,
check/apply commands and unchanged-rerun checks. Ansible runs directly on the
Mac without Docker Desktop or controller-side Compose. Keep the real inventory
and playbook outside this repository.

```yaml
---
- name: Prepare an existing VM
  hosts: vpn_vms
  gather_facts: true
  become: true
  vars:
    vpnmodo_vm_management_user: VM_USER
    vpnmodo_vm_docker_access: direct
    vpnmodo_vm_l2tp: true
  roles:
    - vpn_sidecar.client.vm_prepare
```

`VM_USER` must be an explicitly selected, existing account. Connect directly to
the VM rather than through the VPN being managed. No VPN server, health target,
credentials, profile, consumer instance, deployment directory or runtime package
pin is required. Preparation does not load images, create profiles, start a
controller, initiate MFA/login or deploy legacy containers/watchdogs. Continue
with [vpnmodo host setup and profile commands](../docs/offline-installation.md#2-load-the-container-images-on-the-vm)
after preparation succeeds.

## Preparation inputs

| Variable | Purpose |
| --- | --- |
| `vpnmodo_vm_management_user` | Required explicit existing account that will manage vpnmodo |
| `vpnmodo_vm_docker_access` | `direct` (default) adds that account to the Docker group; `sudo` validates its existing noninteractive sudo access without granting policy |
| `vpnmodo_vm_l2tp` | `true` (default) prepares PPP for L2TP/IPsec; `false` prepares IPsec/XAuth only |
| `vpn_sidecar_l2tp_acceleration` | `false` (default) permits userspace L2TP; `true` strictly requires the optional `l2tp_ppp` kernel acceleration module |
| `vpn_sidecar_install_docker` | `true` (default) installs missing Docker Engine, Compose v2 and Buildx dependencies |
| `vpn_sidecar_install_kernel_modules` | `true` (default) installs available module packages matching only the running kernel when required |
| `vpn_sidecar_kernel_allowlist` | Exact running kernel releases allowed; empty (default) allows any release not blocked |
| `vpn_sidecar_kernel_blocklist` | Exact running kernel releases rejected; empty by default |
| `vpn_sidecar_kernel_warning_releases` | Exact releases producing an advisory warning; defaults to `['6.8.0-146-generic']` |

Direct Docker access gives administrative authority over the VM. A fresh SSH
login is required after group membership changes. Sudo mode does not add Docker
group membership or write sudoers policy; use [vpnmodo sudo mode](../docs/onboarding.md#sudo-mode)
consistently for subsequent image loading and controller setup. Ansible's
optional `-K` prompt authorizes preparation only, while vpnmodo sudo mode needs
pre-authorized `sudo -n` access as the selected account.

The shared prerequisites install baseline utilities and use the package family
matching an existing Docker CE installation, or Ubuntu's Docker packages for a
fresh host. An incompatible mixed or unrecognized incomplete installation fails
instead of being replaced. With installation switches set to `false`, required
components must be provided externally. Preparation enables/starts Docker but
does not restart an already running daemon.

Both backends require IPsec/XFRM support. L2TP additionally requires PPP modules
and `/dev/ppp`; `l2tp_ppp` is required only with acceleration explicitly enabled.
The role loads and persists required modules. It does not upgrade/downgrade the
boot kernel or reboot. Available packages for the exact running kernel and
working device management remain host requirements. Kernel allow/block/warning
policies apply before preparation. Review the [kernel regression note](../docs/kernel-regression.md):
module availability does not establish tunnel stability, and the recorded L2TP
incident is not a blanket finding for all 6.8 kernels or XAuth.

## Validation and scope

See the [preparation validation record](../docs/vm-preparation-validation.md)
for the exact local checks, isolated Linux results and remaining limits.

Run syntax checking, `--check --diff`, apply and an unchanged rerun as shown in
the VM guide. Check mode uses read-only probes and reports planned changes when
the host is incomplete. It never installs packages, changes accounts, starts
services or loads modules. Apply performs final Docker daemon, management-account
access, XFRM and selected PPP checks. An unchanged successful rerun should report
`changed=0`. Preparation cannot prove VPN negotiation, credentials, MFA or traffic
health; those require later vpnmodo acceptance.

The collection still contains the older `vpn_sidecar.client.vpn_sidecar` full
deployment role. That workflow requires consumer settings/credentials and Docker
Compose on the Ansible controller for rendering. It is separate from managed
vpnmodo VM preparation; new managed installations should use `vm_prepare` and
continue through vpnmodo.
