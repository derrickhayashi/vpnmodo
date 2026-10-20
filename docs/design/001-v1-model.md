# V1 profiles, protocol and operation model

Status: model baseline from issue #2, controller/SSH from issue #3, and secure
profile management from issue #4, and persistent operation coordination from
issue #5. Managed L2TP and XAuth are described in the
[runtime](005-managed-l2tp.md) and [MFA/isolation](006-managed-xauth.md) guides.
Workstation [service access](007-service-access.md) adds named forwards and
live diagnostics. Private [release packaging](../releases.md) supplies candidate artifacts;
adoption and platform acceptance remain the final release gate. Storage and credential
input details are in the
[profile storage guide](002-profile-storage.md).

## Package ownership

| Package | Responsibility |
| --- | --- |
| `internal/profile` | Backend-specific settings, credential validation and managed deployment identities |
| `internal/protocol` | JSON-RPC requests/responses, wire operation snapshots, MFA input and framing |
| `internal/operation` | Lifecycle states, domain records and operation/challenge identities |
| `internal/lifecycle` | Service-owned workers, intent, cancellation, MFA and reconciliation |
| `internal/operationstore` | Protected transactional journal and bounded metadata |
| `internal/secret` | Shared redacted values with explicit transport encoding |
| `internal/jsonstrict` | Bounded, exact-shape JSON parsing |
| `internal/rpcio` | Bounded jrpc2 channels and negotiated client |
| `internal/controller` | Private socket service, profile administration, discovery/status and bridge |
| `internal/host` | Workstation host/profile references, verified SSH and explicit bootstrap |
| `internal/profilestore` | VM profile/vault transactions and recovery |
| `internal/privatefile` | Private atomic files, ownership checks and locks |
| `internal/credentialinput` | Hidden terminal and protected descriptor input |

`protocol` depends on the domain packages. `profile` and `operation` do not depend
on RPC types; in particular, RPC errors belong to wire snapshots in `protocol`.
`secret` and `jsonstrict` are focused shared helpers. There is no catch-all
`contract` package. JSON field names, method names and validation behavior stay
unchanged by this package split.

## Scope and build targets

- CLI/product: `vpnmodo`. Controller executable, image and service:
  `vpnmodo-controller`. Keep the existing `scripts/vpn` command operational.
- V1 backend scope: L2TP/IPsec and IPsec/XAuth. Tailscale retains its existing
  standalone workflow; the new API returns unsupported-backend for it in v1.
- Initial workstation targets: macOS arm64/amd64. Controller/image targets:
  Linux arm64/amd64 on Ubuntu 24.04 LTS with rootful Docker Engine and Compose v2.
  VM creation, Windows and Linux workstation support are outside initial scope.
  These are release targets, not a claim that runtime acceptance has passed.
- Build with Go 1.27.1; hosted Actions remains disabled. There is no Go runtime requirement for
  teammates installing eventual compiled binaries. The controller/SSH stage
  adds runnable executables; see the [controller guide](../../containers/controller/README.md).

## Profile ownership and schema

The VM is authoritative for managed VPN configuration, revisions and credentials.
The workstation stores SSH host aliases, managed profile references, service
listener ports and destination SSH settings. Do not synchronize files in both
directions or put host paths/credentials in the workstation registry.

Profiles use `schema_version: 1`, a lowercase hyphenated name (1-48 characters),
a `backend` discriminated by `type`, and a TCP `health` endpoint. Both initial
backends require a private health endpoint to establish readiness. Future backend
schemas can have different readiness requirements. Examples are in
`examples/profiles/v1/`; they are non-secret data, not live deployment inputs.

`backend.config` is decoded into exactly one Go configuration type. Each type
owns its attributes, validation and defaults. Unknown keys, wrong case, duplicate
keys (including escaped equivalents), null values, unsupported versions and
mixed-backend attributes are rejected. Input must be UTF-8, at most 64 KiB, with
nesting depth at most 32. Errors do not reproduce field names or values from input.
Use the exported decode helpers, not bare `json.Unmarshal`, at trust boundaries.

L2TP accepts an IPv4 gateway or ASCII DNS hostname, optional IPv4 DNS addresses,
`force_encapsulation`, and IKE/ESP proposal arrays. Encapsulation defaults to true;
DNS/proposal lists are bounded. XAuth requires an IPv4 gateway literal and accepts
IKE/ESP proposals and optional IPv4 VPN DNS addresses. A hostname health target
requires that DNS list; there is no ordinary-resolver fallback. There is no switch to disable fail-closed behavior. Descriptor
capabilities express required runtime behavior, not verification of a running VPN.
IPsec proposal syntax is checked here; algorithm support/negotiation belongs to
the backend. Existing legacy proposal defaults are retained for compatibility.

Configuration field descriptors derive names/types from the Go structs. They are
backend description metadata, not a full JSON Schema implementation. Both CLI and
controller must call the same semantic validators. An absent or empty proposal
list selects legacy defaults. Effective configuration returns private copies so
one caller cannot change another profile's defaults.

