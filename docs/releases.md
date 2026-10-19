# Private release artifacts

Issue #9 supplies an offline distribution path. These are private release
candidates until the adoption/onboarding release gate is complete; no registry,
Homebrew tap or GitHub release is published by the build scripts. GitHub Actions
stays disabled. Build and test on the workstation and authorized local VM.

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

VM images are built separately on each native Linux architecture. The controller
embeds exact local image IDs for L2TP, XAuth and the application. Docker archives
contain image IDs without repository tags. The loader checks the checksum, full
image set, configuration hashes, architecture, controller version and embedded
backend IDs. It refuses tagged archives before loading, so an existing legacy
image tag remains unchanged. Loading images does not activate a controller,
connect a profile, stop a watchdog or modify a legacy deployment.

Image IDs are Docker content hashes used with `docker load`; they are not registry
manifest digests. Do not substitute mutable `:latest`/`:dev` references for the
recorded IDs. Keep the previous bundle/images for explicit rollback. Building
again can resolve newer upstream packages: source identity and artifact hashes
are pinned, but byte-identical image rebuilds are not promised.

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
isolated daemon. Repeat on native amd64 to produce the amd64 VM bundle; this
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
vpnmodo host setup personal-vm --image sha256:<new-controller-id> --replace
vpnmodo host check personal-vm --json
vpnmodo status example
```

Choose a maintenance window with no login/MFA operation in progress. Established
VPN sessions and persistent state remain in place when replacing the controller.
New sessions use the release's pinned backend defaults; existing sessions retain
the image identities in their deployment records. Do not prune old images while
such sessions or rollback plans still need them. An interrupted login requires
an explicit new attempt and fresh XAuth MFA.

For controller rollback, repeat `host setup --image <old-id> --replace`, then
verify status and private service access. This applies to compatible schema/API
versions. Do not run an older binary against a newer incompatible state schema;
a future schema-changing release needs its own migration/backup instructions.
Never automatically replace credential/journal files with an older backup while
sessions are active. CLI rollback does not disconnect a VPN.

## Validation scope

The release targets are macOS arm64/amd64 and Ubuntu 24.04 Linux arm64/amd64.
A successful cross-build alone does not establish runtime acceptance. Record
native CLI installation, verified SSH onboarding, real VPN/service use,
controller upgrade and disposal evidence for each tested architecture.

The existing packet/recovery acceptance suites remain required backend evidence.
`--onboarding-only` in the isolated harness tests packaged production images
without shortening their production MFA/recovery policy. It exercises real
authentication, private service access and controller replacement; it does not
replace the full packet/failure/rekey suite. Legacy coexistence, explicit cutover
and rollback with fresh MFA remain separate release-gate checks in #9. Keep the
unreproduced startup observation #19 open; it is not a reproducible release blocker.
