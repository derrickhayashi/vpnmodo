# Release artifacts

ARM64 candidate qualification is recorded in the
[validation record](release-validation.md). Builds stay local by default;
`vm --publish` uploads release images to GHCR. The standalone CLI is distributed
through GitHub prereleases; there is no Homebrew tap. Repository visibility and
GHCR package access are separate settings. Public runtime images can be pulled without registry credentials. Build CLI artifacts on the workstation and container images on
a native Linux builder.

## Single-file CLI download

The current standalone CLI release is `v0.1.0-rc.13`. Download `vpnmodo-darwin-arm64`
from the [rc.13 release](https://github.com/derrickhayashi/vpnmodo/releases/tag/v0.1.0-rc.13),
install it and run `vpnmodo setup`. Sign in with an authorized GitHub account if
release access is restricted. For private images supply the optional
`--registry-auth FILE` described in [installation](../README.md#installation).
The supported environment is one Apple Silicon Mac with one Lima VM managed by
vpnmodo. Setup prepares the controller. VPN settings remain in JSON profiles, which
automatically use the managed VM. There is no host selection, external VM setup
or manual provisioning-file transfer.

The executable includes Lima YAML, the VM installer and a digest-pinned registry
manifest. The release builder embeds the published runtime manifest through
`--runtime-manifest FILE`, so the CLI and controller can use images built from the
same committed source revision. `registry-release.json` is distributed alongside
the binaries and included in their checksums. The
[validation record](release-validation.md) distinguishes historical qualification
from checks performed for the current release.

`SHA256SUMS-cli` covers the executable. macOS binaries are unsigned and not
notarized; follow your team's approved software policy. No installer changes
Gatekeeper settings. Existing source and image archives remain maintainer build
artifacts, rather than another supported user installation flow.

## Matching CLI and runtime builds

Build and publish the ARM64 VM images first from a clean committed revision.
Pass the returned `registry-release.json` to the CLI build from that same revision
and version using `--runtime-manifest`. The builder refuses mismatched identities,
mutable image tags or another repository's images. This avoids changing source
between the runtime build and CLI build. The source archive and separately
published runtime manifest provide the inputs needed to reproduce the CLI build.

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
python3 scripts/build-release.py cli --version v0.1.0-rc.13 \
  --runtime-manifest /absolute/private/build/linux-arm64/registry-release.json \
  --output /absolute/private/build/cli
cd /absolute/private/build/cli
shasum -a 256 -c SHA256SUMS-cli
```

Transfer the checksum-verified source archive to an isolated native
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
write `SHA256SUMS-cli` or `SHA256SUMS-linux-<arch>` last. Without `--publish`,
these commands do not upload artifacts.

## Publish GHCR images

On the native Linux builder, log Docker into `ghcr.io` with package write access,
then use the existing VM build command with `--publish`:

```sh
python3 scripts/build-release.py vm --version v0.1.0-rc.13 --publish \
  --output /absolute/private/release/linux-arm64
```

This builds the same four images and offline bundle, then pushes
`ghcr.io/derrickhayashi/vpnmodo/ROLE:VERSION-arm64` for `controller`,
`l2tp`, `xauth` and `application`. `registry-release.json` records the returned
registry digests. Docker can push a single-platform manifest whose digest
differs from a local index; use the push result, not local `RepoDigests` or
image IDs. Image publication does not change package visibility. Review the
source and built image contents before changing package access; for restricted
packages, grant intended users read access. Making this repository public does
not make its packages public.

The CLI embeds the Lima YAML, image installer and default registry manifest.
Pass the published manifest to the CLI builder with `--runtime-manifest`, using
the same source revision and release version as the image build. Afterwards,
update `examples/lima/registry-release.json` for source builds. Provisioning
emits a receipt consumed internally by setup.

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
platform results and known limitations.

Recorded native qualification covers macOS arm64 and Ubuntu 24.04 Linux arm64.
The CLI builder also produces amd64 cross-built artifacts, but those do not have
native Intel runtime qualification. Legacy rollback testing is not covered.

The `--onboarding-only` subset tests packaged production images without shortening
their MFA/recovery timeouts. It exercises authentication, private TCP/DNS, service
access and controller replacement. It does not run the full packet/failure/rekey
suite. See [known issues](known-issues.md) for unresolved startup and session
observations.

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

## Publishing in-place upgrade metadata

See [updating an installation](upgrading.md) for the user workflow. This feature
requires two additional assets in the selected GitHub release. Build the CLI and
publish the Linux arm64 registry images from the same committed revision and
version, then generate the descriptor:

```sh
python3 scripts/build-upgrade-manifest.py \
  --cli-dir /absolute/private/build/cli \
  --runtime /absolute/private/build/linux-arm64/registry-release.json
```

Upload `upgrade-darwin-arm64.json` and the raw `vpnmodo-darwin-arm64` binary from
the CLI output directory to that version's GitHub release. Verify that the
Releases API reports SHA-256 asset digests before directing users to upgrade.
The descriptor records API major 1 and state schema 1, CLI size/hash, matched
CLI/controller identity, and the four immutable GHCR image references. Releases
requiring another state schema must provide a separately designed migration;
changing this descriptor's compatibility numbers does not implement migration.
Do not publish unqualified images or enable additional release CI workflows.