The CLI JSON file includes a `credentials` object; the controller treats those
values, including usernames, as separate secret input. Initial backends
both need username/password/PSK. `profile.create` carries a non-secret profile and
credential input, but only the profile and a protected credential reference are
persisted in their respective stores. Credential values are non-empty, at most
4096 UTF-8 bytes each, and exclude NUL/CR/LF for compatibility with legacy
line-based backend credential files. The encoded create payload must also fit the
64 KiB limit. The CLI reads the owner-only JSON file without prompts and sends credentials
through the separate RPC credential field, never flags/environment variables.
MFA is prompted only during an XAuth connection.

The `secret.Value` type redacts fmt formatting and refuses normal JSON encoding.
Only explicit transport/protected-vault encoders release secret strings to bytes; secret checks
use predicates that must not retain or log plaintext.
Raw wire bytes remain sensitive: never log, dump or persist them. Go cannot
guarantee memory erasure, and wrapping a value does not protect arbitrary strings
or external logger/reflection integrations. Runtime logging must allowlist fields.

Credential persistence/provisioning is implemented in issue #4. Use private directories/files
(0700/0600), atomic writes and explicit rotation. Do not return secret values from
inspection. Reject updates/rotation/deletion of an active profile until the user
explicitly disconnects. Updates use expected revisions to prevent lost changes;
disconnect preserves credentials, while deletion is an explicit separate action.

## Managed identity and adoption

A managed profile named `example` has project `vpnmodo-example`, with labels:

```text
io.vpnmodo.managed-by=vpnmodo
io.vpnmodo.profile=example
```

Resolve a registered managed profile first; require its project and both labels
when selecting containers. Labels are an ownership filter, not authentication or
an instruction to adopt a container. A name prefix alone is insufficient. Never
treat `vpn-example`, unlabeled containers or a foreign manager as managed.

Installing the controller must leave existing launchers, manifests, watchdogs
and sessions untouched. Adoption explicitly creates a managed profile and performs
a planned cutover with one recovery owner. V1 need not retain legacy layouts or
provide an automatic importer. Preserve old configuration for rollback, which may
require new MFA. Do not silently run parallel logins against a gateway.

## RPC framing, negotiation and method surface

The workstation starts a fixed non-PTY host SSH command invoking a bridge inside
the container. The bridge forwards to its service's local Unix socket; no API
TCP port is exposed. Host setup/upgrade runs independently through host SSH/Docker
commands. The service owns operation lifetimes, not bridge/request contexts.

