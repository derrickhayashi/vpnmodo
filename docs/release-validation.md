# ARM64 release validation

This record lists completed checks by release and platform. Historical source
references identify tree-equivalent commits in the rewritten repository; old
release binaries and container images are not republished. Results apply to
the recorded source, not automatically to newly rebuilt artifacts. For symptoms, affected versions and recovery steps,
see [known issues and regressions](known-issues.md).

## XAuth delayed-MFA handshake timeout — 2026-10-20

Qualified CLI: `v0.1.0-rc.12+git.68b20091f975`.

The managed XAuth entrypoint sets `charon-cmd.half_open_timeout = 300`, matching
the legacy launcher. Its omission left the aggressive-mode IKE handshake on
strongSwan's 30-second default, shorter than the controller's 90-second MFA
challenge. The controller deadline is unchanged.

The focused real-protocol regression uses the synthetic XAuth/RADIUS gateway.
The responder timeout remains 300 seconds in every trial.

| Trial | Result |
| --- | --- |
| Shipped rc.7 image, immediate synthetic MFA | Tunnel establishment, enforcement checks and TCP echo pass |
| Same image, MFA delayed 40 seconds | Reproduces exit 27 before CHILD_SA establishment |
| Same image, only corrected entrypoint mounted, same 40-second delay | Tunnel establishment, enforcement checks and TCP echo pass |
| Rebuilt fixed image, same 40-second delay | Tunnel establishment, enforcement checks and TCP echo pass |

The one-setting control confirms the client handshake timer causes this delayed
MFA failure. Diagnostic 27 alone does not mean the supplied code was incorrect.
The released image's file contents and permissions match the tested rebuild;
only build timestamps/version labels differ. Existing XAuth reason tests and
managed runtime/lifecycle Go tests also pass.

The regression is [`tests/xauth_handshake_timeout.py`](../tests/xauth_handshake_timeout.py).
It accepts explicit old and fixed images plus the corrected entrypoint and
requires a delay above 30 seconds. This focused check does not requalify unrelated
protocol or lifecycle scenarios.

The ARM64 XAuth image is pinned to
`sha256:f8a578da17a5037d5a699fc40a79a3b1a97e0243e381288f197511419ecd796f`.
Controller, L2TP and application images retain the rc.7 identities. See the
[registry manifest](../examples/lima/registry-release.json); its bundle version
remains rc.7. VM restart checks confirm that the saved manifest and controller
select the corrected image. Native Mac archive/standalone installation smoke
checks pass. Separate functional acceptance confirmed structured MFA completion,
VPN readiness and authenticated destination SSH access.

## CLI and managed VM qualification — 2026-10-20

| Release and source | Completed checks | Limits at that release |
| --- | --- | --- |
| rc.11, `0a6f183bf1cf` | Optional separate registry-auth input, anonymous provisioner/retry tests, saved-auth reuse/replacement, strict credential input, native ARM64 artifact installation and full Go race/vet checks | Anonymous pulls were tested locally through provisioner fixtures; no real anonymous registry pull was performed. Runtime images remained rc.7. |
| rc.10, `96efe3350ede` | One-file VPN settings/credentials for both backends; create/update/rotation RPC tests; owner-only files and secret-free output; native ARM64 artifact checks; 77 Python tests and Go race/vet checks | Historical same-file registry input was replaced by rc.11's separate `--registry-auth`. This input-only release did not rerun protocol acceptance. |
| rc.9, `b0040e7df805` | Single managed `vpn-vm`, JSON profile creation, controller readiness, native artifact/setup checks, 77 Python tests and Go race/vet checks | A connection attempt exited after MFA with diagnostic 27; private access was not verified in that check. This was not an end-to-end pass. |
| rc.8, `6328d9b9af16` | Fresh Lima setup, Docker/kernel preparation, controller readiness, cold restart/retry, missing-image and registry-auth recovery, native ARM64 artifacts, 83 Python tests and Go race/vet checks | Lima was already installed; automatic Homebrew installation and native Intel execution were not exercised. VPN authentication was not repeated for this CLI change. |

A fresh rc.11 managed-VM walkthrough also verified VM creation and noninteractive
JSON profile import. Authentication results depended on the interaction path;
the delayed-MFA check above subsequently reproduced and corrected the handshake
timeout. The earlier failures remain part of the qualification record and do not
establish that every diagnostic 27 has the same cause.

Registry input validation rejects malformed or duplicate fields and exposed
file permissions without disclosing values. Setup keeps registry authentication
separate from VPN configuration. Earlier setup qualification confirmed that root
Docker auth stayed mode 0600 and that the supplied token and its base64 encoding
were absent from VM logs. Secret scans passed for these candidates.

