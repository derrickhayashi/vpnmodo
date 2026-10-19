# VM preparation validation — 2026-10-19

## Summary

- **[VERIFIED] Local suite:** all 79 Python tests pass with Ansible 2.21.5 and Python 3.12 on macOS ARM64, including 15 VM preparation tests.
- **[VERIFIED] Collection:** `vpn_sidecar.client` 0.4.0 builds, installs into a temporary collection directory, and resolves `vm_prepare` through its packaged FQCN.
- **[VERIFIED] Linux functionality:** both isolated Ubuntu 24.04 ARM64/systemd fixtures pass preparation, check mode, account policy and idempotence assertions.
- **[VERIFIED] Preservation is inconclusive:** an existing VM container disappears and another changes state during the run. Owned fixture cleanup succeeds; the overall runner exits 1.
- **Limits:** these results do not qualify fresh-VM kernel package repair, reboot persistence, actual SSH transport, Docker CE repair or VPN traffic.

The tested source is `78bfd940d1b06403bce1ac5bcf86f85babd1e004`.
Subsequent changes pin the setup guide and improve failure diagnostics in the
lab runner. The diagnostic changes pass local simulated snapshot checks but
have no additional Linux acceptance run.

Detailed synthetic logs, SSH connection details and VM state snapshots remain
outside the repository. GitHub Actions stays disabled.

## Local checks

```sh
uv run --no-project --python 3.12 --with ansible-core==2.21.5 \
  --with pyyaml --with jinja2 \
  python -m unittest discover -s tests -p 'test_*.py' -v
```

Result: **79 tests pass**. The 15 VM tests include real Ansible control flow
with synthetic OS actions: fresh/prepared state, check/apply/rerun, explicit
management account selection, fresh supplementary group acquisition, sudo
preflight, Docker package-family policy, rootful Docker/Compose v2 validation,
kernel policy, optional acceleration and PPP/device failures. Those fixtures
alone do not establish real package or service behavior.

Collection packaging also passes: `ansible-galaxy collection build`, installing
the resulting `vpn_sidecar-client-0.4.0.tar.gz` into a temporary directory, then
`ansible-playbook --syntax-check` using `vpn_sidecar.client.vm_prepare`.

## Isolated Linux functional results

The [preparation lab](../tests/vm-prepare-lab/README.md) runs genuine systemd as
PID 1 and initially has no Docker Engine. Each fixture installs Ubuntu's Engine,
Compose and Buildx packages and starts its own rootful nested daemon. It receives
no VM Docker socket or host network. The IPsec-only fixture removes only its
private PPP device node before testing.

| Check | L2TP enabled | IPsec only, no `/dev/ppp` |
| --- | --- | --- |
| Fresh check mode with managed-state snapshots unchanged | Pass, `changed=4` | Pass, `changed=4` |
| Fresh package installation, Docker active/enabled and account access | Pass, `changed=4` | Pass, `changed=4` |
| Unchanged apply | Pass, `changed=0` | Pass, `changed=0` |
| Prepared check mode with snapshots unchanged | Pass, `changed=0` | Pass, `changed=0` |
| Pre-authorized sudo apply/check without policy or group grants | Pass, `changed=0` | Pass, `changed=0` |
| Missing account, invalid access/L2TP input, blocked kernel and unauthorized sudo | Expected refusals | Expected refusals |
| Missing PPP device followed by restoration | Expected refusal, then `changed=0` | PPP checks omitted |
| Persisted module selection and zero created containers/controller data | Pass | Pass |

Check-mode snapshots cover package versions/state, account/group files, sudo
policy, module persistence, Docker configuration and Docker service state.
Ansible temporary files are outside that comparison. Required kernel modules
are already loaded on the VM; the fixture drops `CAP_SYS_MODULE`, mounts module
metadata read-only and disables matching-kernel package installation.
The ordinary L2TP path does not require or persist `l2tp_ppp`.

## Overall preservation result

The functional scenarios pass, but the complete runner **exits 1** at the final
comparison with pre-existing VM containers. One previously running container is
absent, and one surviving container has changed status and `StartedAt`.
The user confirms that the VM is shared and does not know whether other
activity changed containers during the run (2026-10-19). Preservation is
therefore inconclusive. The cause is not established by this run; attribution
to either the fixture or another actor requires additional evidence. The
kernel journal contains no OOM indicators during the recorded test interval.

The exact fixture IDs are disjoint from all baseline IDs, and all owned fixture
containers are removed. Cleanup targets only recorded IDs with verified ownership
labels and the unique fixture image tag. Existing deployments receive no repair,
restart or rollback actions. The final loaded-module comparison is not reached
in this run because the container snapshot raises first.

This is a **functional pass with an inconclusive preservation result**. The
runner still exits 1 because its strict snapshot comparison fails. Full harness
acceptance requires an authorized lab interval with controlled concurrent
activity and a successful preservation comparison. No additional acceptance
run is initiated on the shared VM after this clarification. No existing workload
should be modified to manufacture a matching snapshot.

## Remaining qualification

The containers share an already prepared VM kernel. They cannot establish
fresh-VM kernel package availability/repair, reboot persistence or kernel
stability. Docker CE selection and package conflicts have local policy coverage;
live acceptance uses Ubuntu's Docker packages. SSH transport and VPN negotiation,
credentials, MFA, enforcement and traffic require their separate workflows.
VM provisioning and Intel native qualification are outside issue #24.
