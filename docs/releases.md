# Release artifacts

Every push to `main` runs the **Build prerelease** workflow. After Go checks,
secret scanning and Python regressions succeed, it builds the Linux ARM64
container images, publishes them to GHCR and builds matching CLI artifacts.
Native macOS ARM64 installation checks run before the complete GitHub prerelease
is published. Protocol acceptance runs separately on a local Linux lab.

See [supported platforms](supported-platforms.md) before choosing a build.
GitHub-hosted builds require no workstation or local VM to be online.

## Downloading the CLI

Open [Releases](https://github.com/derrickhayashi/vpnmodo/releases) and download
`vpnmodo-darwin-arm64` for an Apple Silicon Mac. Choose the latest stable release
for normal use, or a release marked **Pre-release** to test a candidate. If no
stable release exists yet, the available releases are candidates.

Follow [installation](../README.md#installation) to install the executable and
run `vpnmodo setup`. The executable embeds the Lima inputs, VM installer and
immutable runtime image references for that release. Public images need no
registry credentials. Repository visibility and GHCR package access are separate.
For private images, use the optional registry-auth file described in installation.
macOS executables are unsigned and not notarized.

## Automatic prereleases

Each workflow run receives a permanent version, `v0.1.N`, where `N` is the
workflow run number. Numbers may have gaps. A rerun keeps the same version and
source commit. Already-published versions cannot be overwritten; start a new
**Build prerelease** run on `main` to build another candidate from that commit.
A failed publication can resume its own unpublished draft by rerunning failed
jobs. Files are uploaded and verified while the release is a draft, then it is
published with **Pre-release** set and **Latest** unset.

The runtime and CLI come from the same commit. The CLI embeds the runtime's
published GHCR digests. Release assets include the executables and archives,
`registry-release.json`, `upgrade-darwin-arm64.json`, `build-metadata.json` and
`SHA256SUMS`. The build metadata links the version to its source and workflow run.
No user installation is upgraded by a merge or by publication.

The workflow uses the repository's `GITHUB_TOKEN`, with package-write permission
only in the runtime job and release-write permission only in the publication job.
GHCR packages must be linked to this repository and permit its Actions workflows
to write. New GHCR packages start private: set each new package to **Public**
in its package settings, then rerun failed build jobs. The image checks use no
publisher credentials and refuse to publish a candidate whose images require
a registry login. This is a one-time setting per new package. Actions must be
enabled for the two release workflows. No additional personal token is needed.
Change the `v0.1` prefix in `release.yml` when starting a new version series; never reuse a published version.

## Promoting a release

1. Install and test the exact candidate, including required local VPN protocol
   acceptance. Keep results in its PR or issue.
2. Open **Actions → Promote release → Run workflow** on GitHub.
3. Select `main`, enter the existing version and confirm that local protocol
   acceptance for that exact build is complete. Run the workflow.

Promotion verifies the successful main-branch build, source tag, full asset set,
checksums, matching CLI/runtime metadata and anonymous availability of every
pinned GHCR manifest. It then clears **Pre-release** and sets **Latest** on the same release.
It does not rebuild, rename the version or replace any executable or image.
Promotion refuses to move **Latest** to an older version.

Users receive the promoted version through `vpnmodo upgrade`. To opt into a
candidate, use `vpnmodo upgrade --prerelease` or `--version VERSION`. Promotion
changes release availability; each user chooses when to upgrade. See
[updating an installation](upgrading.md) for runtime behavior and recovery.

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
contains the controller, L2TP, XAuth, IKEv2 and application images with
reserved, content-addressed `vpnmodo-offline/`
references. The loader verifies archive and configuration hashes, the complete
OCI manifest set, architecture and controller version. It refuses a conflicting
reserved reference before loading and does not overwrite pre-existing image tags.
Loading does not activate a controller or change an existing VPN deployment.

Docker's classic and containerd image stores can assign different local IDs to
the same exported image. Save the loader's JSON output as `installed-release.json`:
it records the verified destination-local immutable IDs for the complete image set.
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

This builds the same images and offline bundle, then pushes
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

## Release checks

Run release checks on the [supported platforms](supported-platforms.md).
Cross-building an executable does not qualify it for native operation.
Keep results and source/image identities in the release PR or issue.

The `--onboarding-only` subset tests packaged production images without shortening
their MFA/recovery timeouts. It exercises authentication, private TCP/DNS, service
access and controller replacement. It does not run the full packet/failure/rekey
suite. See [known issues](known-issues.md) for unresolved startup and session failures.

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
