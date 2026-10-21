# VPN connection history and diagnostics

The managed controller records lifecycle and health changes for every supported
VPN backend: L2TP/IPsec, IPsec/XAuth and IKEv2. Logging starts with the updated controller;
backend diagnostic events require updated backend images and a new explicit
connection. Existing sessions are not automatically replaced to enable capture.

```sh
vpnmodo logs work --since 24h
vpnmodo logs work --since 336h --json
vpnmodo logs work --since 2026-10-20T00:00:00Z --limit 100 --json
```

`work` is the saved local profile alias. Requests use the existing authenticated
SSH/JSON-RPC connection. Reading logs does not connect a VPN, probe a destination,
initiate MFA or restart containers. The command reads the backend's available
sanitized buffer before returning history.

Results are newest ingestion first, with UTC event timestamps and a sequence
number. A delayed backend event keeps its original timestamp, so ingestion order
can differ from event-time order. The default window is 24 hours and the default
page size is 200; the maximum is 500. `--since` accepts a positive Go duration up
to 336 hours or an RFC3339 timestamp. JSON output is an object containing `events`
and, when more records exist, `next_before`. Use that value with `--before` and
the same `--since` timestamp to retrieve the next page. Human output prints JSON
lines followed by the next-page command when needed.

## Recorded information

| Event | Information |
| --- | --- |
| Operation | Queued/running/MFA-wait/verifying/success/failure/cancellation, operation ID and closed failure classification |
| Connection | Desired and observed state changes, including intentional disconnect |
| Health | Tunnel loss, endpoint unavailability, forwarding detachment and healthy recovery; unchanged checks produce no new event |
| Backend diagnostic | Allowlisted indications of peer DELETE, DPD/retransmission exhaustion, rekey/authentication failure, establishment, client exit, PPP loss or support-process exit |
| Container exit | Exit code and out-of-memory classification when observed before removal |

The backend never writes raw daemon output to disk or Docker logs. L2TP processes
feed an in-memory pipe into a fixed-code classifier; the managed XAuth reader
classifies lines before discarding them. Stored events contain no PSKs, passwords,
MFA values, usernames, private endpoint addresses or authentication transcripts.
The profile alias is retained. Existing connection records from before the upgrade
can lack backend metadata until the next explicit operation.

A `peer_delete` event establishes that a DELETE was received, not why the gateway
sent it. `dpd_timeout` includes retransmission exhaustion; it cannot distinguish a
network outage from an unresponsive peer. A client exit or missing kernel state
alone does not establish a root cause. Unrecognized daemon messages are discarded.
Gateway-side logs may still be needed. Historical drops before this upgrade
cannot be reconstructed from the new event store.

## Storage, bounds and failures

The controller writes `controller-state/logs/events.jsonl` in its existing private
VM data root. Rotated files use suffixes `.1` through `.4`: at most five files of
10 MiB each, shared across profiles. Directories are mode 0700 and files mode 0600,
owned by the VM user. Logs survive controller replacement, backend removal and
profile deletion while retained. Unsafe permissions, foreign ownership, symlinks
and malformed complete records are rejected. A crash-truncated final JSONL record
is discarded before further appends; completed records remain available.

Only the VPN container mounts its profile's diagnostic buffer at
`/var/log/vpnmodo`. The controller pre-creates the VM-user-owned files, and the
root backend preserves their ownership. Two slots bound the buffer to less than
128 KiB per deployment even while the controller is down. A shared file lock
serializes collection and rotation. Successful durable collection consumes buffer
records; retry after a partial collection is deduplicated. Buffer overflow can
lose the oldest diagnostics; this is bounded troubleshooting history, not a
complete audit trail.

Queries exclude events older than 14 days. Expired records are physically purged
at startup, on reads, and by hourly maintenance while the controller runs.
Maintenance also drains inactive-profile buffers. While the controller is stopped,
bounded files remain until its next startup. Size rotation can discard records
sooner than 14 days. JSONL history and the operation journal are separate; their
commits are not atomic, and a crash between them can leave a history gap. Lifecycle
operations commit their journal state before publishing an event.

`system.status` and the controller section of `doctor` expose `logging_ready`.
A logging failure sets it false until controller restart. Log retrieval returns a
fixed persistence error when the history is unreadable. The controller preserves
failed log files and keeps lifecycle/teardown available; logging errors do not
undo a committed operation or prevent safe disconnect. After correcting storage,
restart the controller to restore reporting. Replacing only the workstation CLI
does not upgrade the controller or VPN images.
