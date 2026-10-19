# Isolated Ansible VM preparation acceptance

This lab runs the real `vpn_sidecar.client.vm_prepare` role against disposable
Ubuntu 24.04 ARM64 containers with genuine systemd as PID 1. Docker is absent
from the fixture image. The role installs Ubuntu's rootful Engine, Compose and
Buildx packages and starts its own nested daemon through systemd. No VPN login,
profile, controller, watchdog or application container is started.

The container shares the VM's kernel and is privileged. It isolates package,
account, service and Docker state, not hostile code. Use only trusted committed
source on the already authorized local Linux lab VM. Do not target a customer
host or run the role directly against a shared VM for this test.

The VM must already have Docker, Python 3, cgroup v2, matching `/lib/modules`
metadata and loaded `xfrm_user`/`esp4`. The full L2TP fixture additionally requires
loaded `ppp_generic`/`ppp_async` and `/dev/ppp`. The runner checks these conditions;
it never installs VM packages, loads new VM modules, changes its kernel or
restarts its daemon. The collection's matching-kernel package installation path
therefore remains outside this container acceptance. `l2tp_ppp` is not required
or loaded: the fixture validates the ordinary userspace-L2TP preparation path.

## Prepare committed source on the Mac

Commit and scan the intended private branch first. In that private checkout:

```sh
test -z "$(git status --porcelain)"
bundle=$(mktemp -d /tmp/vpnmodo-vm-prepare-source.XXXXXXXX)
chmod 700 "$bundle"
git archive HEAD | tar -xf - -C "$bundle"
```

Transfer only this archive directory into a private temporary directory on the
authorized VM using the previously verified SSH connection. Keep the address,
identity, known-hosts file and execution evidence outside the repository. Use
`StrictHostKeyChecking=yes`, `ForwardAgent=no` and the private pinned
`UserKnownHostsFile`. Do not copy a working tree, ignored runtime files or
consumer inventories. This does not need Docker Desktop on the Mac.

## Run on the authorized VM

Set `source` to the transferred extracted archive directory:

```sh
source=/absolute/private/path/to/extracted-committed-source
VPNMODO_AUTHORIZED_VM_PREPARE_LAB=1 python3 \
  "$source/tests/vm-prepare-lab/run.py" --source "$source" --sudo
```

Omit `--sudo` when the VM account already has direct Docker access. `--scenario
full` selects only the L2TP fixture; `--scenario ipsec-only` selects a separate
fresh fixture without `/dev/ppp`. By default both run. Ansible is installed in
the disposable fixture's venv, pinned to `ansible-core==2.19.3` (Python 3.12).

Each run owns a UUID-named image and outer container IDs with the matching
`io.vpnmodo.vm-prepare-lab` label. The runner records existing VM container IDs,
status and `StartedAt` before running, compares them after exact cleanup, and
checks for unexpected additions/removals. It removes only its recorded IDs
after verifying their ownership label, then its unique image tag. It never
mounts the VM Docker socket or host network and never uses Docker prune.

The outer containers use `--privileged --cgroupns=private`, private `/run` and
`/run/lock`, read-only source and read-only `/lib/modules`. No writable VM cgroup
root mount is supplied. If the VM's Docker/runtime cannot provide writable
cgroup delegation inside that private namespace, systemd may fail to start.
That is an explicit environment limitation: do not bind the VM cgroup root
writable, use fake systemctl commands or claim fresh-VM acceptance. Existing
VM workloads must remain unchanged. The fixture masks automatic host-module,
sysctl and udev startup services because those are outside this test.
PID 1 and the test executor also drop `CAP_SYS_MODULE` from their capability
bounding sets; already loaded required modules still pass the real `modprobe`
checks. The runner compares loaded VM module names before/after because kernel
network operations can also request automatic module loading. Any difference
fails acceptance and must be investigated without unloading shared VM modules.

The private evidence directory is printed at startup. Before exact cleanup the
runner copies synthetic detailed Ansible output and generated playbooks from
the owned container into its private evidence directory. The host also retains
synthetic runner output and before/after state records. If the
runner fails, inspect its private log before removing the exact local evidence
and transferred bundle directories. A forceful process kill can bypass Python
cleanup: use `owned.json` in that private directory to inspect each exact ID and
verify its label before `docker rm -f ID`. Never stop an unverified container.

## Assertions and limits

The fixtures cover fresh check mode without managed-state changes, real package
installation and service activation, a second apply reporting `changed=0`, and
prepared check mode reporting `changed=0`. Check-mode snapshots compare package
versions/state, account/group files, sudo policy, module persistence and Docker
service state; Ansible temporary files are outside that promise. The build keeps
its apt metadata so the fresh check can plan package installation without
updating target package lists.

Synthetic existing accounts exercise explicitly selected direct group access,
pre-authorized noninteractive sudo and denied sudo. Sudo policy is created only
by the fixture and must remain unchanged by the role. Missing account, invalid
access/L2TP input and blocked running kernel must fail before managed changes.
The full fixture temporarily hides its own private `/dev/ppp` node to require a
real device-readiness refusal, then restores it and requires idempotence. The
IPsec-only fixture proves preparation succeeds without PPP. Both require zero
Docker containers and no controller data directories after preparation.

This is service/package/account acceptance in an isolated Ubuntu container,
not provisioning or reboot testing of a new VM. It does not establish kernel
package repair, kernel stability, actual SSH transport, VPN negotiation,
XFRM output-mark enforcement or traffic isolation. Those remain separately
documented checks in the [existing protocol lab](../local-lab/README.md).
