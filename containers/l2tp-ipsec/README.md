# L2TP/IPsec backend

Select `backend.type: "l2tp-ipsec"` in a vpnmodo JSON profile. This backend uses
strongSwan IKEv1/IPsec transport mode around xl2tpd and PPP. Authentication uses
username, password and PSK. See the
[profile example](../../examples/profiles/v1/example-l2tp.json) and
[profile commands](../../docs/onboarding.md).

`backend.config.ike_proposals` and `esp_proposals` override the
[proposal defaults](../../docs/profiles.md#ike-and-esp-proposals). L2TP passes
these to `ipsec.conf`; append `!` to the last entry of each list to prevent
strongSwan from adding engine defaults.

## Runtime and networking

`vpnmodo setup` prepares Docker and the PPP/XFRM prerequisites in the managed
Ubuntu VM. The VPN container receives NET_ADMIN, NET_RAW and `/dev/ppp`.
The application shares its network namespace and receives no VPN credentials.
Credentials and generated strongSwan/PPP files remain in protected storage.

PPP obtains its IPv4 address from the gateway. After establishment, application
traffic uses `ppp0`; a host route preserves access to the VPN server. The IPv4
OUTPUT filter permits PPP traffic and narrowly scoped gateway IKE/IPsec traffic.
IPv6 is disabled. DNSmasq listens on loopback and forwards to the profile's
explicit `dns` list, or PPP-provided DNS if the list is omitted. Startup fails
when neither supplies DNS servers.

The controller checks tunnel state and a private TCP endpoint before attaching
the application. It owns bounded recovery and namespace repair; the backend has
no independent reconnect loop. A destination outage alone does not trigger a
new login. Use `vpnmodo status PROFILE`, `vpnmodo doctor PROFILE` and
`vpnmodo logs PROFILE` to diagnose failures. See the
[runtime design](../../docs/design/005-managed-l2tp.md) and
[recovery policy](../../docs/design/004-recovery-policy.md) for details.

## Build

From the repository root, on a native Linux Docker builder:

```sh
docker build -t vpnmodo-l2tp:dev containers/l2tp-ipsec
```

Use the controller setup or upgrade workflow to activate an image. A local
build does not replace a running VPN. See [development](../../docs/development.md)
for checks and the [local lab](../../tests/local-lab/README.md) for protocol tests.
