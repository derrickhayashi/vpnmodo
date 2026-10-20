# Onboarding, profile maintenance and adoption

Start with [README installation](../README.md#installation). `vpnmodo setup`
prepares the single managed local environment. VPN configuration stays in JSON.
Then use [README usage](../README.md#usage) to connect and access private services.

Create a profile with `vpnmodo profile create NAME --file PROFILE.json`. It uses
the managed VM and prompts for credentials separately.
VM selection and manual host registration are not part of the CLI.

## Credential input for automation

Interactive `profile create` and `profile rotate-credentials` use hidden prompts.
Automated callers can use `--credentials-stdin` or `--credentials-fd N`. The input
is a JSON object with `username`, `password` and `psk` fields, supplied through a
protected pipe or an owner-only regular file. For example, if your approved
secret-management process has already created a mode-0600 file outside Git:

```sh
vpnmodo profile create work \
  --file "$HOME/vpnmodo-private/work.json" \
  --credentials-stdin < "$HOME/vpnmodo-private/credentials.json"
```

This file is an additional credential copy that you must manage under your
team's policy. Prefer hidden prompts for manual use. Never put secrets in shell
arguments, environment variables, profile JSON, issues or logs. The VM vault is
plaintext protected by filesystem permissions and is readable by VM/Docker
administrators; SSH encrypts transport. The Mac registry does not retain a VPN
credential copy.

`--json` operation watching does not prompt for MFA. Deliver the current code
through protected stdin for the matching operation/challenge using
`operation submit-mfa`, as described in the [XAuth MFA guide](design/006-managed-xauth.md).
A disconnected watcher does not cancel the VM-owned operation. Inspect/watch it
before starting a new login.

## Update a profile or rotate credentials

First inspect the current revision:

```sh
vpnmodo profile inspect work
```

Edit your private profile JSON while preserving its `name`, then use the returned
revision number in place of `N`:

```sh
vpnmodo down work
vpnmodo profile update work --file "$HOME/vpnmodo-private/work.json" --revision N
vpnmodo profile inspect work
```

To rotate credentials, inspect again and pass the **current** revision. The
command prompts for the new values; connect explicitly afterwards:

```sh
vpnmodo profile rotate-credentials work --revision N
vpnmodo up work
```

Mutations require the managed profile to be inactive. Revisions prevent stale
updates; do not reuse an old revision after a successful mutation. See
[profile storage](design/002-profile-storage.md) for lifecycle and disposal rules.

## Removal

Run `vpnmodo down work` while the CLI is still installed. This removes owned
workstation forwards and disconnects the profile; credentials remain on the VM.
To dispose of the inactive VM profile, inspect its current revision and run:

```sh
vpnmodo profile delete work --revision N
```

`profile unregister` only removes a workstation reference; it does not delete a
VM profile. To remove the workstation executable, remove `<prefix>/bin/vpnmodo`
after stopping owned forwards. Removing the binary does not disconnect a VM
session or dispose of VM secrets. Preserve local registry/service files if you
intend to reinstall. Do not delete the controller's storage while other profiles
still need it.
