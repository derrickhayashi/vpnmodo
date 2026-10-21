# VPN profiles and proposal settings

For managed installation and complete profile JSON examples, start with
[VPN configuration](../README.md#vpn-configuration). Create a profile on the Mac
with `vpnmodo profile create work --file "$HOME/vpnmodo-private/work.json"`.
Keep this plaintext input file outside Git. To change an existing profile, use
the [profile update workflow](onboarding.md#update-a-profile-or-rotate-credentials).

## IKE and ESP proposals

Both `ipsec-xauth` and `l2tp-ipsec` accept optional `ike_proposals` and
`esp_proposals` arrays inside `backend.config`. The complete
[XAuth and L2TP JSON examples](../README.md#vpn-configuration) show both lists.
These backends use IKEv1: XAuth uses Aggressive Mode with PSK/XAuth and emailed
MFA; L2TP uses Main Mode with PSK and IPsec transport mode around L2TP/PPP.
For IKEv2 EAP-MSCHAPv2, use the separate [IKEv2 guide](ikev2.md). These IKEv1
settings do not change the IKE version or authentication method.

IKE proposals negotiate protection for the IKE control connection: encryption,
integrity/hash and Diffie-Hellman (DH) group. For example,
`aes256-sha256-modp2048` means AES-CBC with a 256-bit key, SHA-256 and a 2048-bit
MODP DH group; strongSwan derives the matching PRF from the hash.
ESP proposals negotiate protection for the IPsec data traffic: encryption and
integrity, plus an optional DH group for a separate Perfect Forward Secrecy
(PFS) exchange during IKEv1 Quick Mode setup and rekeying. Thus `aes256-sha256`
omits a separate ESP DH exchange, while `aes128-sha1-modp1536` requests one.
See the [strongSwan cipher-suite reference](https://docs.strongswan.org/docs/latest/config/proposals.html)
and [IKEv1 option semantics](https://raw.githubusercontent.com/strongswan/strongswan/5.9.14/man/ipsec.conf.5.in).

### Profile defaults

Each row lists the vpnmodo defaults in order. Omitting a list, or supplying
`[]`, uses that row. `null` is rejected by the JSON decoder. A nonempty list
**replaces that profile default list**, independently of the other list. It
does not append to the row.

| Backend | Setting | Ordered profile defaults |
| --- | --- | --- |
| `ipsec-xauth` | `ike_proposals` | `aes128-sha1-modp1536`, `aes256-sha256-modp1536` |
| `ipsec-xauth` | `esp_proposals` | `aes128-sha1-modp1536`, `aes256-sha1-modp1536` |
| `l2tp-ipsec` | `ike_proposals` | `aes256-sha256-modp2048`, `aes256-sha1-modp2048`, `aes256-sha1-modp1024`, `aes128-sha1-modp1024`, `3des-sha1-modp1024` |
| `l2tp-ipsec` | `esp_proposals` | `aes256-sha256`, `aes256-sha1`, `aes128-sha1`, `3des-sha1` |

These are compatibility defaults, including legacy algorithms. Get an approved
list from the gateway administrator rather than treating the defaults as a
security recommendation. Engine defaults can extend the L2TP lists as described
below.

### Syntax and ordering

Use one dash-separated strongSwan proposal per JSON array entry, in preferred
order. For these IKEv1 backends, put one algorithm of each kind in each proposal;
use separate entries for alternative combinations. For example,
`["aes256-sha256-modp2048", "aes128-sha256-modp2048"]` offers two IKE proposals.
Do not put commas or spaces inside an entry. Names are lowercase; the managed
validator permits only letters, digits and single separating hyphens, with an
optional final `!`. It allows at most 32 entries per list and 256 characters per
entry. Some strongSwan keywords containing underscores are therefore unavailable
through this profile format.

Profile validation checks the string format, not whether an algorithm name,
combination or plugin is usable. strongSwan parses the proposals at connection
time; crypto plugins, the Linux kernel and the gateway must support them.
Valid JSON alone does not prove that negotiation will succeed.

vpnmodo preserves array order when passing proposals to the client. Ordering
expresses preference; the gateway still selects a mutually acceptable proposal
according to its policy. A strongSwan responder normally prefers its own
configured order. For XAuth Aggressive Mode, the initial DH exchange uses the
first configured IKE DH group; put the gateway's required group first rather
than expecting later groups to provide automatic fallback. See the pinned
[Aggressive Mode exchange](https://github.com/strongswan/strongswan/blob/5.9.14/src/libcharon/sa/ikev1/tasks/aggressive_mode.c)
and [DH selection](https://github.com/strongswan/strongswan/blob/5.9.14/src/libcharon/config/ike_cfg.c).

### Engine defaults and strict lists

The invocation matters; the two backends do not interpret `!` identically:

- **XAuth:** the managed wrapper passes each entry as a repeated
  `charon-cmd --ike-proposal` or `--esp-proposal` option with
  `--profile ikev1-xauth-psk-am`. In the pinned strongSwan 5.9.14 client,
  nonempty option lists replace the engine defaults too. vpnmodo always supplies
  both lists, using the profile defaults above if needed, so the client does
  **not** append extra default proposals. Do not add `!`: although the managed
  profile validator accepts it, `charon-cmd` passes it to the algorithm parser
  and rejects it. See the [pinned client implementation](https://github.com/strongswan/strongswan/blob/5.9.14/src/charon-cmd/cmd/cmd_connection.c).
- **L2TP:** the entrypoint joins each array with commas and writes it directly
  to `ike=` or `esp=` in `ipsec.conf`, then uses `ipsec start` / `ipsec up`.
  strongSwan's stroke configuration appends engine defaults unless the **whole
  comma-separated list ends in `!`**. This applies even when you supply an
  explicit profile list. Engine defaults are separate from vpnmodo's table and
  depend on the installed strongSwan version and crypto support; the backend
  installs Ubuntu 24.04's strongSwan package. To restrict either list, append
  `!` only to its final array entry. For example,
  `"ike_proposals": ["aes256-sha256-modp2048!"]` and
  `"esp_proposals": ["aes256-sha256!"]` suppress extra defaults for both.
  With multiple entries use `["aes256-sha256", "aes128-sha256!"]`, not an
  exclamation mark on every entry. See the [stroke parser](https://github.com/strongswan/strongswan/blob/5.9.13/src/libcharon/plugins/stroke/stroke_config.c)
  and [ipsec.conf reference](https://raw.githubusercontent.com/strongswan/strongswan/5.9.14/man/ipsec.conf.5.in).

The local XAuth patches do not change proposal parsing or default selection.
For the runtime mapping, see [profile defaults and validation](../internal/profile/profile.go),
the [managed manifest](../internal/managedvpn/manifest.go), the
[XAuth invocation](../containers/ipsec-xauth/managed/vpn-xauth-expect) and the
[L2TP entrypoint](../containers/l2tp-ipsec/scripts/docker-entrypoint.sh).
