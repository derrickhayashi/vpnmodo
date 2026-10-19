# Ansible collection

This repository packages the shared role as `vpn_sidecar.client.vpn_sidecar`.
Consumer repositories own the playbook, inventory, settings, encrypted
credentials, and dependency requirements. Install the collection before running
the playbook; role installation is not a deployment pre-task.

## Install a pinned dependency

Put a requirements file in the consumer repository:

```yaml
collections:
  - name: git+file:///absolute/path/to/l2tp-ipsec-vpn-sidecar
    type: git
    version: REPLACE_WITH_FULL_COMMIT
```

Use an accessible Git URL in place of the local file URL when the repository
is hosted. Pin a reviewed full commit, then install through standard Galaxy:

```sh
uv run ansible-galaxy collection install -r meta/vpn-sidecar-requirements.yml -p .
```

The consumer's Ansible configuration must include that installation directory
in `collections_path`. The installed `ansible_collections/` directory is a
Git-ignored dependency, not a maintained source copy.

Load the role normally:

```yaml
- name: Deploy a VPN consumer
  hosts: vpn_sidecars
  become: true
  gather_facts: true
  vars_files:
    - ../inventory/vpn-settings.yaml
    - ../inventory/vpn-secrets.vault.yaml
  roles:
    - vpn_sidecar.client.vpn_sidecar
```

The role renders the selected pinned backend and shared watchdog overlay together
with Docker Compose on the controller, validates the result, and deploys one
fully resolved `compose.yaml` on Ubuntu. No build definition is included in
that runtime file. Consumer settings are also written to `.env`. Credentials
stay in protected files on Ubuntu and are never included in rendered Compose
output. The pinned package supplies both image build contexts. Updating the
collection dependency does not recreate containers if runtime inputs stay equal.

The collection dependency and `vpn_sidecar_package_version` must both select
revisions containing the selected `containers/<backend>/`, launcher, and
`watchdog/` assets. The role checks
all required pinned source assets before changing the VPN host. Update both
pins together when adopting containerized recovery.

Set `vpn_sidecar_registry_image` and `vpn_sidecar_watchdog_registry_image` to
published image tags or digests to pull them instead of building locally. Log
in to a private registry as the account running Docker before deployment.
To use images already built or loaded on the host, set the corresponding
`vpn_sidecar_pull_image` or `vpn_sidecar_watchdog_pull_image` to `false`.
The runtime package pin still selects the Compose configuration and local
build sources; use the source revision used to publish the images.

`vpn_sidecar_backend` defaults to `l2tp-ipsec`. L2TP deployment starts the VPN,
application and automatic watchdog after health validation. Choose
`ipsec-xauth` to provision source, images, profile and credentials without
initiating login or requesting MFA. Its watchdog mode is manual.

For XAuth, set `vpn_sidecar_project_name: vpn-<instance>`,
`vpn_sidecar_runtime_dir: /absolute/consumer/vpn/instances/<instance>`, and
`vpn_sidecar_credentials_dir: <runtime>/secrets`. The role generates the sibling
`profiles/<instance>/profile.env` and prints an interactive launcher command.
Run that command on the VM after provisioning. The launcher handles emailed
MFA and starts all three services. Both backends use the same vault input keys:
`username`, `password`, `psk`. XAuth writes a protected `vpn.credentials` file
rather than three individual secret files.

XAuth input changes while the selected project has running containers require
an explicit launcher disconnect before reprovisioning. An unchanged run
preserves the existing session. Ansible never submits an authentication code. Serialize provisioning and
launcher operations for an instance; the role does not hold the launcher lock
across Ansible tasks.
See the [deployment guide](../docs/deployment.md) for a complete example.

## Role inputs

