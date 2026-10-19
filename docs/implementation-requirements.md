# vpnmodo implementation requirements

Status: discussion captured for implementation review, 2026-10-19.
The v1 profile, protocol and operation implementation is tracked in issue #2.
Controller/profile/lifecycle management and managed L2TP/XAuth are implemented
in the private stage branches. Workstation [service access and diagnostics](design/007-service-access.md)
are implemented. Private release packaging supplies candidate artifacts;
adoption and ARM64 release acceptance remain the final release gate. See
[v1 profiles, protocol and operations](design/001-v1-model.md)
for the initial scope, ownership and lifecycle defaults selected for implementation.

## Goal and deployment model

Give team members a straightforward way to create VPN profiles and make private
services available through their own Linux VM. Each person owns their VM, which
may run locally or remotely. Both are reached over SSH from the workstation.

The agreed product and workstation command name is `vpnmodo`. Use Go, one
repository, and two executables: `vpnmodo` for the workstation and
`vpnmodo-controller` for the VM controller and bridge. Use JSON-RPC 2.0 over SSH
initially. Name the new controller image/service `vpnmodo-controller`. The private
repository remains `vpn-sidecars-next`; its name does not determine installed
commands. Preserve existing `scripts/vpn` entry points for legacy deployments.
A persistent HTTP service or WebSocket endpoint is not required for the initial
CLI. Keep transport separate from controller operations so another transport can
be added later without duplicating business logic.

### Agreed container deployment

The user agreed that the VM controller runs in a container with access to the
VM Docker socket. It manages sibling VPN/application containers through the host
Docker Engine. It is a trusted VM management component; socket access is not a
security sandbox against the controller itself.

- Run one persistent controller per personal VM, outside VPN network namespaces.
- Package the Go controller and required backend tooling in a versioned image.
- Keep the controller in its own Compose project, independent of profile projects.
  Disconnecting or deleting a profile must never remove the controller.
- Use Docker restart supervision rather than a separate systemd service for the
  controller process. Docker/SSH and host prerequisites remain VM responsibilities.
- The workstation uses non-PTY SSH to invoke a fixed `docker exec -i` bridge
  command. That short-lived process relays JSON-RPC to the service over a private
  Unix socket inside the controller container. Ending it does not end operations.
- Require no controller TCP listener, published API port or in-container SSH server.
- Persist profiles, credentials and operation metadata outside the image and
  source checkout. Use configured host storage roots mounted at the same absolute
  paths inside the controller wherever sibling containers need host bind mounts.
  Adoption may provision a new managed layout; supporting every legacy host
  path or mounting each old consumer root is not required for v1.
- Use a read-only image filesystem and explicit writable state/temporary mounts.
  The controller itself does not require `privileged: true`; backend container
  kernel capabilities are specified separately.
- Bootstrap and upgrade from the workstation over host SSH/Docker commands, so
  a failed controller can be repaired without a functioning controller API.
- Replace only the controller on upgrade. Preserve VPN containers and persistent
  data; reconcile observed runtime state on startup before scheduling recovery.
- Never assume that an operation survives a controller restart just because it
  survives an SSH disconnect. Define interrupted-operation handling explicitly.

Development takes place in this independent private repository. Customer data,
credentials and runtime artifacts remain outside its source tree. A future public
PR will transfer reviewed patches rather than this private Git history.

## Functional requirements

### Host setup

- Register a VM using an SSH target and user-selected local name.
- Support normal SSH configuration, authentication and host-key verification;
  do not bypass host-key checks or require copied workstation private keys.
- Check host reachability, Linux/Docker prerequisites and controller compatibility.
- Provide explicit setup/update operations for installing a compatible controller
  and provisioning prerequisites. Registration alone must not deploy anything.
- Detect existing deployments and report the intended changes before replacing
  components. Routine connection recovery must not become implicit provisioning.

### Profile creation and management

- Create a profile from backend-specific settings and securely supplied credentials.
  Referencing existing profiles alone does not meet the requirement.
- Discover supported backends, their configuration schema, secret fields and
  capabilities. Share typed definitions between CLI and controller.
- A guided creation wizard is not part of the agreed scope. Profile creation
  must support explicit configuration; exact flags/file syntax is a CLI design
  decision. Hidden secret and connection-time MFA prompts remain supported.
