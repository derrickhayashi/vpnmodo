# Controller recovery policy

The controller monitors managed profiles and applies backend-specific recovery.
[L2TP](005-managed-l2tp.md) can reconnect automatically within a bounded retry
budget. [XAuth](006-managed-xauth.md) can repair a detached application container,
but tunnel loss requires a new explicit login and fresh MFA.

## Health and recovery decisions

Backends opt into automatic monitoring through the internal `RecoveryAdapter` Go
interface. Its read-only `Check` returns a closed health classification; raw
backend output never becomes journal data. `CanRecover` remains the backend's
unattended-authentication policy. Connection status gains optional `recovery`
metadata: health, check time, consecutive attempt count and `not_before` timestamp.
Older journal records without that field still load.

| Health | Controller action |
| --- | --- |
| Healthy | Update observation; preserve the session. |
| Starting | Allow startup to settle; no new login. |
| Private service unavailable | Report separately; no new login. |
| Probe unavailable or invalid result | Report unavailable; no destructive inference. |
| Stopped | Disable background recovery; require explicit ensure. |
| Recovery owner conflict | Disable background recovery; refuse takeover. |
| Tunnel down | Queue recovery after cooldown and budget checks. |
| Application namespace detached | Queue application repair using the existing tunnel. |

Monitoring starts after startup reconciliation. It probes every 30 seconds with a
10-second deadline, at most four probes concurrently and one probe per profile.
Each probe uses the existing profile gate. It releases the gate before submitting
an operation and supplies the observed intent generation, so a newer ensure or
disconnect invalidates the decision. Active operations take priority over probes.
Probe failures do not cause reconnects.

## Bounded operations

Recovery and namespace repair use journaled `connection.recover` operations,
sharing the same gate, cancellation and stale-worker checks as explicit requests.
There is no separate public recover command. The attempt counter and next
permitted attempt time commit before any external mutation. Defaults permit three
consecutive attempts, with delays of five, ten and twenty minutes, capped at
30 minutes. An initial successful ensure and each successful repair also start a
five-minute cooldown. Healthy observations reset the failure count while retaining
the cooldown; a successful operation resets it and starts a new cooldown. Exhaustion,
authentication failure, an unexpected MFA request, cancellation and deliberate
stops require explicit user action to rearm. An explicit ensure resets the budget
and may override cooldown. XAuth cannot opt into unattended authentication merely
by implementing health probes.

For tunnel loss, the worker rechecks health after acquiring the gate, stops the
application, tears down the failed tunnel, connects, verifies the private endpoint
and repairs application attachment. A namespace-only failure stops/recreates the
application without a new login. A healthy recheck preserves the tunnel. Automatic
work cannot issue MFA challenges. Repair must recheck tunnel/service health and
ownership before attachment and leave forwarding stopped on failure. New failed
login attempts receive bounded teardown; failed namespace repair preserves the
tunnel. Cleanup remains inside the profile gate. Failed quiescence prevents VPN
replacement. Adapter cleanup failures are reported as unknown observations;
controller policy alone cannot guarantee network protection after a Docker error.

Journal failure prevents new side effects and makes the controller unavailable.
An intentional disconnect records intent before superseding recovery. Cancelling
recovery keeps connected intent but disables further automatic attempts. Restart
retains budget and cooldown, interrupts pending operations, and performs the
existing read-only reconciliation: a missing tunnel never causes a fresh login.
Only a previously permitted, still-established, verified session can regain
recovery permission. Exhausted budgets and interrupted authentication remain
blocked until explicit ensure.

## Recovery ownership boundary

Every adapter action verifies managed project/profile labels and rejects competing
recovery owners. Creating a managed profile does not adopt legacy containers.

The current standalone watchdog checks `io.vpnmodo.managed-by` on its selected VPN
before any health-driven recovery or namespace repair. The value `vpnmodo` causes
a stable controller-owned report and no mutations. Older installed watchdog images
do not acquire this guard automatically. Before switching an existing deployment
to controller management, stop its old recovery owner to avoid competing
reconnects. Existing standalone behavior remains available.

## Validation and scope

Go race tests cover classification, healthy reuse, cooldown, retry exhaustion,
application-only repair, ordering, cancellation/disconnect races, bounded probing,
restart preservation and journal failures before/after publication. Watchdog
regressions execute the actual shell with synthetic Docker results.

The isolated Ubuntu SSH workflow exercises the persistent scheduler through CLI
commands and recreates the controller between attempts. Its separate test binary
uses private synthetic runtime files, with short test-only intervals, to assert
bounded retries, application repair without reauthentication, stopped forwarding,
intent preservation, persisted cooldown/budget and restart without login. This is
controller-policy evidence. It does not establish L2TP interoperability or XAuth
packet isolation, and is supplemented by the real L2TP interoperability and recovery lab described
in the adapter design.
