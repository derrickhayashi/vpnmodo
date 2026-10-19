# Private release artifacts

Issue #9 supplies an offline distribution path. ARM64 candidate qualification
passes as recorded in the [validation record](release-validation.md). These are
private release candidates; the build scripts publish no registry, Homebrew tap
or GitHub release. GitHub Actions stays disabled. Build and test on the
workstation and authorized local VM.

## Artifacts and trust

A release has macOS arm64/amd64 CLI archives, a committed source archive, and a
separate Linux arm64 or amd64 image bundle. Each artifact group has SHA-256 sums.
Obtain both artifacts and their expected checksums through the team's trusted,
authenticated distribution channel. A checksum detects a changed download; a
checksum delivered with an untrusted replacement does not authenticate it.

CLI archives contain `vpnmodo`, `install.sh`, `release.json` and a short README.
VM bundles contain `images.tar`, `install-vm.py`, `release.json` and a README.
The source version, full Git revision, target architecture and binary version are
recorded in each manifest. `vpnmodo version` reports, for example,
`v0.1.0-rc.1+git.<12-character-revision>`.

VM images are built separately on each native Linux architecture. The bundle
contains all four images with reserved, content-addressed `vpnmodo-offline/`
references. The loader verifies archive and configuration hashes, the complete
OCI manifest set, architecture and controller version. It refuses a conflicting
reserved reference before loading and does not overwrite legacy image tags.
Loading does not activate a controller or change an existing VPN deployment.

Docker's classic and containerd image stores can assign different local IDs to
the same exported image. Save the loader's JSON output as `installed-release.json`:
it records the verified destination-local immutable IDs for all four images.
`host setup --release` applies those exact IDs, including backend defaults.
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
isolated daemon. The builder can also produce a native amd64 VM bundle, but
Intel builds/testing are outside the first-release qualification scope. The
script does not install emulation or provision another VM.

The output directory must not exist. Failed builds can leave partial artifacts;
discard the failed output directory and start with a new one. Successful builds
write `SHA256SUMS-cli` or `SHA256SUMS-linux-<arch>` last. Review diffs and run
`scripts/check-secrets` before pushing source. Review distribution contents before
sharing. Neither command uploads artifacts or enables CI.

## Installation and upgrades

Follow [teammate onboarding](onboarding.md) for verification, installation and
explicit VM activation. CLI installation is atomic and refuses an existing
executable unless `--replace` is provided. Keep the old verified CLI archive to
restore a previous version using the same explicit replacement path. The
installer changes only `<prefix>/bin/vpnmodo` and creates no shell startup edits.

Load the new VM image bundle first, record the old controller image ID, then run:

```sh
vpnmodo host setup personal-vm --release /absolute/private/installed-release.json --replace
vpnmodo host check personal-vm --json
vpnmodo status example
```

Choose a maintenance window with no login/MFA operation in progress. Established
VPN sessions and persistent state remain in place when replacing the controller.
New sessions use the release's pinned backend defaults; existing sessions retain
the image identities in their deployment records. Do not prune old images while
such sessions or rollback plans still need them. An interrupted login requires
an explicit new attempt and fresh XAuth MFA.

For controller rollback, repeat `host setup --release <old-receipt.json> --replace`, then
verify status and private service access. This applies to compatible schema/API
versions. Do not run an older binary against a newer incompatible state schema;
a future schema-changing release needs its own migration/backup instructions.
Never automatically replace credential/journal files with an older backup while
sessions are active. CLI rollback does not disconnect a VPN.

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
