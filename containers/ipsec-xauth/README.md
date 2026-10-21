# IPsec/XAuth backend

This backend establishes an IKEv1 Aggressive Mode PSK/XAuth tunnel with
interactive emailed MFA. Select it with `VPN_BACKEND=ipsec-xauth` in a named
profile. See [profile setup and commands](../../docs/profiles.md).

For managed profiles, select `backend.type: "ipsec-xauth"` in the
[complete JSON example](../../README.md#vpn-configuration). Optional
`backend.config.ike_proposals` and `esp_proposals` arrays override the profile
defaults. See [proposal settings](../../docs/profiles.md#ike-and-esp-proposals)
for defaults, ordering, supported syntax and the pinned `charon-cmd` behavior:
it uses the supplied lists without appending engine defaults and does not
accept the `ipsec.conf` trailing `!`. The legacy equivalents are comma-separated
`VPN_IKE_PROPOSALS` and `VPN_ESP_PROPOSALS` values in `profile.env`.
This backend uses IKEv1; IKEv2 support is separate planned backend work.

`Dockerfile` builds strongSwan 5.9.14 on Alpine 3.22. Local patches handle
`XAUTH_PASSCODE` through the PIN prompt and honor `half_open_timeout` in
Aggressive Mode. `connect.exp` drives one authentication attempt, redacts the
password and PSK from diagnostic output, and disables the startup timeout once
the CHILD_SA is established. These are local changes to strongSwan.

`vpn` controls Compose and validates TCP connectivity from both the VPN and
application containers. Use `scripts/vpn` at the collection root to supply its
profile configuration. Credentials are mounted read-only; each instance owns
its own MFA file, lock and sanitized log directory. The healthcheck detects an
ESP policy, and the controller additionally tests the configured TCP endpoint.

This backend has no traffic kill switch after tunnel loss and does not support
unattended MFA recovery. Kernel IPsec/XFRM support and NET_ADMIN are required.
The shared watchdog runs in manual mode: it reports tunnel or private TCP
endpoint failure without recreating either sibling. Reconnect through the
launcher with a fresh emailed code. Do not assume PPP/DNS setup from L2TP.

Both backends use `Dockerfile`, image-only `compose.yaml`, and an explicit
`compose.build.yaml` override for local builds. The launcher builds with that
override before runtime deployment.

### Managed XAuth safe failure diagnostics

Managed attempts suppress transcripts and never retry authentication. Fixed
process exit reasons can be recovered from Docker lifecycle events after cleanup:
20 repeated/unexpected PSK prompt; 21 password prompt; 22 PIN prompt;
23 initial login timeout; 24 MFA input timeout; 25 timeout after MFA submission;
26 client exit before submission; 27 client exit after submission;
28 invalid MFA input; 29 invalid mounted credentials;
30 establishment before required MFA; 31 established client exit.
Exit 2 remains the generic authentication guard. An exit reason identifies a
stage, not proof that a user entered an incorrect code. Repeated prompts or
client exits require diagnosis before another explicit attempt with fresh MFA.
A concurrent health-check exec may exit 137 when the VPN process exits; the
controller reads the owned VPN container's status to preserve authentication
failure classification. No credential or raw client output is retained.
