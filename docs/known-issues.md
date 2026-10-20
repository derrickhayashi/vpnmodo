# Known issues and regressions

Use `vpnmodo status PROFILE` and `vpnmodo doctor PROFILE` to distinguish VM,
controller, tunnel and destination-service failures. A failed destination check
does not by itself mean VPN authentication failed. Replace `work` below with your
profile name.

## XAuth delayed MFA on older runtime images

**Symptom:** login reaches the MFA prompt, then exits with diagnostic 27 before
the tunnel is established when code entry takes more than about 30 seconds.
Diagnostic 27 identifies the failed stage; it does not prove the code was wrong.

The rc.7 managed XAuth image used a 30-second client handshake timeout, shorter
than the controller's 90-second MFA window. The rc.12 release pins a corrected
XAuth image with a 300-second handshake timeout. The controller's MFA deadline
remains 90 seconds. A synthetic regression reproduces the failure with a
40-second delay and passes with only that timeout corrected.

Use the [rc.12 release](releases.md#single-file-cli-download) and its corrected
XAuth image. Replacing only the workstation binary does not upgrade an existing
VM runtime automatically; see [installation and upgrades](releases.md#installation-and-upgrades).
If a login is still in progress, inspect its operation before starting another:

```sh
vpnmodo status work
vpnmodo operation status op-0123456789abcdef0123456789abcdef --json
```

Replace the example operation ID with the one returned by your connection
attempt. If the attempt failed, start a new explicit `vpnmodo up work` when ready
and use the new code for that attempt. Do not reuse an old code. Other causes of
post-MFA exits remain under investigation in
[XAuth session observations](#xauth-session-state-loss).

## XAuth session state loss

**Symptom:** an established connection loses its kernel IPsec state while the
client process still appears to be running. The controller reports a disconnected
or authentication-required state and removes the application container.

The cause remains unconfirmed. The rc.12 delayed-MFA correction does not establish
that it fixes this separate session-lifetime problem. Kernel enforcement continues
to block unprotected application traffic; the controller does not silently retry
XAuth authentication.

Check the current state:

```sh
vpnmodo status work
vpnmodo doctor work
```

To start a new login after confirming that the previous operation has ended, run
`vpnmodo up work` and supply the fresh MFA code. Report recurring failures with the
CLI version and sanitized status/doctor results, omitting credentials, MFA codes
and private destination details. Follow
[XAuth session observations](#xauth-session-state-loss) for progress.

## Intermittent L2TP startup exit in the test lab

A cold isolated-lab run observed an L2TP container exit with status 2 during manual
replacement while the controller was paused. Later full runs passed without
changing startup, recovery or stop timeouts. The original cause is unconfirmed;
see [the release validation record](release-validation.md#known-limitations).

This observation does not establish a general L2TP deployment failure. When
investigating a repeated startup failure, first distinguish a container startup
error from VM/kernel prerequisites and an unreachable health endpoint with
`vpnmodo status PROFILE` and `vpnmodo doctor PROFILE`. The
[isolated lab guide](../tests/local-lab/README.md) describes the targeted service
test and protected startup diagnostics for reproducing the lab failure.

## Ubuntu 6.8.0-146 L2TP kernel regression

L2TP/IPsec operation on Ubuntu's `6.8.0-146-generic` kernel has public reports of
crashes and soft lockups. VPN containers share the VM kernel, so replacing a
container or restarting the watchdog cannot repair a kernel fault.

Check the VM's running kernel with `uname -r` and follow the
[kernel regression guide](kernel-regression.md) for the dated bug references,
configuration options and maintenance checks. That guide has not verified a
released fixed kernel. The public L2TP reports do not establish that XAuth or all
6.8 kernels are affected.

## Resolved packet and shutdown regressions

- **XAuth PMTU feedback:** the older managed filter dropped related IPv4
  fragmentation-needed errors. Transfers over smaller routed links could stall.
  The correction permits only conntrack-related fragmentation-needed feedback;
  unrelated ICMP and plaintext traffic remain blocked. The regression transfers
  256 KiB each way across a 1280-byte path without a route MTU or MSS override.
- **L2TP shutdown:** a foreground idle sleep delayed the TERM handler beyond
  Docker's two-second stop limit, leaving the peer session open. The idle wait is
  now interruptible. The regression checks clean termination, peer cleanup and
  three fresh reconnects within the existing limits.

See [release validation](release-validation.md) for the tested source revisions,
platforms and protocol coverage. Successful short tests do not establish
long-term session stability or compatibility with every gateway and MFA method.