- Give each selected backend its own configuration section and schema, separate
  from common metadata, optional health checks, services and credential input.
  The backend discriminator selects the accepted attributes; do not use one flat
  set of optional fields or accept another backend's configuration attributes.
- Share backend field definitions between validation, API descriptions and any
  applicable prompts. Validate on both workstation and VM; reject unknown fields
  and unsafe names. Never accept secret values as command-line arguments.
- Provision credentials separately from the saved non-secret profile.
- Select storage paths from configured host/consumer roots, not arbitrary paths
  in RPC requests. Keep configuration and secrets outside the source checkout.
- Validate and provision a profile before reporting creation success. Use atomic
  writes and restrictive permissions; failed creation must not leave a usable
  partial profile. Handle local registration failure with a clear recovery path.
- Reject duplicate names by default. Profile update and credential rotation must
  be explicit operations and must not silently disrupt an active connection.
- Register, list and inspect managed profiles without importing their credentials
  into the workstation registry. Existing deployments may be explicitly converted
  or recreated in the new profile system; an in-place legacy importer is not
  required for v1.

### Connection orchestration

- Provide a single workstation command to ensure VPN connectivity, verify the
  configured private TCP endpoint, and optionally start selected local forwards.
- Reuse a healthy connection. Repeated and concurrent calls must not create
  duplicate sessions, start competing login attempts or corrupt runtime state.
- Support interactive MFA for IPsec/XAuth, with progress updates and a hidden
  workstation prompt. Bind each code to its operation and current challenge.
- Apply challenge expiration, bounded timeouts and explicit cancellation behavior.
  Do not automatically repeat failed MFA login attempts.
- Track long-running operations on the VM independently of an individual SSH
  session. After client disconnect, allow the CLI to query the operation and
  retrieve current state; do not require a code to be persisted for reconnection.
- Distinguish cancellation of an operation from disconnecting an established VPN.
- Preserve backend authentication and recovery semantics: unattended recovery
  where supported, and explicit fresh MFA when required. Existing shell watchdog
  and Expect implementations may be improved or replaced; they are not fixed
  architectural requirements. Validate replacements against existing behavior.
- Disconnect and report what was stopped, including workstation forwards managed
  by the CLI. Preserve credentials and unrelated profiles.

### Agreed existing-deployment compatibility and adoption

The user requires existing deployments to continue working, but does not require
v1 to import their exact existing layout or manage them in place.

- Preserve the existing launcher, Compose and Ansible deployment paths until a
  consumer explicitly adopts the new system. Controller installation alone must
  not stop, upgrade, mutate or take ownership of a legacy VPN or watchdog.
- Provide a documented v1 adoption path that creates a new managed profile using
  the relevant existing settings and securely provisioned credentials. A generic
  importer for every legacy directory/configuration format is not required.
- Preview the planned cutover, including container identities, credential handling,
  SSH integration and forwards. Keep legacy configuration intact for rollback;
  do not silently delete old credentials, manifests or consumer files.
- Stage and validate the new profile before cutover. Cutover must be explicit,
  with any reconnect/MFA requirement or interruption reported. Do not assume that
  a gateway permits simultaneous sessions with the same account.
- During cutover, stop the old profile's recovery owner and old session before
  activating its replacement as necessary. There must be one active lifecycle
  owner. Unrelated legacy or managed deployments must remain unchanged.
- Validate new private/service access before declaring success. On failure, report
  the partial state and provide explicit rollback; XAuth rollback may require
  fresh MFA and must not trigger unattended login attempts.
- Use unique controller-owned labels/project identities. Do not discover and
  automatically adopt arbitrary existing VPN containers.

### Agreed XAuth fail-closed requirement

Fail-closed is mandatory for the new XAuth backend, not an optional recovery
setting. It applies to managed application traffic in the VPN network namespace;
VM management traffic and unrelated host workloads remain outside this policy.

- Install default-deny network enforcement before application traffic is possible
  and before VPN startup. Failure to install/verify it is a connection failure.
- Permit application traffic only through the authenticated IPsec path. When the
  tunnel is absent, lost or partially established, block plaintext fallback through
  the container's ordinary network interface, including already-open flows.
