# IPsec/XAuth backend

Select `backend.type: "ipsec-xauth"` in a vpnmodo JSON profile. This backend
uses IKEv1 Aggressive Mode with PSK/XAuth and interactive emailed MFA. See the
[profile example](../../examples/profiles/v1/example-xauth.json) and
[profile commands](../../docs/onboarding.md).

`backend.config.ike_proposals` and `esp_proposals` override the
[proposal defaults](../../docs/profiles.md#ike-and-esp-proposals). The pinned
`charon-cmd` uses the supplied lists without appending engine defaults and does
not accept the `ipsec.conf` trailing `!`. For EAP-MSCHAPv2 without emailed MFA,
see the separate [IKEv2 backend](../ipsec-ikev2/README.md).

## Runtime and authentication

The image builds strongSwan 5.9.14 on Alpine 3.22. Local patches support
`XAUTH_PASSCODE`, protect credential delivery, mark encrypted traffic for the
namespace firewall and disable unattended IKEv1 reauthentication.
`vpn-managed-xauth` configures enforcement and starts the Expect adapter.
The controller supplies protected credential mounts and delivers each MFA code
through stdin to a private temporary file. Raw client transcripts are discarded.

Use `vpnmodo up PROFILE` to connect and answer the hidden MFA prompt. The
controller verifies tunnel enforcement and a private TCP endpoint before
attaching the unprivileged vpntools container. After tunnel loss, run an
explicit ensure with a fresh code; XAuth does not start unattended MFA logins.
See [authentication and enforcement](../../docs/design/006-managed-xauth.md)
for the state machine, DNS behavior and recovery rules.

## Build

From the repository root, on a native Linux Docker builder:

```sh
docker build -t vpnmodo-xauth:dev containers/ipsec-xauth
```

Activate backend images through the controller setup or upgrade workflow.
Building an image does not replace an active VPN session.

## Failure diagnostics

Use `vpnmodo logs PROFILE` and `vpnmodo doctor PROFILE`. Fixed exit reasons are:
20 repeated/unexpected PSK prompt; 21 password prompt; 22 PIN prompt;
23 initial login timeout; 24 MFA input timeout; 25 timeout after MFA submission;
26 client exit before submission; 27 client exit after submission;
28 invalid MFA input; 29 invalid mounted credentials;
30 establishment before required MFA; 31 established client exit.
Exit 2 is a generic authentication failure. A reason identifies a stage; it
does not prove the code was incorrect. Diagnose repeated prompts or client exits
before starting another attempt. See [known issues](../../docs/known-issues.md).
