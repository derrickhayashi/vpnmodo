# Private candidate validation — 2026-10-19

Tested source: `728b9a4b3a97c74df2b16ba7d58d7935b6ef331f`.
Candidate binary identity: `v0.1.0-rc.1+git.728b9a4b3a97`.
Subsequent edits in this PR document the results and clarify installation steps.
No release was published and no customer deployment was adopted.

| Target | Evidence | Remaining |
| --- | --- | --- |
| macOS arm64 | Native archive installation/version/help; explicit replacement; checksum/symlink refusal; temporary-prefix cleanup | Final adoption workflow |
| macOS amd64 | Cross-built archive and checksum | Native runtime acceptance |
| Ubuntu 24.04 arm64 | Full production Dockerfile build; verified offline image load; real L2TP and XAuth onboarding, two-hop forwarding, controller replacement and disposal | Legacy VPN adoption/cutover/rollback |
| Ubuntu 24.04 amd64 | Controller binary cross-build | Native image build and runtime acceptance |

The image bundle's Docker archive contains content IDs without repository tags.
Loading it preserves deliberately conflicting pre-existing tags. The loader
verifies archive and image configuration hashes, architecture, version, and
controller backend/application IDs. Its production build and loader passed.

The isolated onboarding checks use the packaged production controller/backend
images and their normal operation/MFA/recovery settings. L2TP authentication,
PPP, private TCP/DNS and named service forwarding passed. XAuth authentication,
challenge-bound code consumption, pre-login packet enforcement, private TCP/DNS,
structured JSON MFA and named service forwarding passed. Established sessions
and private access survive explicit controller replacement. The rehearsal
controller has a different binary version and different backend/application
image IDs (test-only labels distinguish otherwise unchanged backend code).
Existing sessions retain their saved IDs; later connections use the new defaults.

The first XAuth invocation failed before authentication because its synthetic
pre-login guard expected the `vpnmodo-xauth:dev` fixture alias. Image inspection
confirmed it was absent. Pointing that alias at the verified release image
satisfied the prerequisite; the repeated XAuth onboarding/upgrade run exited 0.
The L2TP invocation had already completed successfully. This was a fixture
prerequisite failure, not evidence of a VPN authentication failure.

Go race tests and vet pass. All 76 Python tests pass, including source-inventory,
checksum, architecture, image identity and legacy-tag refusal cases. The native
Mac installation changes only a temporary prefix. Both Mac archives, the source
archive and the ARM64 VM bundle have verified SHA-256 checksums. Full history and
working-tree secret scans pass; distribution artifacts and raw synthetic evidence
remain outside Git. GitHub Actions remains disabled and the exact disposable lab
and remote bundle were removed after verifying no nested test containers remained.

These onboarding checks are a subset, not a replacement for the previous full
[packet/rekey/recovery evidence](../tests/local-lab/README.md). Issue #9 stays open
for explicit legacy adoption/rollback and the remaining platform release gate.
The unconfirmed startup observation #19 remains open after its separate 20-pass
replacement investigation; no backend fix was claimed for it.

Native macOS terminal acceptance also runs `python3 tests/smoke_terminal.py`.
This uses synthetic input on a controlling PTY to verify idle waiting, hidden
MFA/credential input, cancellation and restored terminal settings. Version/help
checks alone do not exercise these prompts.

## Native onboarding follow-up: Docker stores and hidden input

Revision `8deb0974a931` includes explicit noninteractive-sudo host registration,
the schema-2 portable image loader/installed receipt, a stable public SSH proxy,
and native hidden-terminal input fixes. The portable loader was exercised against
Docker 29.1.3 with its containerd image store using the existing verified ARM64
production image content. All four destination-local IDs resolved and the
controller binary identity matched. Classic-store ID resolution and reserved-tag
collision refusal have unit coverage; a fresh complete native classic-store
bundle build/load remains to be repeated for the schema-2 format.

The native macOS terminal harness passed hidden credential/MFA input, waiting,
cancellation and terminal restoration. Local Go race tests/vet and 84 Python
tests passed. Synthetic Expect replay distinguishes repeated PIN, client exit
after MFA, post-MFA timeout and an established idle session. The controller also
preserves authentication classification when a concurrent health exec is killed
as the VPN process exits.

A native ARM64 functional trial used these compiled controller changes and the
updated Expect script layered on the previously verified production base images.
It completed managed XAuth login and private TCP checks, then authenticated SSH
through the public proxy. A protected-route MTU/MSS workaround was required for
the larger SSH exchange; lowering only the namespace interface MTU did not help.
Follow-up isolation reproduced the stall with synthetic ESP/NAT-T across a
1280-byte bottleneck: the managed INPUT filter dropped related ICMP
fragmentation-needed errors. A narrow RELATED/type-3-code-4 exception restores
256 KiB transfers each way without route MTU/MSS overrides. Unrelated ICMP and
plaintext/post-policy probes remain blocked. A fresh native ARM64 trial login
with this correction also passes authenticated SSH and 256 KiB uploads/downloads
on both configured targets, with a 1500-byte container interface and no route
MTU/MSS override. Issue #21 retains the authenticated rekey application gate.
The focused packet fixture passes; the full protocol/lifetime suite has not
been rerun for this correction. These
patched trial images are not a new full production-Dockerfile release bundle.

PR #20 remains draft. Issue #9 stays open for #21, fresh schema-2 bundle acceptance,
legacy cutover/rollback with fresh MFA, and the remaining native amd64 matrix.
No raw deployment settings, credentials, transcripts or packet metadata belong
in this record. GitHub Actions remains disabled.

One preceding fresh trial attempt exited after MFA with fixed diagnostic 27.
The explicitly authorized next attempt succeeded. That diagnostic identifies
the stage, not the peer-side cause or code validity. Earlier spontaneous loss of
kernel IPsec state remains unexplained; short application success does not
establish that the PMTU correction addresses session lifetime.