- Keep enforcement active while waiting for MFA, during failed login, rekeying,
  recovery, cancellation and teardown, and after controller/Expect/client failure.
  Recreating a network namespace must reapply protection before attaching workloads.
- Allow only narrowly defined traffic needed to establish/maintain the VPN and
  approved local communication. Specify gateway IKE/ESP/NAT-T exceptions precisely;
  they must not become a generic route around IPsec protection.
- Cover DNS and IPv6 as well as IPv4. DNS must not silently fall back to an ordinary
  resolver; disable IPv6 unless equivalent tunnel enforcement is implemented.
- Enforce in the VPN network namespace independently of controller polling or
  stopping an application container. Health reporting is not a kill switch.
- Include packet-observed synthetic-lab tests for missing/dropped IPsec policies,
  tunnel loss with active flows, login/MFA failure, rekey/recreation, DNS/IPv6 bypass
  and controller failure. Confirm legitimate authentication/rekey still works.
- Preserve existing deployments' behavior until an explicit upgrade/adoption;
  do not claim untouched legacy XAuth instances have gained fail-closed protection.

### Watchdog and XAuth implementation improvements

The user explicitly welcomes improvements to both the watchdog and Expect
implementation. The following is a recommended design direction, subject to
backend compatibility validation:

- For controller-managed profiles, consolidate health monitoring and recovery
  into the persistent controller so there is one lifecycle authority. Disable the
  standalone watchdog for that profile before enabling controller-owned recovery;
  never run competing recovery loops. Retain the standalone deployment path until
  its migration is explicitly in scope.
- Track user-requested connection intent independently from observed health.
  A deliberate disconnect must not be undone by background recovery.
- Distinguish container failure, tunnel failure, unreachable private service,
  application namespace detachment, and a genuine need for reauthentication.
  An unreachable TCP endpoint alone must not be labeled as requiring fresh MFA.
- Use bounded recovery, persisted cooldown/backoff, cancellation and shared
  operation locks. Recover application forwarding only after validating VPN
  health. A failed recovery must leave forwarding stopped.
- Return typed health/recovery events to the CLI and operation store instead of
  requiring consumers to infer state from log strings.
- Replace the host-side terminal-only XAuth prompt with a structured challenge
  interface. Avoid parsing generic diagnostics as the control protocol.
- Keep Expect isolated as a backend adapter if needed. Improve explicit states,
  repeated-prompt handling, challenge expiration, EOF/exit handling, bounded
  diagnostics and secret-safe output. Prefer an ephemeral private channel for
  MFA delivery; any necessary temporary code file belongs in protected tmpfs
  with single-use consumption and cleanup on timeout, cancellation and restart.
- Investigate a native strongSwan control interface before replacing Expect.
  Generic support for configuring or observing tunnels is not proof that the
  current custom IKEv1 XAuth emailed-code exchange is supported. Retire Expect
  only after demonstrating equivalent authentication and quiet-session lifetime.
- Characterize existing behavior with synthetic tests before migration. Cover
  controller crashes, disconnect/recovery races, namespace replacement, probe
  failure without tunnel failure, MFA expiry/repeated prompts, quiet established
  sessions and secret exclusion from outputs and state.

### Service access and diagnostics

- Manage named SSH forwards on the workstation with localhost listeners by default.
- Detect occupied local ports and report the affected service and next action.
- Report VPN connectivity and local-forward readiness separately. A VM response
  cannot prove that a workstation listener or an authenticated destination works.
- Provide unified status for registered profiles and their managed service access.
- Provide a doctor command for SSH, controller versions, prerequisites, credential
  presence, profile/deployment mismatches, reachability and port conflicts.
- Offer readable terminal output and stable machine-readable JSON results/errors.
- Expose service endpoints when ready. Describe exactly which checks passed;
  private TCP reachability is not proof of destination authentication.

## Protocol and security requirements

- JSON-RPC requests use method names, parameters and request IDs; responses use
  results or errors. Notifications carry progress and action-required events.
- Keep request IDs distinct from persistent operation IDs and MFA challenge IDs.
- Define framing explicitly: proposed initial framing is one UTF-8 JSON object
  per line over a non-PTY SSH process, with message size and input limits.
