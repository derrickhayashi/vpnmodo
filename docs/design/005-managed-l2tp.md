# Managed L2TP/IPsec runtime

The production controller manages L2TP/IPsec containers, checks tunnel health
and applies bounded recovery. [XAuth](006-managed-xauth.md) uses a separate
authentication and packet-enforcement path. Both backends support
[workstation service forwards](007-service-access.md).

## Runtime and images

The controller image bundles the Go service with Docker CLI/Compose from
`docker:28.5.1-cli`. It keeps the existing read-only filesystem, user/group mapping,
private Unix socket and state bind, no network attachment or published API ports.
Docker socket access supplies administrative VM authority. Commands always target
the configured local Unix socket, use argument arrays, discard stderr and bound
stdout. Cancellation kills the CLI/plugin process group. No shell or inherited
Docker context/environment constructs commands. Compose gets an explicit project,
manifest, project directory and empty environment file. JSON manifest strings
escape Compose dollar interpolation.

`vpnmodo setup` installs the release images before connecting. Development defaults
are `vpnmodo-l2tp:dev` and `alpine:3.22` for the application namespace. Routine
connect/recovery uses `--pull never`; it never builds, pulls, provisions the VM or
changes unrelated deployments. The application supplies a namespace and local
VPN DNS resolver configuration for proxied traffic and service forwards.
[Release bundles](../releases.md) supply offline image pinning/checksums and
explicit controller upgrades. Manual
controller deployments may set `VPNMODO_L2TP_IMAGE` and
`VPNMODO_APPLICATION_IMAGE`; image references are operator configuration and are
not accepted through profile JSON. Development controller builds retain those defaults. Release controller images
bake exact backend/application image IDs; workstation bootstrap preserves the
selected image's baked defaults.

## Ownership and lifecycle

Each profile uses project `vpnmodo-<name>`, the manager/profile identity labels,
backend and profile-revision labels, and only `vpn`/`application` services. Every
adapter method reads its private deployment record, queries the entire Compose
project plus every use of that profile label, and validates each container. A
unknown service, conflicting labels/image, duplicate service, or
container without a saved deployment record causes refusal before mutation.
Existing default networks must also carry the managed identity/backend labels.
A name alone never confers ownership. These checks prevent accidental adoption;
they are not a sandbox against the VM user or Docker administrator.

VPN startup uses its managed manifest. Application repair uses a separate
application-only manifest pinned to the verified VPN container ID, with no VPN
service definition or service dependency. Compose cannot replace or start a VPN
while repairing its workload, including when an external Compose version created
the healthy replacement. No orphan removal is used. Teardown removes exact owned IDs, application first, then VPN;
it never runs a broad `down`, wildcard removal or automatic resource adoption.
Deletion removes the owned network only if Docker permits it without disturbing
active users. A failed deletion retains the profile for explicit repair/retry.

`Observe` checks tunnel invariants independently of the private TCP endpoint, so
an endpoint outage cannot trigger a fresh login. Existing startup sessions are
waited for within the operation deadline. New sessions attach the application
only after the authenticated tunnel and private TCP probe pass. Health checks
separate startup, stopped/failed container, tunnel loss, endpoint outage and
application namespace detachment. Namespace identity is read inside both
containers; matching container IDs or `network_mode` alone cannot detect a Docker
restart that replaced the VPN sandbox.

Controller-managed VPN containers have Docker restart disabled. Their strongSwan
DPD/close actions and xl2tpd redial do not create competing reconnect loops.
The idle health loop waits for a background sleep, allowing TERM to run cleanup
immediately within Docker's two-second stop limit. A foreground sleep could
defer that handler until Docker force-killed the VPN, leaving an authenticated
peer session behind and interfering with a later reconnect.
Managed PPP requests its address from the gateway (`noipdefault`) instead of
proposing the container's ordinary interface address, avoiding overlapping outer
and PPP routes with permissive peers. See the
[pppd options reference](https://ppp.samba.org/pppd.html).
A clean externally stopped VPN/application
is treated as intentionally stopped and requires explicit ensure. A missing
previously managed VPN is tunnel loss; persisted desired-disconnected intent
prevents its revival. Recovery uses the policy from
[004-recovery-policy.md](004-recovery-policy.md), including cooldown, bounded
attempts, forwarding quiescence, cancellation and no fresh login on restart.

## Credentials and state

The authority remains the 0700/0600 profile vault. An internal `WithCredentials`
callback materializes only the selected profile's username, password and PSK into
separate 0600 files in a private generated credential bundle. No credential
read-back RPC is added. The callback runs under the profile transaction, without
external commands or retained plaintext fields. Compose mounts those three files
read-only at `/run/secrets/vpn_*`; it never mounts the full vault or places values
in argv, environment, manifests, results or operation records. Application
containers receive none of these mounts. They run as the VM user with a separate
read-only local-resolver configuration bind and no capabilities. Docker logging is disabled for managed
VPN/application containers to avoid persistent backend authentication transcripts.
[VPN logging](../logging.md) captures fixed lifecycle/health classifications and
allowlisted backend diagnostic codes separately, without retaining raw output.

`controller-state/deployments/<name>/` contains a protected nonsecret deployment
record and Compose manifest, plus the private runtime credential bundles. The
record includes backend config/revision and image identity, not credential values.
Disconnect removes both containers and runtime credential copies while preserving
the authoritative vault. Successful profile deletion removes deployment metadata
and the vault record. Failures leave unknown/partial state for explicit retry;
they never claim credential disposal without completing it. No secure-erasure
claim is made for filesystem/Docker storage or backups. The VM user, root and
Docker administrators remain trusted with credential access.

The backend writes generated strongSwan/PPP files privately inside the container.
Its `/run` state is tmpfs. Traffic may leave through PPP or narrowly specified
gateway IPsec/IKE exceptions. There is no broad established-flow egress exception.
