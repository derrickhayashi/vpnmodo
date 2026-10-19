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
