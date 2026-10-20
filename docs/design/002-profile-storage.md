# Managed profile creation and credential storage

Issue #4 adds profile management to the controller/SSH skeleton. VPN connection
operations remain disabled. Creating a profile validates and commits its settings
and credentials; it does not log in to a gateway, create VPN containers or adopt
an existing deployment.

## Workstation workflow

Build and register/setup the VM as described in the
[controller guide](../../containers/controller/README.md), then create an explicit
JSON profile using the [backend examples](../../examples/profiles/v1/):

```sh
/tmp/vpnmodo profile create example --file /private/path/profile.json
/tmp/vpnmodo profile list
/tmp/vpnmodo profile list
/tmp/vpnmodo profile inspect example
```

There is no wizard. Creation prompts only for username, password and PSK, with
all three hidden. Cancellation restores terminal settings and sends no partial
credential request. With automation, `--credentials-stdin` reads one bounded JSON
object until EOF; `--credentials-fd N` reads an inherited descriptor numbered at
least 3. The object has exactly `username`, `password` and `psk` string fields.
Neither option accepts a terminal with echo. Regular credential input files must
be owned by the current user and private; pipes are supported. Do not place
credentials in argv, environment variables, profile JSON or shell history.

The VM profile name comes from the profile file. The positional name is a local
alias. Workstation references contain only alias, registered host name and VM
profile name, in a protected sidecar file at `<host-registry>.profiles.json`.
`--config FILE` before `profile` selects the host registry as with host commands.

If SSH is lost during a mutation, its result may be unknown. Inspect/list the VM
before retrying; duplicate creation never overwrites a profile. If the VM commit
succeeds but local registration fails, repair local storage then explicitly
register the already-created VM profile:

```sh
/tmp/vpnmodo profile list
/tmp/vpnmodo profile register example --profile example-l2tp
```

Registration verifies the VM profile through RPC; it never fetches credentials.
`profile unregister ALIAS` removes a local reference only. `profile list` lists profile summaries from the managed VM.
Inspection returns non-secret settings, revision and credential availability.
List responses use compact summaries so configuration sizes cannot overflow the
bounded RPC frame. Results support `--json`; errors use sanitized stderr and a
nonzero exit status.

## Explicit changes and inactivity checks

Use the current revision returned by inspection, for example:

```sh
/tmp/vpnmodo profile update example --file /private/path/profile.json --revision 1
/tmp/vpnmodo profile rotate-credentials example --revision 2
/tmp/vpnmodo profile delete example --revision 3
```

Each successful update/rotation increments the revision. Missing/stale revisions
are rejected. Updates preserve profile name/backend type and credentials. Change
backend type by explicit deletion/recreation of an inactive profile. Rotation
replaces the complete credential set. Delete is separate from disconnect and
reports credential-file disposal; it also removes the selected local alias.
Other aliases may still refer to a deleted VM profile and can be unregistered.

Before a mutation, the controller queries only containers carrying all managed
profile/project labels. Running, paused, restarting or unrecognized states block
the change. Docker errors or ambiguous ownership also block it. Stopped/dead/new
containers are inactive; existing legacy VPNs are excluded by ownership filters.
No Docker resource is modified by these checks. Future lifecycle workers must
coordinate through the profile transaction lock before starting/stopping sessions;
Docker inspection alone cannot serialize an independent actor with socket access.

## VM transaction boundary

Inside the protected controller state root:

```text
profiles/<profile-name>.json       # non-secret settings, revision, credential ref
credentials/<random-token>.json    # username/password/PSK; protected vault only
.profiles.lock                    # interprocess transaction lock
```

Directories use 0700 and written files use 0600. The store rejects symlinks,
foreign ownership, public permissions and malformed records; it does not print
raw storage/credential input. No vault read-back method is exposed. Credential
availability validates the protected file without returning its contents or path.
The vault is permission-protected JSON on the VM, without application encryption.
It is outside the checkout/build context; keep VM disks and backups protected.
Deletion unlinks controller credential files, not historical disk/backups copies.
Go memory is not guaranteed to be erased.

Mutations hold a context-aware file lock, including the inactivity check and
expected-revision comparison. Credential creation/rotation writes a new random
vault file, fsyncs it and its directory, then atomically replaces the non-secret
profile record and fsyncs that directory. A profile is published only after its
credential file is present. Configuration-only updates preserve the reference.

Before disposing old/orphaned vault files, recovery makes the current profile
directory durable, validates all references and removes only store-owned temporary
files and unreferenced credential tokens. It runs under the same lock on startup
and subsequent transactions. Corrupt profile metadata aborts cleanup, preserving
credentials for explicit repair. Partial pre-commit writes leave no usable
profile; a crash may leave an orphan file until the next transaction/startup.

An error after publication/directory sync uncertainty returns the interrupted
outcome rather than pretending the mutation rolled back. New referenced
credentials are retained. Inspect/list after reconnect to determine the committed
revision; recovery finishes orphan disposal. Rotation/deletion success is reported
only after old credential-file cleanup succeeds. The store currently bounds VM
profiles to 128 and workstation aliases to 256.

`internal/privatefile` owns private atomic files/locks, `internal/profilestore`
owns profile/vault transactions, `internal/credentialinput` owns workstation secret
input, and `internal/host` owns local references. Shared profile/RPC validators run
at both ends. Pinned [golang.org/x/term](https://pkg.go.dev/golang.org/x/term@v0.47.0)
provides hidden terminal input and restoration.

## Evidence

Go tests inject failed profile commits before/after publication, failed credential
rotation, concurrent stale updates across independent store instances, unsafe
paths/permissions, cancellation and RPC result redaction. The isolated Ubuntu SSH
acceptance additionally exercises real CLI creation, descriptor rotation, active
and stale update refusal, hidden prompts/Ctrl-C, registration recovery, controller
recreation, persisted credentials and explicit deletion. These tests use synthetic
values and do not establish VPN protocol interoperability or packet isolation.