For current commands, follow [installation](../README.md#installation) and
[profile maintenance](onboarding.md), rather than historical CLI interfaces in
older releases.

## Earlier rc.6 protocol qualification — 2026-10-19

Validated source: `4e1376ac5b31d4330041e5b61d0ad20c63bb8428`.
Candidate identity: `v0.1.0-rc.6+git.990dc2fe7cae`.
Subsequent documentation commits record these results without changing runtime
code. The complete isolated protocol/recovery/packet suite and a fresh ARM64
release bundle built from production Dockerfiles passed. This qualification did
not publish registry images or release artifacts.

### Qualified scope and results

Qualification covers macOS arm64 and Ubuntu 24.04 arm64. Intel/amd64 native
testing and legacy rollback testing are outside this release qualification scope.
Cross-built amd64 CLI artifacts have checksums but no native runtime
qualification.

| Check | Result |
| --- | --- |
| Native Mac ARM64 CLI | Archive checksums, install, version/help, explicit replacement, corrupt/symlink refusal and temporary-prefix cleanup pass |
| Hidden terminal input | Credential/MFA waiting, echo suppression, cancellation and terminal restoration pass |
| Full isolated Linux suite | Controller/profile/operation, L2TP, XAuth and PMTU suites complete with exit 0 |
| XAuth authenticated CHILD rekey | Replacement CHILD_SA confirmed; 256 KiB each way passes before/after rekey with no route MTU/MSS override and zero observed plaintext |
| XAuth controller replacement | VPN retained; 256 KiB each way and private service access pass before explicit disconnect |
| Fresh container image bundle | Production L2TP, XAuth and controller Dockerfile builds, pinned image IDs, source inventory and SHA-256 verification pass |
| Classic Docker image store | Docker 29.1.3, vfs, ARM64: schema-2 bundle import and immutable-ID receipt pass; development image tags remain unchanged |
| Containerd image store | Docker 29.1.3, ARM64: checksum-verified import and repeat import pass; receipts verify all four images/version and report activation false |
| Packaged onboarding/upgrade | Production-policy L2TP and XAuth authentication, private TCP/DNS, two-hop service access, credential disposal and controller upgrade pass; final exit 0 |
| Local checks | Go race tests, vet and all 84 Python tests pass |

The complete suite includes endpoint outage, namespace repair, automatic L2TP
recovery and failed recovery, graceful stop and three fresh reconnects. XAuth
covers aggressive PSK/XAUTH_PASSCODE, a 95-second quiet session, expired,
incorrect and repeated MFA, controller crash, DNS/IPv6 bypass refusal and SO_MARK
forgery refusal. Captures require zero plaintext when SAs/policies disappear
while the client and controller are suspended; reachable positive controls prove
the observer and escape paths work.

The separate PMTU fixture uses fresh ESP/NAT-T peers across a routed 1280-byte
bottleneck. The uncorrected filter drops 16 related fragmentation-needed errors
and times out. The narrow ICMP correction permits 256 KiB each way without route
MTU/MSS overrides. Unrelated forged ICMP and protected plaintext/post-policy
probes remain blocked; all five scenarios pass.

The bundle onboarding subset uses production operation/recovery settings. Its
upgrade rehearsal changes the controller binary identity and backend/application
image IDs while preserving the established VPN and service access. Full failure
and packet acceptance use a test controller with shortened lab-only timings;
the suites provide complementary evidence.

The first extended-suite attempt checked a payload after deliberate service
teardown. Moving that probe before teardown corrected the fixture ordering; the
complete committed-source rerun exits 0.

## Known limitations

The delayed-MFA timeout above explains a reproducible cause of diagnostic 27.
Other post-MFA exits and disappearance of established kernel IPsec state are
tracked in [known XAuth issues](known-issues.md#xauth-session-state-loss).
These checks do not establish long-term session stability or resolve unrelated
lifecycle observations.

An intermittent synthetic L2TP startup exit during manual replacement remains
tracked in [known L2TP issues](known-issues.md#intermittent-l2tp-startup-exit-in-the-test-lab).
Its original cause was not confirmed by later successful full-suite runs.

The published protocol results use synthetic fixtures and do not
certify every gateway, MFA method or deployment configuration.

## Import isolation and cleanup

Both image-store imports verify immutable IDs and leave activation false. The
import checks preserve pre-existing named image tags; immediate before/after
containerd import snapshots show unchanged container identities, running/stopped
states and start times. These bounded checks do not assert that unrelated
resources were unchanged over the entire qualification window.

After the suites pass, the nested daemon has no remaining acceptance containers;
the outer lab and staging directory are removed. Candidate archives, receipts
and raw synthetic evidence are kept outside source. Customer settings,
credentials, transcripts and packet metadata are excluded from source and issue
artifacts.
