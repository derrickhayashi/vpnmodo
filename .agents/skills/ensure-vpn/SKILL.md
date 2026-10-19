---
name: ensure-vpn
description: Connect or recover an existing L2TP/IPsec or IPsec/XAuth VPN sidecar using a consumer-owned profile, handle interactive emailed MFA when required, and verify the consumer's private access targets.
---

# Ensure VPN sidecar access

Use the shared repository's `scripts/vpn` launcher. Its root is three directories
above this skill directory. Read the consumer project's instructions and VPN
context first; [profile configuration](../../../docs/profiles.md) describes the
launcher contract. Customer settings and runtime state belong outside this
shared checkout.

## Resolve the consumer context

Obtain these from the consumer's instructions, VPN README, or thin adapter:

- Consumer project root, profile directory and selected profile name.
- An optional consumer wrapper command; otherwise use the shared launcher.
- Verification targets, expected results and any retained deployment that must
  stay stopped. Authenticated SSH aliases are one supported verification method.

Read the selected `profile.env` as literal data, never source it as shell code.
`VPN_BACKEND` selects `l2tp-ipsec` or `ipsec-xauth`. Remote execution comes from
`VPN_SSH_HOST`, `VPN_REMOTE_ROOT` and `VPN_REMOTE_PROFILES_DIR`. Do not assume a
particular workstation OS, VM alias, remote username or installation path.
Resolve an ambiguous profile before connecting; do not guess credentials or
verification addresses.

For example, using absolute paths resolved from that context:

```sh
bash /path/to/vpn-sidecar/scripts/vpn --profiles-dir /path/to/consumer/vpn/profiles example validate
bash /path/to/vpn-sidecar/scripts/vpn --profiles-dir /path/to/consumer/vpn/profiles example status
```

`validate` checks local settings. `status` checks the selected deployment and
private endpoint. When using a consumer wrapper, follow its documented syntax.
A missing runtime manifest or image indicates an unprovisioned deployment;
report that prerequisite rather than treating it as an authentication failure.
Routine connection recovery does not rebuild images, copy credentials, rewrite
SSH aliases or migrate deployments. Explicit deployment/cutover requests are a
separate scope; use the repository and consumer deployment guides for those.

## Reuse or connect

If status is connected and the endpoint is reachable, perform the consumer's
verification checks and reuse that session. A routine ensure request does not
require a new login or restarting a healthy tunnel. Preserve unrelated profiles
and any consumer-defined inactive installation.

If connection is needed, use the same wrapper or launcher with `connect` in a
persistent interactive terminal (`tty: true`), with a short initial yield.
The launcher applies remote profile settings automatically; `--local` is for
execution on the Linux Docker host itself. Use the launcher rather than
starting Compose services directly: it owns lifecycle locking, manifest
rendering and failed-attempt cleanup.

Distinguish inability to reach the Docker host from VPN authentication failure.
A sandbox network denial is not evidence of a broken VPN. Preserve existing
SSH authentication and host-key settings while diagnosing transport failures.

### L2TP/IPsec

This backend uses its provisioned PSK and PPP credentials without an emailed
code. Do not request MFA because another supported backend uses it.

For an already-running unhealthy VPN, inspect the selected watchdog before
starting another operation. If it is recovering or awaiting its configured
cooldown, observe one recovery attempt, then recheck status and the consumer
targets. Bound the observation by the configured cooldown plus its recovery
window (up to 120 seconds), or a shorter user deadline. Keep the user informed;
if that wait cannot be completed, report recovery pending rather than success.
Do not change the cooldown, start a second watchdog, or repeatedly invoke
`connect` to hurry it. If that attempt fails, use bounded VPN/watchdog logs to
diagnose and report the failure instead of cycling indefinitely.

`connect` starts stopped services and waits for health; it does not itself
force-recreate an already-running unhealthy container. Do not promise that
command alone guarantees recovery. For a stopped deployment, connect normally,
poll for completion and inspect bounded diagnostics if startup fails.

### IPsec/XAuth emailed MFA

Wait until the terminal shows `Enter emailed code:`. Ask for the newest code
for this attempt, using `request_user_input_async` when available. The prompt
allows two minutes. If the user already supplied the fresh code for this
attempt, submit it without asking again, after the hidden prompt appears.

Send the numeric code plus newline through the terminal's stdin tool. Do not
echo it, pass it in shell arguments/environment, write it in notes or source,
or reuse a previous attempt's code. The controller uses a private temporary
file that the harness deletes after submission.

Wait for the controller to finish. Cancellation, timeout or failed login must
be reported; let the controller clean up. Do not automatically repeat login
attempts or generate more emails. A further attempt needs a fresh code.

## Verify the result

For a new connection attempt, require a zero controller exit status and the
application connectivity result. For a reused session, require successful
status/private-endpoint checks instead. Then run the consumer-defined checks. For SSH, use the supplied aliases and
compare `hostname` to the supplied expected result. Bound probes, for example
with a 10-second connect timeout and server-alive limits or a 30-second overall
deadline. Distinguish SSH authentication failures from transport failures.
Do not invent destinations or relax host-key checking to make a probe pass.

A running container or installed IPsec policy alone does not establish usable
private access. If no authenticated target check is configured, report the
TCP/application checks actually performed; do not imply SSH authentication was
verified. If a target fails, report it separately and diagnose within the
requested recovery scope. Do not restart a reachable VPN merely because a
destination rejects SSH credentials.

## Watchdog and diagnostics

Inspect service health and bounded logs for the selected runtime manifest:

```sh
sudo -n docker compose -f /path/to/consumer/vpn/instances/example/compose.yaml ps
sudo -n docker compose -f /path/to/consumer/vpn/instances/example/compose.yaml logs --tail 20 watchdog
```

The shared watchdog uses `auto` mode for L2TP and `manual` mode for XAuth.
Automatic recovery recreates an unhealthy VPN and reattaches its application,
with a cooldown. Manual mode reports failed health/private TCP probes without
restarting XAuth siblings or triggering MFA. The [watchdog guide](../../../watchdog/README.md)
describes its state, locking and maintenance behavior. Do not start a second
watchdog or change its mode as an authentication workaround.

For XAuth, sanitized controller/harness logs are `run/cli.log` and
`run/test.log` in the consumer runtime. The source harness is
`containers/ipsec-xauth/connect.exp`; it disables the startup timeout after
CHILD_SA establishment. Compare deployed source only when that specific
quiet-tunnel timeout recurs. L2TP diagnostics use bounded VPN service logs and
its [backend guide](../../../containers/l2tp-ipsec/README.md).

Do not dump credentials, MFA files, full environment/inspection output or
resolved Compose settings. Never dump `ip xfrm state`, which can include
cryptographic keys. Credential files stay on the Docker host.

Report whether a session was reused or newly connected, the verification
results per target, and any watchdog failure. Keep long-term stability and
rekey claims separate from a short functional check. Disconnect only when
requested or required by an explicitly authorized cutover; failed-attempt
cleanup remains the controller's responsibility.
