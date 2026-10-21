# Updating an installation

With a CLI that supports `upgrade`, update the existing installation with:

```sh
vpnmodo upgrade --check
vpnmodo upgrade
```

The command updates the standalone CLI and controller in your existing Lima VM.
It preserves VPN profiles, credentials, service mappings, registry login, SSH
identity, and diagnostic logs. Connected VPN and vpntools containers
keep running. Their new images take effect after you explicitly disconnect and connect
that VPN again; XAuth may then ask for a fresh MFA code.

An upgrade briefly pauses the controller while copying its private state. Status
and configuration commands can be unavailable during this interval. It refuses
an upgrade if a connection operation, MFA prompt, recovery operation, or unknown
connection state is recorded. Finish that operation or resolve the uncertain
state before trying again. A candidate controller blocks mutating requests and
reconciliation until the workstation has installed its matching CLI.

The initial implementation supports Apple Silicon macOS and Linux arm64 runtime
images, API major 1 and state schema 1. It refuses other schemas before replacing
the controller. It does not migrate state formats or recreate VMs.

## Selecting a release

The default is the repository's latest stable GitHub release. To explicitly
select a version or opt into the newest published prerelease:

```sh
vpnmodo upgrade --version v1.2.3
vpnmodo upgrade --prerelease
```

These version numbers illustrate syntax. A selected release must include
`upgrade-darwin-arm64.json` and `vpnmodo-darwin-arm64` assets with SHA-256 digests
in the GitHub Releases API. Older releases without this metadata are refused;
the command does not fall back to an unverified download. Automatic prereleases
include this metadata; only [promoted releases](releases.md#promoting-a-release)
become the default stable upgrade target.

If repository access requires authentication, supply a user-owned mode-0600
file containing a GitHub token with repository contents read access:

```sh
vpnmodo upgrade --github-token-file /absolute/private/github-token
```

The token is read for this invocation; it is neither saved nor copied into the
VM. Docker reuses the VM's existing GHCR login. Release assets are checked against
GitHub's HTTPS API digests; runtime references must be immutable repository GHCR
image digests. This trusts GitHub, the repository's release publishers and their
accounts. It does not provide independent signatures or notarization.

For a CLI predating this feature, or version `v0.1.0-rc.13` and earlier, replace
the standalone executable once using the normal verified download procedure.
Those early clients do not recognize the `vpntools` image name. Subsequent
updates use `vpnmodo upgrade`.
If you use a separately registered host, pass `--host NAME`; the default is the
managed `vpn-vm` host. The selected host must already have a running managed
controller and private controller state.

## Interrupted updates

An interrupted update keeps a private pending transaction. Run:

```sh
vpnmodo upgrade --resume
```

If preparation did not finish, or you want to abandon a pending update, run:

```sh
vpnmodo upgrade --rollback
```

Resume and rollback use the saved release, host and executable path. They do not
need another download or a token. Avoid other configuration changes until the
pending transaction is resolved. A failed candidate activation automatically
restores the previous controller and its state snapshot. The workstation still
requires `--rollback` to close its pending record after that failure.

Rollback applies to a **pending** transaction. After a successful commit, normal
configuration changes resume, so this command refuses to restore an old snapshot
over newer data. It does not offer rollback of an arbitrary completed upgrade.

Controller snapshots remain private on the VM under
`~/.local/share/vpnmodo/upgrades/`. Workstation executable backups remain under
`upgrades/` beside `hosts.json`; these do not contain VPN credentials. Treat VM
snapshots as credentials because they contain the same private material as live
controller state. Completed VM snapshots are pruned to the two newest completed
transactions. Pending transactions are retained for recovery. Image caches are
left in place so existing containers and pending rollback can use their images.
