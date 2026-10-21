# Supported platforms and limitations

vpnmodo targets an Apple Silicon Mac with one managed Ubuntu 24.04 ARM64 Lima
VM. The Mac runs the CLI and OpenSSH; the VM runs Docker, the controller and
per-profile VPN/application containers. Start with [installation](../README.md#installation).

The project is a prerelease. Intel/AMD64 binaries can be cross-built, but native
AMD64 operation is not qualified. A successful connection to one gateway does
not establish compatibility with every gateway or authentication policy.

## VPN backends

| Backend | Authentication | Address family | Recovery |
| --- | --- | --- | --- |
| L2TP/IPsec | Username, password and PSK | IPv4 | Bounded controller recovery |
| IPsec/XAuth | PSK/XAuth and emailed-code MFA | IPv4 | Fresh login requires an explicit command and a new code |
| IKEv2 | EAP-MSCHAPv2 and CA-verified server certificate | IPv4 | Bounded controller recovery; permanent authentication failures stop retries |

The published rc.13 release supplies L2TP/IPsec and XAuth. IKEv2 requires images
built from source; follow the [IKEv2 guide](ikev2.md) for trust, proposal and
networking requirements. Backend selection does not change the Mac's default
route: private access goes through the profile's application namespace.

## Operational limits

Long-duration stability and interoperability depend on the gateway, network
path and policy. IKEv2 IKE SA rekey/reauthentication, large transfers/PMTU and
cross-version runtime upgrades need further qualification. See
[known issues](known-issues.md) for symptoms and recovery steps, and the
[IKEv2 limitations](ikev2.md#limitations) for backend-specific scope.

Keep private profiles, credentials, state and packet captures outside the source
checkout. Use [logs](logging.md) and `vpnmodo doctor PROFILE` to distinguish a
VM/controller failure, tunnel loss and an unavailable private service.
