# Managed IKEv2 with EAP-MSCHAPv2

`ipsec-ikev2` uses the separate `vpnmodo-ikev2` image, strongSwan charon and
swanctl/VICI. The existing Go controller owns each profile's lifecycle and
application namespace. Username/password EAP-MSCHAPv2 authenticates the client;
a CA-verified certificate and an explicit expected IKE identity authenticate the
server. This backend has no PSK, emailed-code challenge or interactive MFA.

## Install and import

Use a controller and image bundle built from this source. Existing release
manifests remain readable but do not gain an IKEv2 image automatically. Newly
built VM bundles include the `ikev2` role and its immutable local image ID; GHCR
publication uses `ghcr.io/derrickhayashi/vpnmodo/ikev2`. The controller image and
Compose override are `IKEV2_IMAGE` and `VPNMODO_IKEV2_IMAGE`, respectively.

For development on an existing native Linux Docker builder:

```sh
docker build -t vpnmodo-ikev2:dev containers/ipsec-ikev2
docker build -t vpnmodo-controller:dev -f containers/controller/Dockerfile .
```

Activate that controller through the existing explicit host setup/replacement
workflow; rebuilding an image does not replace a running controller. The VM
requires kernel XFRM/IPsec, IPv4 policy routing, iptables policy/owner/mark
matches and IPv6 filtering. PPP is unnecessary for this backend.

The [synthetic profile](../examples/profiles/v1/example-ikev2.json) is nonsecret.
To make a combined import file, keep a private input JSON and CA PEM outside the
checkout. That input has `username` and `password` strings. This example reads
both files without placing credentials in command arguments or terminal output:

```sh
umask 077
python3 - <<'PY'
import json
from pathlib import Path
profile = json.loads(Path('examples/profiles/v1/example-ikev2.json').read_text())
private = Path.home() / '.config/vpnmodo-private'
credentials = json.loads((private / 'eap.json').read_text())
credentials['ca_cert'] = (private / 'ca.pem').read_text()
profile['credentials'] = credentials
(private / 'import.json').write_text(json.dumps(profile) + '\n')
(private / 'import.json').chmod(0o600)
PY
vpnmodo profile create example-ikev2 --file "$HOME/.config/vpnmodo-private/import.json"
vpnmodo connection ensure example-ikev2 --wait
```

Adapt synthetic server, identities, DNS and health endpoint in the private file.
Never put real configuration, credentials, CA bundles or runtime evidence in
source. Import also supports protected JSON stdin/file descriptors through the
existing credential flags. `profile inspect` omits credentials and trust data;
`profile rotate-credentials` replaces the complete credential set while inactive.

`ca_cert` is a required PEM bundle of 1–16 CA certificates, at most 32 KiB. The
vault checks PEM/X.509 structure and CA basic constraints. Private keys, extra
PEM types, trailing text and a `psk` field with a value are rejected. Username
and password use the existing 4 KiB UTF-8/no-newline limits. The server handshake
checks chain signatures, certificate validity dates and `server_identity`.
Only the imported CAs are loaded into charon: system/browser trust is not an
implicit fallback. There is no verification-disable option. Updating a private
CA bundle uses the same credential rotation boundary as passwords.

`server_identity` is required and independent of the network gateway address.
Use the identity configured by the gateway and covered by its certificate, for
example `vpn.example.test` or `@vpn.example.test` for an explicit FQDN identity.
`client_identity` selects the IKE identity; `eap_identity` selects EAP identity
and secret lookup. Each defaults to the protected `username` when omitted.
Explicit identities use ASCII letters/digits and `_ . @ : + / -`, start with
an alphanumeric character, `_` or `@`, and are at most 253 characters. Wildcards,
DN expressions and configuration snippets are excluded.

## Proposal policy

| Setting | Default |
| --- | --- |
| `ike_proposals` | `["aes256-sha256-modp2048"]` |
| `esp_proposals` | `["aes256-sha256"]` |

Omitted/empty lists select these IKEv2-specific defaults. Each explicit list
contains 1–32 complete proposals in preference order. Order and duplicates are
preserved. Entries are joined with commas and sent unchanged to swanctl; neither
`default` nor any other implicit proposal is appended. A peer can choose only a
combination offered by that list; responder preference can determine which
mutually supported proposal wins. A trailing `!` is ipsec.conf syntax and is
rejected here.

Supported syntax deliberately uses one algorithm of each kind per entry:

- IKE CBC: `aes128` or `aes256`, then `sha256`, `sha384` or `sha512`, optionally
  an explicit `prfsha256`, `prfsha384` or `prfsha512`, then one DH group.
  Without an explicit PRF, strongSwan derives it from the integrity algorithm.
- IKE AEAD: `aes128gcm16` or `aes256gcm16`, then an explicit PRF, then DH.
- ESP: CBC AES plus SHA-2 integrity, or AES-GCM16; optionally one DH group.
- DH groups: `modp2048`, `modp3072`, `modp4096`, `ecp256`, `ecp384`, `ecp521`,
  `curve25519`. Separate entries express alternatives. Multi-algorithm entries,
  `default`, `none`, SHA-1, 3DES, commas inside an entry and `!` are rejected.

