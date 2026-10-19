# IPsec/XAuth backend

This backend establishes an IKEv1 Aggressive Mode PSK/XAuth tunnel with
interactive emailed MFA. Select it with `VPN_BACKEND=ipsec-xauth` in a named
profile. See [profile setup and commands](../../docs/profiles.md).

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
