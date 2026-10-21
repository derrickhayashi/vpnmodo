# Troubleshooting known issues

Use `vpnmodo status PROFILE`, `vpnmodo doctor PROFILE` and
`vpnmodo logs PROFILE --since 24h` to distinguish VM, controller, tunnel and
private-service failures. A failed destination check alone does not mean VPN
authentication failed. Replace `work` below with your profile alias.

## XAuth delayed MFA on older runtime images

**Symptom:** login reaches the MFA prompt, then exits with diagnostic 27 before
the tunnel is established when code entry takes more than about 30 seconds.
Diagnostic 27 identifies the failed stage; it does not prove the code was wrong.

Runtime images predating rc.12 can have a 30-second client handshake timeout,
shorter than the controller's 90-second MFA window. Updated XAuth images allow
300 seconds for the handshake while retaining the controller's MFA deadline.
Use the current [release and runtime upgrade](upgrading.md); replacing only the
workstation executable does not update the VM's backend images.

Inspect the current operation before starting another login:

```sh
vpnmodo status work
vpnmodo operation status op-0123456789abcdef0123456789abcdef --json
```

Replace the operation ID with the one returned by your attempt. After a failed
operation, use `vpnmodo up work` and the new code for that attempt. Do not reuse
an old MFA code. Other post-MFA exits can have different causes.

## XAuth session state loss

**Symptom:** an established connection loses kernel IPsec state while the client
process still appears to be running. The controller reports a disconnected or
authentication-required state and removes the vpntools container.

The cause remains unconfirmed. Updating the delayed-MFA handshake timeout does
not establish a fix for this separate session-lifetime problem. Kernel filtering
blocks unprotected application traffic; the controller does not silently retry
XAuth authentication.

Check `vpnmodo status work`, `vpnmodo doctor work` and recent logs. After the
previous operation ends, use `vpnmodo up work` and a fresh code to reconnect.
Report recurring failures with the CLI/runtime versions and sanitized diagnostic
codes. Omit credentials, codes and private endpoint details.

## L2TP startup failure

Use status, doctor and logs to distinguish missing VM/kernel prerequisites,
container startup errors, authentication failures and an unreachable health
endpoint. A private-service outage alone does not require another VPN login.
Do not increase recovery budgets or loop connection commands to hide a recurring
startup failure. Report the fixed diagnostic codes and software versions.

## Ubuntu 6.8.0-146 L2TP kernel regression

L2TP/IPsec operation on Ubuntu's `6.8.0-146-generic` kernel has public reports of
crashes and soft lockups. Containers share the VM kernel; replacing a container
or restarting the controller cannot repair a kernel fault.

Check the VM's running kernel with `uname -r` and see the
[kernel regression guide](kernel-regression.md) for the reported symptoms and
maintenance checks. These L2TP reports do not establish that XAuth or all 6.8
kernels are affected.

## Stalled transfers and incomplete shutdown

Older XAuth filters could drop related IPv4 fragmentation-needed feedback,
causing transfers to stall over smaller routed links. Current filters permit
only conntrack-related fragmentation-needed feedback while blocking unrelated
ICMP and plaintext traffic. Update the backend through the normal upgrade
workflow before considering route MTU or MSS overrides.

Older L2TP idle loops could delay shutdown beyond Docker's stop budget, leaving
the peer session open. Current idle waits are interruptible. If the peer still
reports an existing login after disconnect, inspect backend versions and logs
before reconnecting repeatedly.
