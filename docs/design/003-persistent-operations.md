# Persistent operations and lifecycle coordination

Issue #5 supplies a service-owned operation manager and a protected bbolt journal.
The [managed L2TP adapter](005-managed-l2tp.md) now runs in the production controller; XAuth remains unavailable. Synthetic
adapters exist only in Go tests and a separate acceptance image built from a Go
test binary. There is no production flag/environment variable/profile value that
activates the synthetic backend. Profile edits remain synchronous atomic
transactions; they do not return lifecycle operation IDs.

## Workstation and RPC interfaces

The CLI adds explicit lifecycle commands against an existing registered alias:

```sh
vpnmodo connection ensure example --json
vpnmodo connection disconnect example --wait --json
vpnmodo connection status example --json
vpnmodo operation status op-0123456789abcdef0123456789abcdef --host my-vm --json
vpnmodo operation watch op-0123456789abcdef0123456789abcdef --host my-vm --json
vpnmodo operation cancel op-0123456789abcdef0123456789abcdef --host my-vm --json
```

L2TP ensure/disconnect use the real adapter in the production image. XAuth remains
backend-unavailable. Tests can inject a separate synthetic adapter. `ensure`/`disconnect` return a queued operation snapshot; they
do not claim that a connection is ready. `--wait` subscribes until terminal state.
Watch output with `--json` is one JSON snapshot per line. Failed/cancelled watches
exit nonzero after printing the final snapshot. Closing SSH/the watcher does not
cancel the VM operation. Cancellation is an explicit separate request.

RPC methods are `connection.ensure`, `connection.disconnect`, `connection.status`,
`operation.status`, `operation.cancel`, `operation.subscribe` and
`operation.submit_mfa`. Profile/operation IDs are validated and negotiation is
required. Status includes desired intent, verified observation, generation,
revision, recovery permission and active/last operation IDs. A newly unmanaged
connection observation is unknown, never assumed disconnected/healthy merely
because memory has no worker.

Subscriptions register the snapshot and observer atomically. The response frame
is written before notifications start. Each subscriber keeps only its latest
snapshot; slow clients cannot block lifecycle work. Limits are 16 subscriptions
per bridge, 128 total, and 32 buffered notifications in the workstation client.
Revisions increase on committed changes; skipped revisions are allowed. Watches
also fetch snapshots periodically to detect broken SSH and recover dropped events.
Retained operations remain inspectable by ID after reconnect. Old pruned IDs
return not-found.

## Ownership, locking and intent

The profile-store transaction lock covers profile lookup and lifecycle submission.
Profile edits use that same lock and consult the manager before Docker inactivity
checks. Lock order is profile transaction, manager mutex, journal transaction.
Adapters never run under those locks. Desired-connected intent or pending/unknown
lifecycle work blocks settings changes, credential rotation and deletion, even
before a VPN container appears. An explicit successful disconnect allows edits.

One gate per profile serializes external adapter actions. Different profiles can
proceed independently. Concurrent ensure calls join an active ensure/recovery.
A later ensure against an established tunnel verifies it without a new login.
Private-endpoint verification failure alone never triggers a fresh login.

Disconnect commits desired-disconnected intent, disables recovery, cancels older
ensure/recovery and queues teardown behind its cleanup. Every worker update
checks both active operation ID and intent generation. A stale worker cannot mark
itself successful or restore connected intent after a newer disconnect. Repeated
pending disconnects join. Automatic recovery requires connected intent, explicit
recovery permission and adapter policy, and can never request MFA.

Cancellation stops an in-flight attempt and disables automatic recovery. It
preserves the desired intent and does not mean disconnecting an already
established session that was merely being verified. A newly started failed or
cancelled login gets bounded cleanup. Cancelling a disconnect retains disconnected
intent and attempts bounded teardown; unknown health is reported if teardown
cannot be verified. Use explicit disconnect to settle state before editing.