| Variable | Purpose |
| --- | --- |
| `vpn_sidecar_backend` | `l2tp-ipsec` (default) or `ipsec-xauth` |
| `vpn_sidecar_install_docker` | Install missing Docker Engine/Compose/Buildx dependencies; defaults to true |
| `vpn_sidecar_install_kernel_modules` | Install available module packages for the running kernel when required; defaults to true |
| `vpn_sidecar_kernel_allowlist` | Optional exact running kernel releases permitted; empty allows any not blocked |
| `vpn_sidecar_kernel_blocklist` | Exact running kernel releases rejected before provisioning; defaults to empty |
| `vpn_sidecar_kernel_warning_releases` | Releases producing an incident warning; defaults to `6.8.0-146-generic` |
| `vpn_sidecar_registry_image` | Optional published image tag or digest; empty builds locally |
| `vpn_sidecar_watchdog_registry_image` | Optional watchdog image tag or digest; empty builds pinned source locally |
| `vpn_sidecar_watchdog_pull_image` | Pull the configured watchdog registry image; defaults to true |
| `vpn_sidecar_watchdog_interval` | Seconds between watchdog checks; defaults to 30 |
| `vpn_sidecar_watchdog_cooldown` | Seconds between recovery attempts; defaults to 300 |
| `vpn_sidecar_package_dir` | Controller checkout of this repository |
| `vpn_sidecar_package_version` | Pinned runtime Git commit, 7–40 hexadecimal characters |
| `vpn_sidecar_runtime_dir` | Absolute runtime directory on Ubuntu |
| `vpn_sidecar_credentials_dir` | Absolute runtime credential directory |
| `vpn_sidecar_project_name` | Unique Compose identity |
| `vpn_sidecar_instance` | Consumer instance name |
| `vpn_sidecar_server` | VPN server address or DNS name |
| `vpn_sidecar_health_host`, `vpn_sidecar_health_port` | Private TCP health target |
| `vpn_sidecar_credentials` | Username, password, and psk dictionary from the consumer vault |

The connection must define `ansible_user`, provide Python 3 on Ubuntu, and
support sudo. The controller requires Git, Ansible, and Docker Compose v2 (rendering does
not require a running Docker daemon). Connect directly to the
VPN host instead of routing deployment through the VPN being managed.

The role installs VM utilities and checks Docker daemon, Compose and Buildx
availability. It uses packages matching an existing Docker CE installation or
the Ubuntu Docker package family when installing missing components. An
incompatible or incomplete existing installation fails validation rather than
being silently replaced.

It verifies kernel IPsec/XFRM support and, for L2TP only, PPP modules and
`/dev/ppp`. Missing module packages target the exact running kernel. Repository
availability and kernel support remain requirements; there is no automatic
kernel upgrade, downgrade or reboot. The recorded L2TP crash on
`6.8.0-146-generic` matches Ubuntu regression
[LP 2169477](https://bugs.launchpad.net/bugs/2169477); see the
[kernel regression note](../docs/kernel-regression.md) for matching evidence and
the applied `6.8.0-142-generic` workaround with initial acceptance. The
exact-release warning is advisory by default. Use a consumer allow/block list
to enforce your tested kernel policy; this is not a blanket finding for 6.8
kernels or evidence that XAuth triggers the same bug.

Optional defaults include PPP-provided DNS, NAT-T enabled, backend-specific
crypto proposals, a 120-second L2TP startup timeout, Docker installation enabled,
and a 300-second recovery cooldown. Override these in consumer settings.

SSH acceptance is optional. Set `vpn_sidecar_verify_connection: true` and
provide `vpn_sidecar_verify_ssh_config`, `vpn_sidecar_verify_ssh_alias`, and
`vpn_sidecar_expected_hostname` to enable it.

The watchdog runs as a third Compose service, mounting the Docker socket and
read-only consumer, source, and credential paths at their original absolute
paths so Compose can recreate the sibling services. It has broad
Docker control through the socket. Cooldown state lives in a per-project named
volume. No watchdog script, service, timer, or configuration is installed on the
host. Docker itself remains a host service.

`vpn_sidecar_retired_timer` defaults to empty. For an explicitly approved
migration, set it to the exact existing timer unit, for example
`vpn-watchdog@example.timer`; the role checks whether this exact timer and
its corresponding `vpn-watchdog@example.service` are loaded, including units
provided through systemd templates. It stops and disables the selected timer,
then stops any running corresponding service before deploying containers.
Use a concrete instance name, rather than an uninstantiated template name such
as `vpn-watchdog@.timer`. It does not discover or stop unrelated host watchdogs.
Existing unit files are retained; remove those separately if no other consumer
uses them.

Credential tasks suppress output and diffs. For L2TP the role previews runtime changes,
stops its existing watchdog container before replacing changed inputs, starts
the VPN/application services and checks VPN health, then starts the watchdog.
An unchanged run reuses the existing containers.

Deployment errors remain visible as the original failing Ansible task. There
is no automatic configuration or credential rollback and no temporary rollback
snapshots. If validation fails after stopping the watchdog, it remains stopped
so that recovery cannot interfere with diagnosis. Correct the failing inputs
and rerun the playbook to restore monitoring.

For manual recovery, restore the consumer settings, encrypted vault, and runtime
package pin to a known working revision, then rerun the consumer playbook. If
credentials were rotated with the VPN provider, coordinate their restoration
before redeploying. The role does not switch deployment versions on its own.

Validate syntax, lint, check mode, an approved live deployment, and an unchanged
rerun in each consumer. VM lifecycle and workstation tunnels stay in consumers.
