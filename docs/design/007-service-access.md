# Workstation service access and diagnostics

Issue #8 adds named localhost forwards and composed `up`/`down` commands. The VM
still owns VPN profiles and credentials. The workstation owns service names,
local ports, private destination SSH aliases and destination-relative endpoints.
Existing `scripts/vpn-services` mappings are not imported or rewritten.

## Configure once

Register a managed profile alias using the profile commands, then write a private
service definition on your workstation (mode 0600):

```json
{
  "schema_version": 1,
  "services": [
    {
      "name": "dashboard",
      "ssh_target": "private-jump",
      "ssh_config": "/absolute/path/to/project/vpn/ssh_config",
      "local_port": 18081,
      "remote_host": "127.0.0.1",
      "remote_port": 8081
    }
  ]
}
```

`ssh_target` selects the private destination's normal workstation OpenSSH settings:
its host, user, port, identity and known-host files. `ssh_config` is optional;
omitting it uses normal OpenSSH configuration. When supplied it must be absolute.
The target's host key must already be verified through a trusted process. Unknown
or changed keys fail; neither hop accepts keys automatically. Private keys and
SSH agents remain on the workstation. Agent forwarding is disabled.

`remote_host`/`remote_port` are reached **from the private SSH destination**. For
example, 127.0.0.1:8081 means that destination's loopback service. It does not mean
the VM or workstation loopback. V1 accepts IPv4 literals and ASCII DNS names;
IPv6 forwarding targets are excluded by the current VPN namespace policy.

```sh
chmod 600 /absolute/path/to/services.json
vpnmodo service configure work-vpn --file /absolute/path/to/services.json
vpnmodo up work-vpn
vpnmodo status work-vpn
vpnmodo doctor work-vpn
vpnmodo down work-vpn
```

No wizard or consumer-file modification is involved. Definitions have at most 32
services with unique names and local ports. Strict decoding rejects duplicate,
unknown or incorrectly cased fields. Configure validates/stores locally without
contacting the VM. Stop owned services before replacing their definition.

`up` waits for the managed VPN, including hidden XAuth MFA input when required,
then opens the configured forwards. A healthy existing session is reused. It
never treats a TCP listener as proof of end-to-end readiness. `down` attempts
owned local teardown and explicit VM disconnect; unreachable VM/control failures
remain visible rather than claiming disconnect succeeded. Credentials remain
provisioned, as with `connection disconnect`.

Services can be controlled independently:

```sh
vpnmodo service start work-vpn --service dashboard
vpnmodo service stop work-vpn --service dashboard
vpnmodo service status work-vpn
vpnmodo up work-vpn --service dashboard
```

`service start` requires an already healthy managed VPN; it never initiates login.
Omitting `--service` selects all configured/owned services. Stopping one service
leaves other services and the VPN running. Different profiles have separate locks
and session ownership. Aliases referring to the same VM profile share that
profile's connection intent: `down` disconnects the underlying profile.

## Transport and ownership

The private destination SSH connection follows this path:

```text
workstation OpenSSH
  -> verified VM SSH
  -> fixed Python helper on the VM
  -> controller connection.application RPC (fresh owned runtime checks)
  -> Docker exec into the verified application container, nc destination:port
  -> private destination OpenSSH server
  -> private service
```

The helper's fixed program is embedded in the CLI. A bounded JSON stdin header
carries profile and endpoint data; none is interpolated into the VM shell command.
It reads the header byte by byte so it cannot consume the following SSH stream.
After successful protocol negotiation and healthy owned-application resolution,
it execs Docker/nc with separate validated arguments and an exact container ID.
There is no arbitrary Docker exec RPC or credential read-back API.

`connection.application` and profile-scoped `system.doctor` acquire the same
profile runtime gate used by monitoring and connection changes. They are bounded,
read-only inspections: no ensure, repair, build, pull or authentication. An active
operation returns busy. Container project/profile/backend/revision/image and
namespace checks remain required. A previous operation's success alone is not
proof of the current application. A known unhealthy/disconnected runtime never
returns an application ID.

