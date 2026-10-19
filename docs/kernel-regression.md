# Ubuntu 6.8.0-146 L2TP kernel regression

Documented 2026-10-15. This affects the VM's kernel, which VPN containers share.
The reported failure is in kernel L2TP handling; replacing a container image or
restarting a watchdog cannot repair it.

## Ubuntu bug references

[Launchpad bug 2169477](https://bugs.launchpad.net/bugs/2169477) tracks crashes
and soft lockups with kernel `6.8.0-146-generic`, package `6.8.0-146.146`, during
L2TP/IPsec operation on Ubuntu Noble. Its
[original bug notification](https://www.mail-archive.com/ubuntu-bugs%40lists.ubuntu.com/msg6318318.html)
reports failures after updating from `6.8.0-142`, with L2TP session-reference
errors followed by panic or a hang. The reporter found the earlier kernel
stable in that environment.

[Related bug 2169269](https://bugs.launchpad.net/bugs/2169269) includes another
[report of the same regression](https://www.mail-archive.com/ubuntu-bugs%40lists.ubuntu.com/msg6318868.html)
on an L2TP/IPsec client using NAT traversal. Its traces identify session lookup
and release in `l2tp_core` after IPsec transport reinjection. These public reports
are on x86_64; a local ARM64 deployment exhibited the matching signature below.

A saved official Launchpad API snapshot retrieved on 2026-10-14 recorded bug
2169477 as **Confirmed**, importance **Undecided**, with no fix-release date.
A released fixed kernel has not been verified for this guide. Check the live bug
before selecting an updated kernel; do not treat the dated snapshot as current
release status.

## Observed signature and confidence

The ARM64 VM repeatedly lost SSH and guest-agent access while its virtual CPUs
remained busy. Its kernel log contained:

```text
refcount_t: addition on 0; use-after-free.
Workqueue: events xfrm_trans_reinject
l2tp_tunnel_get_session
l2tp_session_dec_refcount
```

The same boot also recorded refcount underflow/saturation and a later allocator
Oops. The affected release and L2TP stack match the Ubuntu regression closely.
The exact faulty backport and whether L2TP memory corruption caused that later
allocator fault were not independently proven locally. This is stronger evidence
than an unexplained generic VM hang, while retaining that distinction.

The public reports concern L2TP/PPP. They do not establish that the separate
IPsec/XAuth backend triggers this bug, nor that every 6.8 kernel is affected.

## Applied workaround and verification

The local workaround selected the already-installed `6.8.0-142-generic` kernel
persistently in GRUB and rebooted during maintenance. The boot image, initramfs
and matching GRUB entry were checked first. No kernel packages were removed or
held.

After booting, `uname -r` confirmed `6.8.0-142-generic`. VPN and application
health, authenticated private SSH and application HTTP checks passed. The
initial postboot kernel-log check contained no L2TP warnings or Oops. This was
initial acceptance, not a prolonged stability test or a universal endorsement
of that older kernel. Select a supported, security-maintained kernel verified
for your environment and track the Ubuntu fix.

## Ansible policy

The role warns for `6.8.0-146-generic` by default. To reject it under a consumer's
policy, set:

```yaml
vpn_sidecar_kernel_blocklist:
  - 6.8.0-146-generic
```

Use `vpn_sidecar_kernel_allowlist` to admit only exact releases tested by that
consumer. The default block and allow lists are empty. The warning is shared
host guidance; it does not claim an XAuth reproduction.

Installing missing modules makes the running kernel usable but does not fix
this regression. The role never changes the boot kernel or reboots the VM.
Keep kernel selection and reboot in an explicit VM maintenance procedure, then
rerun provisioning and verify authenticated destination access and kernel logs.
See the [deployment guide](deployment.md#running-kernel-requirements) and
[role reference](../ansible/README.md).
