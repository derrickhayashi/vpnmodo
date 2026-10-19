# vpnmodo staged implementation plan

Status: agreed staged delivery approach, 2026-10-19. Issue links are recorded below.
See [requirements](implementation-requirements.md) for product requirements and
[development boundaries](development.md) for publishing rules.

## GitHub backlog

Umbrella tracker: [#1](https://github.com/derrickhayashi/vpn-sidecars-next/issues/1).

- Stage 1: [#2 — vpnmodo v1: Define the v1 contracts and backend profile model](https://github.com/derrickhayashi/vpn-sidecars-next/issues/2)
- Stage 2: [#3 — vpnmodo v1: Ship the container controller and SSH transport skeleton](https://github.com/derrickhayashi/vpn-sidecars-next/issues/3)
- Stage 3: [#4 — vpnmodo v1: Implement secure managed profile creation](https://github.com/derrickhayashi/vpn-sidecars-next/issues/4)
- Stage 4: [#5 — vpnmodo v1: Implement persistent operations and lifecycle coordination](https://github.com/derrickhayashi/vpn-sidecars-next/issues/5)
- Stage 5: [#6 — vpnmodo v1: Integrate L2TP and controller-owned recovery](https://github.com/derrickhayashi/vpn-sidecars-next/issues/6)
- Stage 6: [#7 — vpnmodo v1: Integrate XAuth MFA with mandatory fail-closed enforcement](https://github.com/derrickhayashi/vpn-sidecars-next/issues/7)
- Stage 7: [#8 — vpnmodo v1: Add workstation service access and diagnostics](https://github.com/derrickhayashi/vpn-sidecars-next/issues/8)
- Stage 8: [#9 — vpnmodo v1: Validate adoption, packaging and teammate onboarding](https://github.com/derrickhayashi/vpn-sidecars-next/issues/9)

## Tracking and delivery

Use one umbrella GitHub issue for the release and one issue per reviewable stage.
Keep requirements and design decisions versioned in the repository. Issues hold
scope, dependencies, acceptance criteria and progress, with links to the relevant
documents. This private repository is the implementation/review destination.

Each implementation issue should include:

- User-visible outcome and scope, including explicit exclusions.
- Dependencies and links to design/requirements sections.
- Observable acceptance criteria and relevant failure cases.
- Validation evidence required to close it, including Linux lab checks when needed.

Use a small state sequence: Backlog, Ready, In progress, In review, Done. Mark an
issue Ready only when its dependencies and material design choices are resolved.
Keep one primary implementation stage in progress initially. Split a stage if it
cannot be reviewed comfortably in one PR. Acceptance criteria, rather than a
fixed issue/PR count, determine completion.

For an authorized work request, read the issue and repository instructions, work
on a feature branch, run relevant checks, and produce a PR in the private repo.
Include validation evidence and unresolved limitations. Review/merge authorization
is separate; creating issues does not authorize unattended monitoring, automatic
issue selection, or merging. Public contribution remains a later explicit step.

No real credentials, customer names, addresses, logs or deployment details belong
in issue bodies, PRs, screenshots or CI output. Use synthetic fixtures and sanitized
failure summaries. Scan every candidate push and review diffs for customer data.

The product/CLI name is `vpnmodo`; the controller executable, image and service
are named `vpnmodo-controller`. Preserve legacy commands during adoption.

## Implementation stages

### 1. Define the v1 contracts and backend profile model

Dependencies: none.

Deliver typed backend-specific profiles, versioned RPC/messages and errors, an
operation state machine, credential-input design, ownership rules and lifecycle
policy. Record platform/backend release scope and justified library choices.
A creation wizard is excluded. Acceptance: valid/invalid synthetic profiles,
unknown backend fields and incompatible protocol versions have defined outcomes;
legacy and managed identities cannot be confused.

### 2. Ship the container controller and SSH transport skeleton

Dependencies: 1.

Deliver controller serve/bridge modes, local socket access, container image,
separate Compose project, persistent/ephemeral mounts, restart behavior and
workstation host registration/setup checks. Acceptance: an isolated Linux VM
serves version/capability/status calls over verified non-PTY SSH, with no exposed
API port; bridge loss leaves the service running; bootstrap can repair a stopped
controller without using its API. Existing VPN deployments remain untouched.

### 3. Implement secure managed profile creation

Dependencies: 1, 2.

Deliver create/list/inspect/register and explicit update/credential-rotation
behavior. Acceptance: schemas are enforced at both ends; profile creation is
atomic; duplicate/unsafe names are rejected; permissions are correct; failures
have a recoverable outcome; secret values do not appear in CLI arguments, saved
non-secret profiles, results, operation records or diagnostics.

### 4. Implement persistent operations and lifecycle coordination

Dependencies: 2, 3.

Deliver operation records, locks, idempotent ensure, subscriptions/status,
timeouts, cancellation and restart reconciliation using a synthetic backend.
Acceptance: concurrent callers join/reuse appropriate operations, disconnects
cannot be undone by recovery, SSH reconnection can inspect progress, interrupted
MFA is not silently retried, and failed state persistence is handled explicitly.

### 5. Integrate L2TP and controller-owned recovery

Dependencies: 3, 4.

Deliver managed Compose lifecycle, health checks, bounded automatic recovery,
application namespace repair and recovery-owner migration. Acceptance: healthy
sessions are reused; cooldown and intentional stops are respected; failed
recovery leaves forwarding stopped; the legacy watchdog cannot compete with the
controller for the same profile; existing standalone regression tests still pass.

Deliver this stage in two reviewable PRs: first the persistent recovery policy and
standalone watchdog ownership guard; then the managed L2TP Docker/Compose adapter,
credential mounting and isolated Linux VPN acceptance. The first PR does not
activate production backend control or complete this issue. See the
[recovery policy](design/004-recovery-policy.md) for its interface and boundaries.

### 6. Integrate XAuth MFA with mandatory fail-closed enforcement

Dependencies: 3, 4; use lifecycle lessons from 5.

Deliver structured challenge handling, secure ephemeral code delivery and a
validated Expect/native-interface adapter. Network enforcement is required before
application attachment. Acceptance: packet-observed Linux tests demonstrate no
plaintext fallback before login, on tunnel loss with active flows, during rekey,
controller/adapter failure or namespace recreation; DNS/IPv6 bypass is covered;
legitimate IKE/IPsec still works; expired/repeated MFA requests are bounded and
quiet established sessions remain alive. Split enforcement and MFA into dependent
PRs if needed; do not release the backend before both acceptance sets pass.

### 7. Add workstation service access and diagnostics

Dependencies: 4, 5; verify against 6 when available.

Deliver named localhost SSH forwards, composed up/down behavior, unified status,
JSON output and doctor. Acceptance: distinguish VM/tunnel/private-service/local
listener failures, detect port conflicts, preserve unrelated access and report
partial success without claiming end-to-end readiness. Test SSH destination
host-key/authentication behavior and forwarding process lifecycle.

### 8. Validate adoption, packaging and teammate onboarding

Dependencies: 5, 6, 7.

Deliver supported CLI binaries and controller images with pinned release identity,
checksums, upgrade instructions, legacy-to-managed adoption/cutover/rollback guide,
and lab acceptance evidence. The agreed qualification targets are macOS arm64
and Ubuntu 24.04 arm64; Intel/amd64 native testing is outside the release gate.
Acceptance: fresh onboarding works on those targets; a legacy deployment
continues working beside the controller; explicit
adoption and rollback work with synthetic credentials and fresh MFA when needed;
controller upgrades preserve VPN containers/state; no legacy layout importer or
VM creation integration is required.

## Release gate

Do not equate synthetic unit tests with live protocol interoperability or packet
isolation. Require the relevant isolated Linux acceptance evidence before closing
backend/release issues. Any real customer-network validation is separate and uses
explicitly authorized deployment context outside repository and issue artifacts.