After resolution, forwarding runs outside the controller container. Replacing
that container therefore does not cut an established SSH stream. Application/VPN
replacement can break a stream; use `service start`/`up` explicitly to reconnect.
There is no unattended SSH restart loop that could trigger fresh VPN MFA.
XAuth packet enforcement continues to protect a retained namespace independently
of these workstation checks.

Each service has an independent OpenSSH control master and exactly one IPv4
127.0.0.1 listener. Inherited forwards and remote/local commands are cleared.
A protected descriptor and short private control-socket directory live under
`/tmp/vpnmodo-UID-TOKEN`; ownership records live next to the host registry in
`hosts.json.access`. Configuration/state files use 0600 and directories 0700.
Workstation runtime records contain destination metadata, never VPN credentials.
Records are written before starting a master so an uncertain startup remains
recoverable. Stop uses that exact control socket, never a saved process ID.
A failed teardown retains ownership for explicit retry. Unknown files, foreign
paths, symlinks and live unresponsive sockets are not recursively removed.

An alias, VM or service binding change cannot silently reuse an old master.
Stop it explicitly first. Port conflicts do not choose a new port or terminate
another listener; correct the configured port or stop the process that owns it.
Other successful selected forwards remain available and the result is partial.

## Diagnostics and automation

`status` and `doctor` perform current network probes and report separate evidence:

- VM SSH host-key/authentication reachability and negotiated controller version.
- Controller state/Docker readiness; profile revision and credential presence.
- Compose and local backend/application image availability, without pulling.
- Deployment ownership, authenticated tunnel, private health TCP endpoint and
  application attachment, independently of the saved connection record.
- Each owned local listener, authenticated destination SSH master and TCP access
  to the configured service from that destination.

The destination's service probe uses Python 3 stdlib over its authenticated SSH
session. Python is required there for verified service readiness, not for raw
forwarding. If remote execution/Python is unavailable, a forward can remain open
but reports `remote_service_unverified` and the overall result is partial. A
successful TCP probe verifies reachability, not HTTP health, application login or
certificate validity. These checks describe a point in time.

Normal terminal output names each failed layer/service and each local endpoint.
JSON status/errors use fixed codes and never include raw SSH/backend stderr.
A final report has `type: "access"`, `outcome`, `vpn_ready`, `ready`, `checks`,
`services` and optional `error`. `ready` requires at least one selected service
and all selected checks to pass. With no configured service, a healthy VPN reports
`vpn_ready`, not end-to-end service readiness. Partial/failure exits nonzero.
Configuration, stop and successful disconnect have their own successful outcomes.

`up --json` and `down --json` emit LF-separated tagged `type: "operation"` events,
then a final access report. MFA events include the operation/challenge references
and deadline; JSON mode never prompts. A second caller can deliver the code with
`operation submit-mfa ... --code-stdin`. Closing a watcher does not cancel the
VM-owned operation. `status --json`, `doctor --json` and service-only commands
return a single report. Stdout contains only these results/events.

| Code | Meaning/action |
| --- | --- |
| `vm_ssh_unavailable` | Check VM reachability, known host key and workstation authentication |
| `controller_unavailable` | Check/explicitly repair the controller with `host check` / `host setup` |
| `inspection_unavailable_operation_may_be_active` | Inspect/watch the existing operation before retrying |
| `vpn_access_unavailable` | Inspect the separate VPN, endpoint and attachment checks; diagnosis never starts login |
| `local_port_conflict` | Release that port or stop services and explicitly reconfigure it |
| `destination_ssh_unavailable` | Check the private SSH target's verified key, authentication and reachability |
| `remote_service_unavailable` | Destination SSH works; check the service endpoint there |
| `remote_service_unverified` | Forward exists, but destination Python/command probing could not verify the service |
| `service_binding_changed_stop_first` | Stop the recorded master before starting the new binding |
| `stop_failed` | Ownership is retained; retry explicit stop after resolving the control-socket problem |

The service acceptance fixture uses synthetic SSH identities on a private SSH
server in each real VPN lab, with a separate destination-loopback HTTP endpoint.
See the [local lab](../../tests/local-lab/README.md) for execution and validation.