Deleting an inactive profile drops its derived connection metadata under the
profile transaction before removing its files. A filesystem failure at that point
leaves an inactive valid profile for inspection. History remains bounded and may
retain non-secret terminal records after deletion.

## Journal and failure boundaries

`controller-state/operations.db` is a 0600 file owned by the controller VM account,
in its existing 0700 state directory. Pinned `go.etcd.io/bbolt v1.5.0` provides
single-writer transactions and sync-backed commits. The store rejects symlinks,
foreign/public files, unknown state shapes and corrupt/truncated journals. It does
not silently reset lost intent. The journal contains only typed operation/state
metadata: IDs, profile names/revisions, desired/observed state, challenge identity
and expiry, timestamps and closed failure classifications. It contains no profile
configuration, credential references/values, MFA values, wire params or raw adapter
errors. Credentials remain in the separate profile vault.

Operation and connection changes commit together before a worker starts or a
successful response/update is published. Journal failure marks the manager
unavailable, cancels workers, refuses further mutations and makes readiness false.
It does not publish success from uncommitted memory. Newly created attempts get
best-effort bounded teardown. Existing established sessions are not torn down just
because a status/verification operation loses persistence. Restart reopens valid
durable state and reconciles; persistent corruption requires explicit repair.

History retains up to 512 snapshots, pinning active/last records and subscriptions.
There are at most 128 connection records. The encoded journal state is at most
1 MiB and the database file has a 32 MiB ceiling. No-sync mode and raw logging are
not enabled. Physical database pages/backups can retain old non-secret metadata;
this is not a secret store or a secure-erasure mechanism.

Default operation timeout is three minutes, MFA challenge timeout 90 seconds,
and cleanup timeout five seconds. Cleanup can finish after the operation deadline.
Adapters must honor cancellation, bound subprocesses, make disconnect idempotent,
return only classified outcomes and keep `CanRecover` a pure policy check. Backend
packet protection and protocol-specific deadlines remain backend-stage work.

## MFA and restart rules

Only an explicit ensure may issue one challenge. Submission validates operation,
challenge, expiry and numeric code shape, then commits consumption before handing
the value to the worker's ephemeral bounded channel. Replay/second prompts are
rejected. MFA values never enter the journal, argv, environment, results or logs.
Expiry/authentication failure ends the attempt. Workstation connection-time MFA
prompt orchestration is completed with XAuth integration; this stage tests the
versioned submit API directly.

On restart, nonterminal operations become failed/interrupted and lose their
challenge. Saved connected intent is observed and verified without starting a
login. Previously established sessions are preserved where verified; recovery is
enabled only after verification and only if previously permitted. Missing tunnels
and interrupted MFA do not cause automatic login. Saved disconnected intent is
enforced with an idempotent disconnect operation. No saved success record alone
proves a tunnel is healthy.

## Recovery extension

The [recovery policy](004-recovery-policy.md) adds bounded background monitoring,
closed health classifications and persisted retry/cooldown metadata. It runs only
for adapters implementing the recovery interface. The [L2TP adapter](005-managed-l2tp.md) supplies real backend activation and
Docker ownership enforcement.

## Validation

Race tests cover concurrent joins, independent profiles, verified-session reuse,
disconnect superseding old work, cancellation without session teardown, MFA replay,
expiry/repeated prompts, deadlines, bounded observers/history, restart, and journal
failures before/after publication and after a new connection starts. RPC tests
cover reconnect, ordered snapshots/notifications and disabled production backends.

The isolated Ubuntu SSH workflow first validates the production controller, then
builds a separate synthetic test-binary image in a binary-only Docker context. It
verifies operations after the initiating SSH exits, concurrent callers, CLI watch,
MFA submission/replay, cancellation, controller restart, established-session reuse,
interrupted MFA without retry, persisted disconnect intent and secret-free journal
bytes. It never logs into a VPN or changes a customer deployment. Synthetic success
does not establish VPN interoperability or XAuth packet isolation.

Reference: [bbolt v1.5.0](https://pkg.go.dev/go.etcd.io/bbolt@v1.5.0).
