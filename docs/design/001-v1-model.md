# Profiles, protocol and operation reference

This page describes the managed profile format and controller API. For routine
use, follow [installation](../../README.md#installation),
[profile maintenance](../onboarding.md) and [service access](007-service-access.md).
For the SSH and container traffic path, see [VPN internals](../internals.md).

## Supported environment

The supported installation uses an Apple Silicon Mac and one Ubuntu 24.04 arm64
Lima VM created by `vpnmodo setup`. The workstation runs `vpnmodo`; the VM runs
`vpnmodo-controller` in a container. Prebuilt CLI installation requires neither
Go nor Docker Desktop on the Mac.

Profiles support L2TP/IPsec, IPsec/XAuth with emailed-code MFA, and
[IKEv2 with EAP-MSCHAPv2](../ikev2.md). See
[supported platforms and limitations](../supported-platforms.md).

## Profile ownership and schema

The VM stores managed VPN settings, revisions and credentials. The workstation
stores profile references, destination SSH settings and service-forward mappings.
Credentials are not copied back by inspection or registration.

Profiles use `schema_version: 1`, a lowercase hyphenated name of 1–48 characters,
a `backend` discriminated by `type`, and a TCP `health` endpoint. Examples are in
[`examples/profiles/v1`](../../examples/profiles/v1/); replace their documentation
addresses before use.

| Backend | Gateway | DNS and health |
| --- | --- | --- |
| `l2tp-ipsec` | IPv4 address or ASCII DNS hostname | Optional IPv4 VPN DNS servers; a stable TCP endpoint checks readiness |
| `ipsec-xauth` | IPv4 address | A hostname health target requires explicit IPv4 VPN DNS servers; otherwise use an IPv4 health target |
| `ipsec-ikev2` | IPv4 address or ASCII DNS hostname | IPv4 virtual IP and explicit VPN DNS; hostname health checks require configured DNS |

All backends accept IKE/ESP proposal lists. An absent or empty list selects the
backend defaults. L2TP `force_encapsulation` defaults to true. Proposal syntax is
validated before connection; successful negotiation still depends on the gateway.

Unknown or duplicate keys, wrong key case, null values, mixed-backend attributes
and unsupported schema versions are rejected. Input is UTF-8, at most 64 KiB,
with nesting depth at most 32. Configuration errors omit raw input values.

The CLI input file includes `credentials.username` and `credentials.password`,
plus `credentials.psk` for L2TP/XAuth or `credentials.ca_cert` for IKEv2.
Username, password and PSK values are nonempty, at most 4096 UTF-8 bytes, and
cannot contain NUL, CR or LF. IKEv2 requires a validated PEM CA bundle; see the
[trust requirements](../ikev2.md#install-and-import). The CLI reads the input file without credential prompts
and sends credentials separately from profile settings over SSH. XAuth MFA is a
separate connection-time step. See [credential storage](002-profile-storage.md)
for permissions, rotation and deletion behavior.

The vault is protected by filesystem permissions, not application encryption.
VM administrators can access it, and deletion does not erase historical disk or
backup copies. Go does not guarantee erasure of credential values from memory.

## Managed identity and adoption

A managed profile named `example` uses Compose project `vpnmodo-example` and
these ownership labels:

```text
io.vpnmodo.managed-by=vpnmodo
io.vpnmodo.profile=example
```

Container selection also verifies deployment records and backend/revision/image
identity. A matching name alone is insufficient. Foreign or conflicting resources
are refused rather than adopted automatically.

## RPC framing, negotiation and method surface

The CLI invokes a non-PTY SSH bridge inside the controller container. The bridge
uses a private Unix socket; there is no controller API TCP listener. A bridge
connection ending does not end a VM-owned operation.

The controller uses [JSON-RPC 2.0](https://www.jsonrpc.org/specification) with one
compact UTF-8 object per LF-terminated frame. Frames are at most 1 MiB excluding
the newline, with nesting depth at most 32. Batches are unsupported. Request IDs
are strings of 1–128 encoded bytes or signed int64 values; null and fractional
request IDs are rejected. Parameters are named objects. stdout carries protocol
frames and stderr carries sanitized diagnostics.

Each connection first negotiates with `system.hello`. Application API version
`{major:1,minor:0}` is separate from JSON-RPC's `"2.0"`. Major versions must match;
the client's minor version cannot exceed the server's. Backend capabilities add
further requirements. Failed negotiation prevents state mutations and credential
provisioning.

| Methods | Purpose |
| --- | --- |
| `system.hello` | Negotiate API version and discover controller version/backends |
| `system.status` | Check controller storage, Docker readiness and runtime availability |
| `backend.list`, `backend.describe` | Inspect backend configuration and capabilities |
| `profile.create` | Create a typed profile with separate credential input |
| `profile.list`, `profile.inspect` | Return non-secret settings, revision and credential availability |
| `profile.update`, `profile.rotate_credentials`, `profile.delete` | Change an inactive profile using an expected revision |
| `connection.status` | Read desired/observed state and active/last operation IDs |
| `connection.ensure`, `connection.disconnect` | Start or join a lifecycle operation |
| `operation.status`, `operation.cancel` | Inspect or cancel an operation |
| `operation.subscribe` | Receive a snapshot and subsequent revisioned notifications |
| `operation.submit_mfa` | Submit one code for the matching operation and challenge |
| `connection.application` | Resolve the currently verified application container for proxied traffic |
| `system.doctor` | Run profile-scoped runtime and prerequisite checks |

`operation.changed` is a server notification without a request ID. Request IDs
correlate individual RPC replies; operation and challenge IDs identify longer-lived
work. Errors use fixed classifications and omit raw backend stderr or input values.

## Operations, concurrency and restart policy

Operation states are `queued`, `running`, `awaiting_mfa`, `verifying`, `succeeded`,
`failed` and `cancelled`. Connection and recovery operations verify readiness
before success. Terminal operations cannot restart; a retry creates a new ID.

- Operation IDs use `op-` plus 32 lowercase hex digits. MFA challenge IDs use
  `mfa-` plus 32 hex digits. They identify work; they are not credentials.
- Only an explicit connection attempt requests MFA. A challenge accepts one
  matching, unexpired code. The current adapter accepts 4–10 ASCII digits.
  Expiry, authentication rejection and repeated prompts end the attempt.
- Concurrent ensure calls join the existing attempt or reuse a verified session.
  Different profiles can operate independently.
- Losing SSH or closing a watcher does not cancel an operation. Cancellation and
  disconnect are separate commands; use disconnect to end an established session.
- An intentional disconnect prevents automatic recovery. A failed private TCP
  check alone does not trigger new authentication.
- On controller restart, interrupted operations fail and their challenges expire.
  Established sessions are checked before reuse. Missing tunnels and interrupted
  MFA do not silently start another login.
- Notifications carry increasing revisions and can skip intermediate updates.
  Reconnecting clients fetch a current snapshot.

See [operation commands](003-persistent-operations.md) and
[recovery policy](004-recovery-policy.md) for timeout, cooldown and retry details.

## Fail-closed enforcement

XAuth packet enforcement is installed in the VPN network namespace before
application attachment and operates independently of controller polling. Losing
IPsec state blocks application traffic, including existing flows. DNS and IPv6
paths are also constrained. There is no profile switch to disable this behavior.
See [packet enforcement](006-managed-xauth.md#packet-enforcement) for the boundary,
bootstrap exceptions and test coverage.

The VM user, root and Docker administrators remain trusted: Docker socket access
provides administrative authority over containers and their network namespaces.
This isolation is not a security boundary against those administrators.
