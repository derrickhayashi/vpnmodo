# Experimental GlobalProtect backend

`openconnect-globalprotect` uses OpenConnect's `gp` protocol in the separate
`vpnmodo-globalprotect` Linux image. It plugs into the existing profile vault,
controller operations, private-service access and bounded recovery policy.
Use a release that includes the GlobalProtect image. Compatibility depends on
the gateway and its authentication and endpoint-compliance policy.

## Connection scope

The backend connects directly to a single **GlobalProtect
gateway**, using username/password. Certificate verification is enabled by
default and requires an explicit trusted CA bundle. A profile can explicitly
disable verification when no bundle is available (see below). The
gateway must allow direct authentication without a preceding portal login.
Only TCP 443 and UDP 4501 to one pinned IPv4 peer are permitted outside the
tunnel. OpenConnect attempts ESP when offered and can fall back to HTTPS.

The following need separate integration and validation:

- Portal login, gateway discovery, multiple gateways and redirected peers.
- Browser SAML/SSO, interactive MFA, client certificates and cookie import.
- HIP/device-compliance report generation. No report generator or device
  impersonation is configured; gateway policy can deny or limit access.
- IPv6, custom gateway ports and server-pushed split routes or DNS.

The runtime uses `--non-inter` and closes password stdin after one value. It
does not advertise external-browser authentication. Unsupported authentication
does not trigger an interactive prompt. A pre-tunnel client exit or startup
timeout stops automatic login retries; fix the configuration and explicitly
ensure again. The raw client transcript is discarded, so these exits are
reported generically rather than claiming a precise certificate/auth diagnosis.

