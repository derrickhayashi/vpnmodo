# Onboarding and profile maintenance

Start with [README installation](../README.md#installation).
`vpnmodo setup` prepares the single managed local environment. Registry auth,
when needed, uses a separate optional `--registry-auth FILE`. VPN configuration
stays in its own JSON file.
Then use [README usage](../README.md#usage) to connect and access private services.

Create a profile with `vpnmodo profile create NAME --file PROFILE.json`. It uses
the managed VM and reads `credentials.username`, `credentials.password` and
`credentials.psk` from that same file for both supported VPN types.
VM selection and manual host registration are not part of the CLI.

## One input file per VPN

Use one JSON file containing the connection settings and a
`credentials` object. Both L2TP/IPsec and XAuth use this format. Registry auth
belongs in a separate optional JSON file containing GitHub `username` and `token`;
it is never part of the VPN profile. Public images need no auth file. See the complete JSON examples in the
[README](../README.md#vpn-configuration).

Both examples show optional `backend.config.ike_proposals` and
`backend.config.esp_proposals` arrays. Set them to your gateway's approved lists
before `profile create`; there are no separate proposal CLI flags. See
[proposal settings](profiles.md#ike-and-esp-proposals) for each backend's
defaults, syntax, ordering and the L2TP engine-default behavior.

```sh
vpnmodo setup --registry-auth "$HOME/vpnmodo-private/registry-auth.json"
vpnmodo profile create work --file "$HOME/vpnmodo-private/work.json"
vpnmodo up work
```

Setup and profile creation do not prompt for credentials. Missing or invalid
input returns an error. XAuth connection is the only interactive prompt: enter
the fresh MFA code when requested. L2TP uses the supplied saved credentials.

The JSON is a plaintext credential backup; keep it outside Git and protect it.
The controller stores secrets in its separate protected VM vault, omits them
from profile inspection/output, and receives them over encrypted SSH. Never put
secrets in shell arguments, environment variables, issues or logs.

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

Edit your private profile JSON while preserving its `name` and backend type,
then use the returned revision number in place of `N`. To change proposal
settings, edit `backend.config.ike_proposals` and/or `esp_proposals` in that file.
Update sends the complete connection configuration, not a partial patch; keep
the server, health endpoint and any other settings you still need. Removing a
proposal field or setting it to `[]` restores that list's backend defaults.

```sh
vpnmodo down work
vpnmodo profile update work --file "$HOME/vpnmodo-private/work.json" --revision N
vpnmodo profile inspect work
vpnmodo up work
```

Profile update changes connection settings only; a `credentials` object in the
input is validated but does not rotate the saved credentials. It also does not
change a running tunnel: disconnect before updating, then explicitly connect to
use the new proposals (with fresh MFA for XAuth). To change credentials,
disconnect again if connected, edit the same JSON, inspect again and pass the
**current** revision to the explicit
rotation command. It reads the file without prompting; connect afterwards:

```sh
vpnmodo profile rotate-credentials work --file "$HOME/vpnmodo-private/work.json" --revision N
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
