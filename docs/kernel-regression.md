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
are on x86_64. They do not establish the cause of failures on other platforms.

A released fixed kernel has not been verified for this guide. Check the current
status of the linked Ubuntu bugs before selecting an updated kernel; this page
describes the regression as documented on 2026-10-15.

## Scope and diagnosis

The public reports concern L2TP/PPP. They do not establish that the separate
IPsec/XAuth backend triggers this bug, nor that every 6.8 kernel is affected.
Match the installed kernel and L2TP failure trace to the public reports before
attributing an unresponsive VM to this regression. A container health failure
alone does not identify a kernel defect.

## Kernel selection and verification

Select a supported, security-maintained kernel verified for your environment and
track the Ubuntu fix. The earlier kernel mentioned in the public reports is
historical evidence, not a general recommendation to downgrade.

Handle kernel selection and reboot as explicit VM maintenance. Verify the boot
image, initramfs and bootloader entry before rebooting. Afterwards, confirm the
running release with `uname -r`, check kernel logs for L2TP warnings or Oops, and
verify VPN, authenticated destination access and application behavior. Initial
success does not establish prolonged stability.

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
[backend prerequisites](../roles/vpn_sidecar/tasks/prerequisites.yaml).