Examples: `aes128-sha256-modp2048`, `aes256gcm16-prfsha384-ecp384` and ESP
`aes256gcm16-ecp384`. An ESP DH group applies to subsequent CHILD_SA rekeys;
the first CHILD_SA derives key material from IKE_AUTH without a separate DH
exchange. Check both initial establishment and rekey with the intended gateway.
The supported grammar does not guarantee every host kernel/crypto provider can
instantiate every transform; unsupported transforms fail without widening policy.
See the upstream [swanctl configuration](https://docs.strongswan.org/docs/latest/swanctl/swanctlConf.html)
and [proposal reference](https://docs.strongswan.org/docs/latest/config/proposals.html).

## Network and lifecycle behavior

The first implementation uses IPv4 outer addresses and requests one IPv4 virtual
IP (`0.0.0.0`). The gateway must assign it. The local selector is `dynamic`
(the assigned VIP); the requested remote selector is `0.0.0.0/0`. The gateway
may narrow it. Only destinations covered by installed tunnel policies can pass.
This is policy-based IPsec with strongSwan's table-220 routes and VIP source
selection. IKE sockets and outer ESP use a fixed mark to bypass those routes.
MOBIKE is disabled so firewall peer exceptions remain bound to one address.
UDP encapsulation is forced; ports 500/4500 and the peer are fixed.

A literal gateway IPv4 needs no lookup. For a hostname, the network-isolated
controller starts a disposable unprivileged resolver container from the backend
image on Docker's bridge. It receives the hostname on stdin, no secrets or state
mounts, and returns the first IPv4 result. Resolution has a five-second deadline
and explicit cleanup. The resulting address is pinned for that attempt; a fresh
reconnect resolves again. There is no DNS exception in the protected namespace.
There is no IPv6 transport, multiple-address failover or live address migration.

`dns` is an explicit list of up to eight IPv4 VPN resolvers. A loopback dnsmasq
forwards only to that list through IPsec. Gateway-pushed DNS is ignored.
Application and backend resolver files point to `127.0.0.1`; Docker's embedded
resolver is blocked. With no `dns`, use literal private endpoint addresses;
hostname health checks require configured DNS. IPv6 is disabled and filtered.
No online CRL/OCSP service is configured or guaranteed; revocation uses
strongSwan's relaxed availability policy. Chain, identity and expiry verification remain mandatory.

Namespace filtering is installed before reading credentials or starting charon.
Applications run as UID 65534 with all capabilities dropped. Plaintext egress is
limited to root-owned IKE sockets for the pinned peer; application egress requires
an outbound ESP tunnel policy. No conntrack ESTABLISHED exception can preserve a
plaintext flow after XFRM loss. Kernel filtering survives daemon/controller loss
and is checked for changes. Readiness requires an established IKEv2 SA, an
installed tunnel CHILD_SA, an IPv4 VIP, expected kernel state/policy and the
post-encryption mark. The controller then requires a private TCP health probe
before attaching the application to that exact namespace.

Each startup is one attempt with a 120-second deadline and no daemon reconnect
loop. The controller removes applications before namespace replacement. Normal
loss uses the existing maximum of three consecutive recoveries, with persisted
five-minute exponential cooldown capped at 30 minutes. A healthy private-service
failure alone does not start another login. Authentication, certificate and
proposal failures exit permanently and stop unattended retries; fix the profile
or rotate credentials, then explicitly ensure again. Backend shutdown is bounded;
operation cancellation/disconnect tears down the attempt and runtime credentials.

A controller restart preserves and re-verifies an established namespace. An
interrupted startup operation is recorded as interrupted; it does not gain an
MFA challenge or an unbounded login loop. Explicit ensure resumes a still-starting
bounded attempt or starts fresh after cleanup. The vault remains private between
connections; per-attempt secret files and swanctl configuration use protected
mounts/tmpfs. Raw charon/swanctl transcripts never enter Docker logs or RPC.

## Troubleshooting

Use `vpnmodo connection logs example-ikev2` and the normal operation/doctor
commands. Fixed diagnostic reasons include `authentication_failed`,
`certificate_untrusted`, `certificate_expired`, `server_identity_mismatch`,
`proposal_mismatch`, `handshake_timeout`, `tunnel_state_lost` and `client_exit`.
Certificate reasons can overlap when strongSwan reports several validation
failures. Expired certificates can produce `certificate_untrusted`; inspect
certificate validity as well as the CA chain. A gateway may reject the requested
server identity with a generic
AUTHENTICATION_FAILED response before sending its certificate; that produces
`authentication_failed`, so check the expected server identity as well as EAP
credentials. Check CA chain/validity and the expected IKE identity; do not bypass
verification. Timeouts also cover unreachable gateways and missing UDP mappings.
`client_exit` covers startup/plugin/configuration failures without exposing raw
input. A healthy SA with a failed private TCP probe leaves applications detached.

## Limitations

IKEv2 currently supports IPv4, EAP-MSCHAPv2 credentials and one gateway address
per attempt. Client certificates, PSK-only authentication, additional EAP/MFA
methods, IPv6 and MOBIKE are unavailable.

Use native Linux storage for controller state. Docker Desktop host bind mounts
can project file ownership in a way that prevents the controller from accepting
private state. Do not loosen ownership checks to work around this.

Native AMD64, cross-version runtime upgrades, IKE SA rekey/reauthentication,
long-running sessions, PMTU/large transfers, every accepted crypto transform,
and a complete connection through external gateway-hostname resolution are not
yet qualified. See [development](development.md) for local test instructions.
