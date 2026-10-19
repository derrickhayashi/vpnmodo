# Managed XAuth, MFA and namespace enforcement

Issue #7 adds the second managed VPN backend. The shared `internal/managedvpn`
adapter preserves the exact project/profile/revision/image checks and private
credential bundles introduced for L2TP. Backend-specific manifests, startup and
authentication remain separate. Legacy launchers, their Expect script and their
image entry point retain their existing behavior.

## Authentication boundary

The native interface investigation found that the pinned `charon-cmd` credential
callback uses terminal prompts for the custom IKEv1 XAuth exchange. Generic VICI
configuration/observation support does not demonstrate equivalent interactive
support. Keep a managed Expect adapter until a replacement proves the actual
password/passcode exchange and quiet session lifetime. This adapter is separate
from the existing standalone script.

The managed state machine accepts one PSK prompt, one password prompt and one PIN
prompt in that order. It suppresses terminal echo and all client diagnostics.
It publishes only fixed state values into private tmpfs, never a transcript.
Absolute login deadlines cannot be extended by a stream of diagnostic lines.
Once a CHILD_SA is established, quiet periods have no login timeout; kernel and
service checks determine readiness. Unexpected prompts, EOF and timeouts fail.

Three small changes to pinned strongSwan 5.9.14 apply only when the managed entry
point selects them. The username comes from its protected credential mount rather
than a process argument. Password/PIN values are not added to the credential
cache, preventing a repeated XAuth request from replaying them. Legacy operation
keeps the original credential handling. A managed CHILD_SA also sets a native
XFRM output mark after ESP encryption. Both modes retain the existing
XAUTH_PASSCODE compatibility patch.

The lifecycle manager owns operation/challenge IDs and expiry. `connection ensure
--wait` and `operation watch` use a hidden controlling-terminal prompt. JSON
watchers report challenges without prompting. Automation can use `operation
submit-mfa ID --host HOST --challenge ID --code-stdin` with a pipe or protected
file. Codes are never command arguments. An invalid, expired, cancelled or already
consumed challenge is refused before backend delivery.

Delivery uses Docker exec stdin, a fixed command, and an exclusive 0600 tmpfs
file. The adapter deletes the file before sending the code and never persists it
in the profile, deployment or operation journal. Context cancellation triggers
owned-container cleanup. A controller crash interrupts the operation; startup
disposes an interrupted XAuth login namespace, including a late-established
session. A previously successful session can survive controller recreation.
Explicit disconnect retains the vault and removes runtime credential copies.

## Packet enforcement

The managed entry point installs and verifies IPv4 and IPv6 default-deny filter
rules before starting the VPN client. Application attachment requires authenticated
IPsec, intact enforcement and a successful private TCP probe. The application has
no capabilities, no-new-privileges, and UID/GID 65534. Docker restart is disabled;
a tiny init process reaps children and forwards termination signals.

Outbound workload traffic requires an ESP tunnel policy to the configured IPv4
gateway. There is no broad established-connection exception. Removing an SA or
policy therefore blocks existing TCP/UDP flows independently of controller polling,
Expect lifetime or application teardown. Enforcement persists for the namespace's
lifetime; a replacement namespace installs it before authentication/attachment.

Only root-owned UDP sockets between fixed local and peer IKE ports 500/4500, and
kernel-marked ESP/NAT-T to the configured peer, bypass the outbound policy
requirement. The native XFRM output mark is applied after encryption, not by a
pre-encryption firewall/conntrack rule. It distinguishes encrypted NAT-T packets
that retain the workload socket's UID from plaintext application UDP. Workloads
have neither NET_ADMIN nor NET_RAW, which are required to set SO_MARK. The tunnel
probe also requires the outbound SA's output mark. Workloads
cannot use that exception: their UID is unprivileged and they lack raw-socket
capability. Managed `charon-cmd` explicitly binds those ports instead of its default
ephemeral ports. No ordinary DNS or arbitrary peer TCP/UDP exception exists.

IPv6 is disabled for all/default interfaces and its filter policies drop traffic.
Loopback egress is limited to 127.0.0.1, excluding Docker's embedded resolver at
127.0.0.11. An optional XAuth `dns` list names IPv4 VPN resolvers; local dnsmasq
forwards only to that list, and its traffic still requires IPsec. It has no host or
ordinary-resolver fallback. Without a configured DNS list, use an IP health target;
private hostname resolution is unavailable. Both containers receive a read-only
resolver file pointing to 127.0.0.1. This fixed nonsecret file is mode 0444 for the
workload UID; its host parent remains 0700. Credentials remain 0600.

Enforcement verification compares the complete filter rules with their installed
digest, using `iptables -S`/`ip6tables -S` so timestamps and mutable chain/rule
counters are excluded.
This detects accidental filter changes before attachment and during checks. The
VM user/root/Docker administrators remain trusted; they can alter namespaces and
are outside this isolation boundary.

## Monitoring and recovery

Health reporting separates private endpoint failures, namespace detachment and
IPsec loss. Endpoint failure alone never starts another login. A healthy tunnel
can repair only its application namespace without MFA. On IPsec loss, stop the
application and report authentication required; do not begin a new PSK/password
attempt in the background. An explicit ensure creates a fresh attempt/challenge.
The kernel filter remains the protection while the controller is unavailable.

## Acceptance

The local Ubuntu lab uses a real aggressive-mode PSK gateway with strongSwan's
[xauth-radius multiple-round profile](https://docs.strongswan.org/docs/5.9/plugins/eap-radius.html#_multiple_rounds)
and a synthetic RADIUS verifier for password plus XAUTH_PASSCODE. No email service
or customer gateway is contacted. This proves the protocol exchange, not email
delivery or customer gateway certification.

A separate reachable escape sink shares the physical bridge. Its packet capture
filters the client's physical Ethernet source address, distinguishing direct
plaintext from authenticated packets forwarded by the gateway. A deliberate
positive control must first demonstrate that this sender, sink and capture detect
a bypass. Captures run inside their owning test containers so they cannot retain
a retired VPN namespace. The suite exercises pending MFA, private TCP/DNS, quiet
sessions, CHILD rekey, active flows with SAs/policies removed while controller and
client are suspended, recreation, expiry, wrong/repeated codes and controller
restart. Zero observed plaintext is required for protected phases. See the
[local lab](../../tests/local-lab/README.md) for execution and recorded validation.
