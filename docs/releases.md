# Private release artifacts

Issue #9 supplies an offline distribution path. ARM64 candidate qualification
passes as recorded in the [validation record](release-validation.md). These are
private release candidates. Builds stay local by default; `vm --publish` uploads
release images to private GHCR. The standalone CLI is distributed through a private GitHub prerelease; there is no Homebrew tap. GitHub Actions stays disabled. Build and test on the
workstation and authorized local VM.

## Single-file CLI download

The current standalone CLI release is `v0.1.0-rc.11`. Download `vpnmodo-darwin-arm64`
from the authenticated private release page, install it and run `vpnmodo setup`. For private images supply the optional
`--registry-auth FILE` described in [installation](../README.md#installation).
The supported environment is one Apple Silicon Mac with one Lima VM managed by
vpnmodo. Setup prepares the controller. VPN settings remain in JSON profiles, which
automatically use the managed VM. There is no host selection, external VM setup
or manual provisioning-file transfer.

The executable includes the Lima YAML, VM installer and registry manifest. It
provisions the existing verified `v0.1.0-rc.7+git.4696f8c7ec50` runtime images;
CLI and runtime versions are recorded separately. This CLI change does not
require rebuilding those images.

`SHA256SUMS-cli` covers the executable. macOS binaries are unsigned and not
notarized; follow your team's approved software policy. No installer changes
Gatekeeper settings. Existing source and image archives remain maintainer build
artifacts, rather than another supported user installation flow.

## Artifacts and trust

The supported user artifact is the standalone executable. It embeds the
[Lima provisioning inputs](lima.md) and pulls the controller, L2TP, XAuth and
application images into the managed VM. Container/source archives are retained
as maintainer build artifacts.

A release has macOS arm64/amd64 CLI archives, a committed source archive, and a
separate Linux arm64 or amd64 container image bundle. Each artifact group has SHA-256 sums.
Obtain both artifacts and their expected checksums through the team's trusted,
authenticated distribution channel. A checksum detects a changed download; a
checksum delivered with an untrusted replacement does not authenticate it.

CLI archives contain `vpnmodo`, `install.sh`, `release.json` and a short README.
Container image bundles contain `images.tar`, `install-vm.py`, `release.json` and a README.
The source version, full Git revision, target architecture and binary version are
recorded in each manifest. `vpnmodo version` reports, for example,
`v0.1.0-rc.1+git.<12-character-revision>`.

Docker container images are built separately on each native Linux architecture. The bundle
contains all four images with reserved, content-addressed `vpnmodo-offline/`
references. The loader verifies archive and configuration hashes, the complete
OCI manifest set, architecture and controller version. It refuses a conflicting
reserved reference before loading and does not overwrite legacy image tags.
Loading does not activate a controller or change an existing VPN deployment.

Docker's classic and containerd image stores can assign different local IDs to
the same exported image. Save the loader's JSON output as `installed-release.json`:
it records the verified destination-local immutable IDs for all four images.
The embedded setup code applies those exact IDs, including backend defaults.
Do not substitute mutable `:latest`/`:dev` references. Keep the old receipt and
images for explicit rollback. Source identity and artifact hashes are pinned;
byte-identical image rebuilds are not promised.

## Maintainer build

Use Python 3.9+, Go 1.27.1, a clean committed checkout and an output directory
outside source. The builder exports only `git archive HEAD`; ignored files,
customer folders, credentials and local runtime state are not included. Exported
source carries a hashed committed-file inventory: changed source files are
refused, and extra files added to an extracted source directory are never copied.

```sh
python3 scripts/build-release.py cli --version v0.1.0-rc.1 \
  --output /absolute/private/build/cli
cd /absolute/private/build/cli
shasum -a 256 -c SHA256SUMS-cli
```

Transfer the checksum-verified source archive to an authorized, isolated native
Linux builder. Extract outside any repository; run its bundled script:

```sh
python3 scripts/build-release.py vm --version v0.1.0-rc.1 \
  --output /absolute/private/build/linux-arm64
cd /absolute/private/build/linux-arm64
sha256sum -c SHA256SUMS-linux-arm64
```

The VM builder needs Python 3.9+, native rootful Docker with Compose v2 and
registry/package download access. It builds the full production controller
Dockerfile, including the pinned Go builder stage; no host Go installation is
needed. Its Docker socket is explicitly `/var/run/docker.sock`. Use the nested
[local lab](../tests/local-lab/README.md) on a shared VM. Build images only in its
isolated daemon. The builder can also produce a native amd64 container image bundle, but
Intel builds/testing are outside the first-release qualification scope. The
script does not install emulation or provision another VM.

The output directory must not exist. Failed builds can leave partial artifacts;
discard the failed output directory and start with a new one. Successful builds
write `SHA256SUMS-cli` or `SHA256SUMS-linux-<arch>` last. Review diffs and run
`scripts/check-secrets` before pushing source. Review distribution contents before
sharing. Without `--publish`, these commands do not upload artifacts. CI remains
disabled.

## Publish private GHCR images

On the native Linux builder, log Docker into `ghcr.io` with package write access,
then use the existing VM build command with `--publish`:

```sh
python3 scripts/build-release.py vm --version v0.1.0-rc.7 --publish \
  --output /absolute/private/release/linux-arm64
```

This builds the same four images and offline bundle, then pushes
`ghcr.io/derrickhayashi/vpn-sidecars-next/ROLE:VERSION-arm64` for `controller`,
`l2tp`, `xauth` and `application`. `registry-release.json` records the returned
registry digests. Docker can push a single-platform manifest whose digest
differs from a local index; use the push result, not local `RepoDigests` or
image IDs. Keep the packages private
and grant intended users package read access.

The CLI embeds the Lima YAML, image installer and default registry manifest.
When selecting a new runtime release, update `examples/lima/registry-release.json`
from the published manifest before building the standalone CLI. Provisioning
emits a receipt consumed internally by setup. No hosted CI is used for publication.

## Installation and upgrades

Follow [README installation](../README.md#installation). To replace the standalone
CLI, install the new executable at the same path and check `vpnmodo version`.
Replacing the CLI does not disconnect a VM-owned VPN connection.

Setup reuses matching images and the controller; it does not silently replace a
different running controller release. Runtime upgrades require a release-specific
migration procedure. Do not delete controller storage containing saved profiles
and credentials as an upgrade shortcut.

## Validation scope

See the [candidate validation record](release-validation.md) for tested revisions,
platform results and remaining release gates.

The agreed first-release qualification targets are macOS arm64 and Ubuntu 24.04
Linux arm64. Intel/amd64 native testing is not a release gate. The CLI builder
still produces amd64 cross-built artifacts; record them as unvalidated on native
Intel hardware. A successful cross-build alone does not establish runtime
acceptance. Record native CLI installation, verified SSH onboarding, real
VPN/service use, controller upgrade and disposal evidence for the ARM64 targets.

The existing packet/recovery acceptance suites remain required backend evidence.
`--onboarding-only` in the isolated harness tests packaged production images
without shortening their production MFA/recovery policy. It exercises real
authentication, private service access and controller replacement; it does not
replace the full packet/failure/rekey suite. Real ARM64 adoption/cutover has
passed, as recorded in the validation record. Legacy rollback testing is outside
the agreed release scope. Controller upgrade/replacement acceptance remains
required. Keep the
unreproduced startup observation #19 open; it is not a reproducible release blocker.

Inside the isolated lab, after preparing the gateway/service fixture images from
its normal runner, the production onboarding check accepts a different controller
image for upgrade rehearsal:

```sh
VPNMODO_ISOLATED_TEST_HOST=1 python3 tests/smoke_controller.py \
  --binary /opt/lab/vpnmodo --image sha256:<candidate-controller-id> \
  --upgrade-image sha256:<compatible-upgrade-controller-id> \
  --xauth --onboarding-only
```

Repeat with `--l2tp` instead of `--xauth`. These are synthetic fixtures, not commands
to run on the VM host daemon. The L2TP gateway build uses `vpnmodo-l2tp:dev`, and
the XAuth pre-login guard uses `vpnmodo-xauth:dev`; when testing release-only images,
point these fixture aliases at the verified corresponding image IDs inside the
isolated daemon. Actual managed profiles continue using their pinned image IDs.