- Reserve stdout for protocol messages and stderr for sanitized diagnostics.
- Negotiate/check protocol and backend capability compatibility before mutation.
- Never interpolate user data into shell command strings. Use validated arguments
  and a fixed remote controller entry point.
- Passwords, PSKs and MFA codes may cross encrypted SSH stdin/messages. Never put
  them in command arguments, ordinary logs, responses or persisted operation data.
- Store credentials only in the protected VM credential store/files required by
  the backend, with directories mode 0700 and files mode 0600. Do not retain a
  workstation credential copy by default. Minimize in-memory lifetime without
  claiming guaranteed memory erasure in Go.
- Keep configuration inspection separate from secret retrieval; no general
  credential read-back API is needed. Report availability rather than values.
- Run secret scans before pushes and in CI. Review diffs for customer identifiers
  as well; a clean secret scan does not establish absence of customer data.

## Remaining product decisions and recommended baseline

The deployment model, profile creation, secure credential provisioning and
backend-specific configuration requirements are clear enough to design the
controller. The following defaults are recommendations, not yet user decisions:

| Decision | Recommended initial behavior |
| --- | --- |
| Backend scope | L2TP/IPsec and IPsec/XAuth first; decide whether Tailscale must ship with v1. |
| Supported systems | Agreed first-release qualification: macOS arm64 workstation and Ubuntu 24.04 arm64 VM. Intel/amd64 native testing is outside the release gate; cross-built artifacts remain unvalidated on native Intel hardware. |
| Profile authority | VM is authoritative for managed VPN configuration and credentials. Workstation keeps host/profile references and local forward bindings. No automatic bidirectional file synchronization. |
| Lifecycle policy | SSH loss leaves operations running; MFA expires normally. On controller restart reconcile established sessions and mark interrupted logins as requiring explicit retry. Fresh authentication is not silently triggered by reboot. |
| Changes and deletion | Reject active-profile configuration changes by default; require explicit disconnect/apply. Disconnect preserves credentials. Delete requires a separate explicit operation and reports credential disposal. |
| Forward configuration | Profile references named remote services; workstation owns localhost ports and SSH destination settings. Import existing service mappings explicitly; do not rewrite consumer files. |

No shared multi-user service, browser dashboard, HTTP or WebSocket server is
required for this release. Versioned CLI binaries with checksums and a versioned
controller image are the distribution direction; Homebrew is an optional
installation channel. Libraries, database choice and exact RPC method naming are
engineering decisions to record in the design, not additional user prerequisites.
The proposed small-stack candidates are jrpc2, bbolt and the Go standard library.

Existing-deployment compatibility/adoption and mandatory XAuth fail-closed
behavior are now agreed above, along with the ARM64 release qualification scope.
The remaining product scope choice is first-release backend coverage.
Profile authority, lifecycle and
forward-configuration defaults above remain recommendations to settle in the
design. Implementation can begin with schemas and the controller skeleton.

## Suggested implementation sequence and acceptance

1. Define versioned schemas, backend capabilities, RPC framing, error codes and
   the operation state machine. Test malformed inputs and compatibility failures.
2. Build the SSH client/controller skeleton and host setup checks. Verify against
   an isolated Linux VM with normal host-key checking and no customer credentials.
3. Implement profile creation/registration and protected credential provisioning.
   Test synthetic secrets, invalid settings, duplicates and partial failures.
4. Integrate both agreed backends, operation locking, session reuse, MFA, operation
   reconnection and cancellation. Implement and packet-test XAuth fail-closed
   enforcement. Preserve existing backend/watchdog regression tests.
5. Add service-forward orchestration, unified status and doctor. Test occupied
   ports, VM unreachability, failed probes and independent profile isolation.
6. Package supported platform binaries and document a fresh teammate onboarding.
   Validate the complete path from installation to profile creation, connection,
   local service use and disconnect in a synthetic lab. Verify a legacy deployment
   remains operational alongside the new controller and exercise explicit adoption
   and rollback using synthetic settings. Live customer-network
   acceptance is separate and requires its own authorized deployment context.

For release, verify no secret values appear in protocol responses, error output,
logs, CLI arguments or operation records. Credentials must remain usable only at
expected backend storage locations. Verify a client reconnection can inspect an
existing operation and a second connect call does not send a second MFA request.