The application uses a restricted [JSON-RPC 2.0](https://www.jsonrpc.org/specification)
profile: a single compact UTF-8 JSON object per LF-terminated frame, at most 1 MiB
excluding LF, nesting depth at most 32. Arrays/batches are outside this profile.
Use string IDs (1-128 encoded bytes); signed int64 IDs are accepted, but null and
fractional IDs are excluded. Requests use named object params. stdout is protocol
only; stderr is sanitized diagnostics. A framing error closes the connection;
never execute a partial/truncated/oversized frame. The writer serializes frames.

JSON-RPC `jsonrpc: "2.0"` is distinct from application API `{major:1,minor:0}`.
Every connection must first call `system.hello`. Major versions must match and
the client's minor version must not exceed the server's. Features additionally
depend on backend capabilities. Failed renegotiation resets the session gate.
No state mutation or secret provisioning happens before successful negotiation.

Reserved method surface:

| Methods | Contract |
| --- | --- |
| `system.hello` | API version negotiation; controller version and backend list |
| `system.status` | Controller/storage/Docker readiness and runtime availability |
| `backend.list`, `backend.describe` | Supported backend descriptors/capabilities |
| `profile.create` | Typed profile plus separate credential input |
| `profile.list`, `profile.inspect` | Non-secret configuration, revision and credential presence |
| `profile.update`, `profile.rotate_credentials`, `profile.delete` | Explicit mutation; expected revision and inactive-profile checks in issue #4 |
| `connection.status` | Desired/observed state, intent generation and active/last operation IDs |
| `connection.ensure`, `connection.disconnect` | Managed profile reference; return/reuse operation ID |
| `operation.status`, `operation.cancel` | Opaque operation ID |
| `operation.subscribe` | Current operation snapshot, then revisioned change notifications |
| `operation.submit_mfa` | Operation ID, challenge ID and secret code |
| `connection.application` | Fresh, lifecycle-gated owned application ID for the VM stream helper |
| `system.doctor` | Profile-scoped live runtime/credential/prerequisite checks; never secret read-back |

Method constants include implemented handlers and future reservations. Exact
mutation/list/doctor payloads are completed with their owning implementation
issues. The controller skeleton implements hello, system status and backend
discovery over verified SSH and a private Unix socket. Profile management is implemented as described in the
[storage guide](002-profile-storage.md). Connection/operation RPC handlers are implemented with the
[operation manager](003-persistent-operations.md). The managed [L2TP](005-managed-l2tp.md)
and [XAuth](006-managed-xauth.md) adapters are enabled. Production
`runtime_enabled` reports that wiring; backend images and verified connectivity
remain separate checks.

`operation.changed` is a server notification without an ID. Client notifications
must never mutate state and receive no response, including when rejected. Request
IDs correlate one call/reply; operation/challenge IDs track longer-lived work.
Successful responses contain `result`; failures contain `error`, never both.
Unknown parse/request IDs are null in error responses. Errors use stable numeric
codes and fixed safe messages; no raw backend stderr, paths or arbitrary data.
Use the response helpers rather than constructing error envelopes from strings.

## Operations, concurrency and restart policy

Kinds are ensure, disconnect, unattended recovery, and profile change. States are
`queued`, `running`, `awaiting_mfa`, `verifying`, `succeeded`, `failed`, `cancelled`.

- Queued work can start, fail or cancel. Running connection/recovery work must
  verify before succeeding; disconnect/profile changes can finish from running.
- Only an explicit ensure operation may request MFA. After code submission it
  returns to running. Expiry, authentication rejection or repeated prompts fail
  the attempt; adapters must not start a new login/challenge automatically.
- Verification can succeed, fail or cancel. Terminal states cannot restart or
  transition; an explicit retry creates a new operation ID.
- Operation IDs are `op-` plus 32 lowercase hex digits; challenge IDs use `mfa-`
  plus 32 hex digits. Runtime generates unpredictable IDs. Record revisions start
  at 1 and increase on committed transitions. IDs are references, not credentials.
- Bind MFA submission to the active operation/challenge, reject it at/after expiry,
  and consume it atomically once. Store challenge identity/deadline, never the code.
  The current adapter accepts 4-10 ASCII digits. Choose the runtime timeout in the
  backend integration so host/controller/container deadlines agree.
- Keep one mutating operation/recovery owner per profile. Concurrent ensure calls
  join an existing attempt or reuse a verified session; they must not send a
  second login email. Different profiles can run independently.
- SSH loss unsubscribes the caller; it does not cancel the operation. Cancellation
  is explicit and distinct from disconnecting an established session.
- Preserve desired connected/disconnected intent separately from observed health.
  An intentional disconnect prevents automatic recovery. A private TCP failure is
  distinct from authentication failure and must not alone trigger fresh MFA.
- On controller restart, inspect actual containers/protection/health. Mark
  interrupted in-flight logins/profile changes failed with a classified outcome;
  reconcile partial writes/containers before retry. Do not claim that a saved
  record proves success or resume an OTP challenge by guessing its state. Check
  established sessions and preserve them where safe. Do not initiate fresh login
  merely because the VM rebooted. Recovery proceeds only under backend policy.
- Subscriptions publish current state and monotonic revisions, not guaranteed
  delivery of every event. On reconnect fetch a snapshot. Use bounded queues;
  slow clients cannot block profile operations. Retain bounded non-secret history.

These transition rules, persistence, locking, atomic challenge consumption,
worker lifetime and crash reconciliation are implemented in issue #5; see the
[operation guide](003-persistent-operations.md).

## Fail-closed enforcement and dependencies

XAuth fail-closed is mandatory. Enforcement belongs in the VPN network namespace
before application attachment and must survive controller/adapter failure. Protect
existing flows, DNS and IPv6, with only narrowly scoped VPN bootstrap exceptions.
No capability flag, state record, polling loop or stopped application is proof of
network enforcement. Issue #7 must supply packet-observed Linux acceptance before
XAuth is released through the new controller. Legacy instances are unchanged.

The profile/protocol model uses the Go standard library. `encoding/json` defaults
accept case-insensitive struct keys and duplicate members, so trust-boundary decoding
adds bounded duplicate/exact-shape checks and sanitizes errors. `fmt.Formatter`
and `json.Marshaler` provide the explicit secret-value policy. Go tests include malformed
input, round-trip fuzz seeds, framing/resource bounds and the state machine.

The controller uses pinned `github.com/creachadair/jrpc2 v1.3.5` for dispatch
and client correlation behind the existing strict framing/request gate. Built-in
methods and request logging are disabled. OpenSSH supplies verified non-PTY
transport; Python stdlib and Docker Compose handle explicit host bootstrap.
The image runs a static Go binary without Docker/SSH tools or network access.

Issue #5 implements a pinned bbolt transactional operation store, suited to one service per VM;
do not put credentials or wire params into it. No HTTP/workflow/database server is
required. Docker/Compose and Expect/native strongSwan remain backend adapters;
the choice of authentication implementation must preserve interoperability.

References: [encoding/json](https://pkg.go.dev/encoding/json),
[fmt formatting](https://pkg.go.dev/fmt#Formatter),
[jrpc2](https://pkg.go.dev/github.com/creachadair/jrpc2),
[bbolt](https://pkg.go.dev/go.etcd.io/bbolt).
