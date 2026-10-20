# ARM64 release validation

## Single managed VM flow — 2026-10-20

The pending rc.9 CLI removes alternate VM registration and selection. It uses
only the managed `vpn-vm` and keeps VPN configuration in JSON files. The
controller and image digests remain the published rc.7 runtime.

| Check | Result |
| --- | --- |
| Default VM setup | `vpnmodo setup` succeeds on the workstation's managed Lima VM and reports controller readiness |
| JSON profile creation | Creating a profile from a private JSON file succeeds on that VM; stored credentials are present |
| Real VPN connection | Reaches the MFA prompt; after the fresh code is submitted, authentication fails and the client exits with diagnostic 27 |
| Private access | Not verified because the VPN connection failed |
| Local checks | Full Go race tests, vet, 77 Python tests and secret scans pass |

The failed connection is not an end-to-end pass. Retained diagnostics do not
identify the underlying peer/client reason. The older qualification below
remains historical evidence, not proof that this managed-VM connection works.
No automatic authentication retry was performed.

## Single-file setup — 2026-10-20

CLI source: `43e5e02d22fdb428e85aa4da28cc32658df71ec3`.
CLI identity: `v0.1.0-rc.8+git.43e5e02d22fd`.
Embedded runtime: `v0.1.0-rc.7+git.4696f8c7ec50`, using the previously published
private GHCR image digests. This workstation-only release does not rebuild the
controller images. Subsequent documentation commits record these results.

| Check | Result |
| --- | --- |
| Fresh local setup | One CLI executable creates an isolated Lima Ubuntu ARM64 VM, provisions Docker/kernel support, pulls private images, registers SSH and starts the controller |
| Readiness | Setup returns only after controller storage, Docker and runtime report ready; version matches the installed release |
| Restart/retry | Cold VM restart and repeated setup preserve the controller container ID and a single host registration |
| Missing image/auth refresh | Removed the application image and guest registry auth; repeated setup refreshes auth from the protected local file and pulls the private image |
| Credential handling | Root Docker auth remains mode 0600; supplied token and its base64 encoding are absent from Lima VM logs |
| Exact release artifacts | Native ARM64 standalone binary matches archive metadata/hash, reports the recorded version, exposes setup help and completes setup against the test VM; archive installation/replacement/corruption checks pass |
| Local checks | Full Go race tests, vet, 83 Python tests, focused setup recovery tests, documentation links/shell syntax and secret scans pass |

The private image credentials were supplied through a protected test file;
hidden prompting uses the existing terminal-input implementation. Lima was
already available in the isolated test; automatic Homebrew installation was not
exercised. Actual VPN authentication and private destination access were not
repeated for this CLI change. Intel native qualification remains outside scope.
All VMs used local resources; GitHub Actions remains disabled.

## Earlier rc.6 protocol qualification — 2026-10-19


Validated source: `990dc2fe7caec4e1b68cf2b54788b996937e66c5`.
Candidate identity: `v0.1.0-rc.6+git.990dc2fe7cae`.
Subsequent documentation commits record these results without changing runtime code.

Both agreed validation gates pass: the complete isolated protocol/recovery/packet
suite, and a fresh ARM64 release bundle built from the production Dockerfiles.
That rc.6 qualification run kept the candidate private and did not publish a
registry image, GitHub release or public artifact. GitHub Actions remains disabled; all work used the existing Mac and VM.

## Qualified scope and results

Qualification covers macOS arm64 and Ubuntu 24.04 arm64. Intel/amd64 native testing
and legacy rollback testing are outside the agreed release scope. Cross-built
amd64 CLI artifacts have checksums but no native runtime qualification.

| Check | Result |
| --- | --- |
| Native Mac ARM64 CLI | Fresh archive checksums, install, version/help, explicit replacement, corrupt/symlink refusal and temporary-prefix cleanup pass |
| Hidden terminal input | Native credential/MFA waiting, echo suppression, cancellation and terminal restoration pass |
| Full isolated Linux suite | Controller/profile/operation, L2TP, XAuth and PMTU suites complete with exit 0 |
| XAuth authenticated CHILD rekey | Replacement CHILD_SA confirmed; 256 KiB each way passes before/after rekey with no route MTU/MSS override and zero observed plaintext |
| XAuth controller replacement | Same VPN retained; 256 KiB each way and private service access pass before explicit disconnect |
| Fresh container image bundle | Full production L2TP, XAuth and controller Dockerfile builds, pinned backend/application IDs, source inventory and SHA-256 verification pass |
| Classic Docker image store | Docker 29.1.3, vfs, ARM64: schema-2 bundle import and immutable-ID receipt pass; existing development image tags remain unchanged |
| Containerd image store | Docker 29.1.3, ARM64: checksum-verified import and repeat import pass; receipts verify all four images/version and report activation false |
| Packaged onboarding/upgrade | Production-policy L2TP and XAuth authentication, private TCP/DNS, two-hop service access, credential disposal and controller upgrade pass; final exit 0 |
| Local checks | Go race tests, vet and all 84 Python tests pass |

The complete suite includes endpoint outage, namespace repair, automatic L2TP
recovery and failed recovery, graceful stop and three fresh reconnects. XAuth
covers real aggressive PSK/XAUTH_PASSCODE, a 95-second quiet session, expired,
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
and packet acceptance use the separate test controller with shortened lab-only
timings; the two suites provide complementary evidence.

## Adoption and known observations

The earlier native ARM64 functional trial completed real adoption/cutover:
existing settings became a managed profile, credentials remained on the VM, the
legacy session was stopped and retained, and the existing workstation wrapper
and SSH aliases selected the managed application. Fresh MFA, authenticated SSH
and 256 KiB uploads/downloads passed on both configured targets with interface
MTU 1500 and no route MTU/MSS override. That trial used reviewed changes layered
on verified base images; the newly validated bundle above is a fresh full build.
Repeating real adoption/cutover or rehearsing legacy rollback is not a release gate.

The intermittent post-MFA client exit (fixed diagnostic 27) and disappearance of
established kernel IPsec state still have unconfirmed causes and are tracked in
[issue #23](https://github.com/derrickhayashi/vpn-sidecars-next/issues/23).
These successes do not establish long-term stability or resolve that follow-up.
The earlier unconfirmed synthetic L2TP startup observation remains tracked in
[issue #19](https://github.com/derrickhayashi/vpn-sidecars-next/issues/19).

The first attempt at the extended full suite reached deliberate service teardown
before a new payload probe, so the probe found no VPN. This was a fixture ordering
mistake: the service test had explicitly disconnected it. Moving the probe before
teardown corrected the test; the complete committed-source rerun exits 0.

## Import isolation and cleanup

Both Docker image-store imports verify immutable IDs and leave activation false.
Every pre-existing named image tag is preserved. Immediate before/after snapshots
around repeated containerd import show unchanged container IDs, running/stopped
states and start times. The existing managed XAuth VPN/controller retain their
identities and start times throughout the run.

An independent watchdog-managed legacy VPN was recreated during the longer test
window, before the image import. Lifecycle timestamps distinguish that event
from import; the whole-run snapshot is not claimed to show every unrelated
container unchanged. No unrelated configuration or VPN logs were read.

After both suites pass, the nested daemon has no remaining acceptance containers.
The exact outer lab and VM staging directory are removed. Verified candidate
archives, receipts and raw synthetic evidence remain privately outside source.
Customer settings, credentials, transcripts and packet metadata are excluded
from this repository and issue artifacts.