See upstream [GlobalProtect protocol support](https://www.infradead.org/openconnect/globalprotect.html)
and the [OpenConnect option reference](https://www.infradead.org/openconnect/manual.html).

## Profile and credentials

Start with the [synthetic profile](../examples/profiles/v1/example-globalprotect.json).
Keep the actual import file, credentials and CA bundle outside the checkout.
`backend.config` accepts only `server` (hostname or IPv4) and `dns` (up to eight
IPv4 resolvers). Both DNS and the top-level `health` endpoint are optional.
Omit `health` to connect with tunnel and packet-isolation checks alone. When
supplied, it names a private TCP service and its probe must pass before access
is ready; a hostname health target requires explicit VPN DNS.

For an input directory already containing private `credentials.json` with
`username`/`password` and an approved `ca.crt`, create the combined import file:

```sh
umask 077
python3 - <<'PY'
import json
from pathlib import Path
private = Path.home() / 'vpnmodo-private'
profile = json.loads(Path('examples/profiles/v1/example-globalprotect.json').read_text())
credentials = json.loads((private / 'credentials.json').read_text())
credentials['ca_cert'] = (private / 'ca.crt').read_text()
profile['credentials'] = credentials
path = private / 'globalprotect.json'
path.write_text(json.dumps(profile) + '\n')
path.chmod(0o600)
PY
```

Replace the synthetic server, DNS and health values in the private file. With
a matching CLI, controller and backend installed:

```sh
vpnmodo profile create example-globalprotect --file "$HOME/vpnmodo-private/globalprotect.json"
vpnmodo connection ensure example-globalprotect --wait
vpnmodo connection status example-globalprotect
vpnmodo connection disconnect example-globalprotect --wait
```

Credentials require `username` and `password`; PSKs are rejected. The default
verification mode additionally requires `ca_cert`.
The CA contract matches IKEv2: 1–16 X.509 CA certificates, at most 32 KiB,
with no private keys or other PEM content. OpenConnect checks the server
certificate against the configured hostname/IP and uses only this CA bundle
(`--no-system-trust`). CA/password changes
use the normal inactive-profile credential rotation workflow.

### Accept any server certificate

To connect without a CA bundle, explicitly set this in `backend.config`:

```json
{
  "server": "203.0.113.10",
  "insecure_skip_tls_verify": true
}
```

Omit `credentials.ca_cert` in this mode. An IP address is sufficient and no
matching certificate hostname is required. This disables certificate chain,
expiry and hostname verification; the connection is encrypted but the server's
identity is not authenticated. Omitting the flag or setting it to `false`
restores the default CA requirement. A failed verification never automatically
switches to this mode. Other VPN backends retain their existing trust contracts.

OpenConnect's packaged CLI has no general no-verify switch. For each explicitly
insecure attempt, the runtime opens a bounded TLS connection to the pinned peer
without verification, obtains its current public-key fingerprint, and supplies
that fingerprint to OpenConnect through the protected config file. No login data
is sent during this preliminary connection. This is an implementation mechanism
for accepting the current certificate, not a trusted or persistent pin. Each
fresh attempt accepts the then-current certificate again. A different key
presented between the preliminary connection and login causes that attempt to
fail. Packet filtering and credential handling remain active in both modes.

## Isolation and lifecycle

The controller resolves a gateway hostname once using a disposable unprivileged
container without vault mounts. Each connection attempt pins that IPv4 while
preserving the original hostname for TLS verification/SNI. No bootstrap DNS is
allowed in the protected namespace.

The backend installs mandatory IPv4/IPv6 filtering before reading credentials
or launching OpenConnect. Root-owned sockets may contact only the pinned peer
on TCP 443 and UDP 4501 through `eth0`. Applications run as UID 65534 without
capabilities and can transmit only through `vpn0` or localhost. Docker's
embedded resolver is blocked. There is no general established-flow exception
on the outer interface, so existing connections cannot escape after TUN loss.

The fixed network hook validates the assigned IPv4 and interface, configures a
`vpn0` default route, and ignores pushed DNS/split routes. The peer retains an
explicit outer host route. The inner MTU defaults to 1400 bytes, including
when a gateway omits MTU and the VM network advertises a 64-KiB outer MTU. The
optional top-level `network.mtu` overrides that default (576–1500); the TUN
interface and its routes use that limit and TCP MSS derived as MTU minus 40.
OpenConnect may choose a lower effective MTU, which status reports with its
corresponding MSS.
A loopback dnsmasq forwards only to the configured
resolvers through `vpn0`; an empty DNS list has no upstream fallback.

Readiness requires the live OpenConnect process, assigned TUN address, expected
routes and unchanged packet rules. When `health` is configured, application
attachment additionally requires its private TCP probe. Without it, status
reports that the private endpoint was not configured or verified. The packet rules survive process
loss while any application retains the namespace.

Startup is limited to 120 seconds. OpenConnect's transport reconnect window is
one second; a reconnect/disconnect hook invalidates readiness and ends the
attempt. The controller owns fresh login attempts and uses its existing maximum
of three consecutive recoveries and cooldown. Service unavailability alone
does not request a fresh login. Cancellation/disconnect removes applications
before disposing the VPN namespace and per-attempt credential files.

Credentials enter OpenConnect through a root-only temporary config and password
stdin, never command arguments. The root filesystem is read-only; `/run` and
`/tmp` are temporary. Cookies stay within the client process. Docker logging is
disabled and diagnostics use the existing fixed reason codes.

## Build and validation

On an existing authorized native Linux Docker builder:

```sh
docker build -t vpnmodo-globalprotect:dev containers/openconnect-globalprotect
docker build -t vpnmodo-controller:dev -f containers/controller/Dockerfile .
```

The kernel must provide TUN, netfilter owner matching and IPv6-disable sysctls.
The controller never builds/pulls images or provisions kernel devices during
connection. Building images does not replace a running controller; activate the
development controller through the existing explicit replacement workflow.
`VPNMODO_GLOBALPROTECT_IMAGE` selects the operator-provisioned image.
Release builds include the `globalprotect` image role and immutable identities;
old four/five-image release manifests remain accepted.

Run Go race tests/vet, Python tests and the secret scanner as described in
[development](development.md). Runtime/configuration regressions are in
[test_globalprotect.py](../tests/test_globalprotect.py); Go tests cover schema,
vault, manifest, cancellation, application gates and permanent login failure.

For the actual OpenConnect binary and Linux packet checks, explicitly select
an authorized local Linux Docker engine, build the backend above, then run:

```sh
VPNMODO_ISOLATED_TEST_HOST=1 python3 tests/globalprotect_acceptance.py
```

The harness creates only uniquely named disposable containers and a network,
generates certificates outside source, and removes its resources. It supplies
no host Docker socket, host network, real profiles or credentials to test
containers. The synthetic gateway implements GP login and HTTPS framing, not
Palo Alto's full product or ESP. It checks certificate policy, optional private
health, MTU/MSS, failed logins, private TCP/DNS and packet enforcement after
tunnel loss. Successful synthetic tests do not establish interoperability with
every Palo Alto gateway or compliance policy.
