# vpnmodo

Private development workspace: see [development boundaries and checks](docs/development.md).

The planned `vpnmodo` CLI manages VPN profiles through a containerized
`vpnmodo-controller` on each user's Linux VM. See the
[implementation requirements](docs/implementation-requirements.md) and
[staged delivery plan](docs/implementation-plan.md). The host/controller transport
skeleton, secure profile management and persistent operation coordination are
available for development. Real VPN backend integration is a subsequent stage. Existing backend commands
below remain available.

Reusable Docker VPN backends for SSH, Ansible and private service access.
Customer projects own their profiles, deployment settings and runtime state;
this repository contains reusable code and generic examples.

Read [how the VPN sidecar works](docs/architecture.md) for architecture and
connection diagrams, the shared network namespace, and user access through SSH
and local service forwards.

## Backends

```text
containers/
  l2tp-ipsec/
    Dockerfile
    compose.yaml
    compose.build.yaml
    .env.example
    scripts/
  ipsec-xauth/
    Dockerfile
    compose.yaml
    compose.build.yaml
    connect.exp
    strongswan.conf
    vpn
  tailscale/
    compose.yaml
    .env.example
    resolv.conf
watchdog/
  Dockerfile
  compose.yaml
  compose.build.yaml
scripts/
  vpn
  vpn-services
```

| Backend | Protocol and authentication | Guide |
| --- | --- | --- |
| `l2tp-ipsec` | strongSwan, L2TP and PPP; PSK and username/password | [L2TP/IPsec](containers/l2tp-ipsec/README.md) |
| `ipsec-xauth` | strongSwan IKEv1 PSK/XAuth and emailed MFA | [IPsec/XAuth](containers/ipsec-xauth/README.md) |
| `tailscale` | Tailscale tailnet access; auth key or browser enrollment | [Tailscale](containers/tailscale/README.md) |

The IPsec directories use the same image filenames. `compose.yaml` runs an image;
`compose.build.yaml` supplies its local build context. Select one backend per
consumer profile. Each running profile has VPN, application, and watchdog containers. The VPN
backends are independent; both use the shared watchdog image.

Tailscale uses the official upstream image with persistent enrollment, a shared
application network namespace, and MagicDNS. Run it directly with Compose using
the [Tailscale guide](containers/tailscale/README.md). The profile launcher,
Ansible role, watchdog, and bundled recovery skill support the IPsec backends.

## Consumer profiles and deployment

Start with the [Linux VM deployment guide](docs/deployment.md) for source and
Ansible provisioning or manual launcher deployment of either backend, including
profiles, credentials and first connection. See
[consumer-owned profiles](docs/profiles.md) for the launcher reference and SSH
integration. Generic templates live
in `examples/profiles/`. Pass the consumer's profile directory explicitly:

```sh
bash scripts/vpn --profiles-dir /path/to/consumer/vpn/profiles example validate
bash scripts/vpn --profiles-dir /path/to/consumer/vpn/profiles example build
bash scripts/vpn --profiles-dir /path/to/consumer/vpn/profiles example connect
```

The [Ansible collection role](ansible/README.md) installs VM prerequisites and
provisions either backend. It starts L2TP directly; XAuth login uses the
interactive launcher after provisioning.
The [shared watchdog](watchdog/README.md) runs as a third container through the
Docker socket. L2TP uses automatic recovery; XAuth reports failures requiring
interactive MFA. No watchdog host service or timer is installed.
See [accessing private services](docs/remote-services.md) for a complete SSH
ProxyCommand and local HTTP forwarding example.
Shared workstation service tunnels use `scripts/vpn-services` with a
consumer-owned service mapping; see `examples/services.json`.

See the [Ubuntu L2TP kernel regression note](docs/kernel-regression.md) for
`6.8.0-146-generic`, the tracked bug, and kernel-selection guidance.

## Agent skill

The repository bundles [ensure-vpn](.agents/skills/ensure-vpn/SKILL.md) for
connecting and recovering existing profile deployments. It covers healthy
session reuse, backend-specific MFA, consumer verification and watchdog
interpretation. It does not provision a deployment during routine recovery.

Consumer projects supply their root, profile directory/name, optional wrapper
command, verification targets/expected results, and any retained installation
that must stay inactive. Keep that context in consumer instructions or a VPN
context file. A customer-specific skill can be a thin adapter that loads the
consumer context and this shared skill by its checkout path. Customer settings
and credentials do not belong in the bundled skill.

## Go control-layer development

The control layer provides profile validation, operation states, RPC framing,
and runnable host registration/setup/check and profile management commands. See the
[controller guide](containers/controller/README.md) for building and testing the
SSH/container skeleton, and the
[profile guide](docs/design/002-profile-storage.md) for secure creation/updates.
See the [operation guide](docs/design/003-persistent-operations.md) for persistent
intent, status and cancellation. Real VPN runtime remains disabled. See
[v1 profiles, protocol and operations](docs/design/001-v1-model.md) for schemas, API
rules, lifecycle policy and release targets. Synthetic JSON profile examples live
in `examples/profiles/v1/`. Existing scripts and deployments remain unchanged.

With Go 1.27 installed:

```sh
go test -race ./...
go vet ./...
```

## Validation

Run from this repository root:

```sh
uv run --no-project --with pyyaml --with jinja2 --with ansible-core python -m unittest discover -s tests -v
```

Compose tests require Docker Compose, but do not start containers. Interactive
XAuth tests use fake clients and Docker; see the profile guide. Live VPN
negotiation and authenticated destination access require separate acceptance
checks on the Linux Docker host.
