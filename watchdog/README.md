# Shared watchdog container

Each VPN Compose project has a third `watchdog` container. It monitors that
project's `vpn` and `application` services through the Docker UNIX socket.
It does not install host systemd units or need access to host PID namespaces.

| Mode | Backend | Behavior |
| --- | --- | --- |
| `auto` | L2TP/IPsec | Recreate unhealthy VPN, wait for health, reattach application |
| `manual` | IPsec/XAuth | Check health and a private TCP endpoint; log need for fresh MFA |

Checks run every 30 seconds by default. Automatic recovery has a 300-second
cooldown, preserves explicitly stopped VPNs, and leaves forwarding stopped if
recovery fails. It repairs an application's stale network namespace by querying
`readlink /proc/self/ns/net` inside each sibling through Docker exec. Replacement
application images need that command available. Manual mode does not recreate
siblings or trigger emailed login codes. Repeated identical status messages
are suppressed; reports go to Docker logs rather than an external notifier.

## Build and run

The profile launcher builds both images with `build` and includes the watcher
with `connect`. For a standalone build, from the shared repository root:

```sh
docker build -t vpn-sidecar-watchdog:local watchdog/
```

The image uses Docker CLI/Compose and a shell polling loop. This is a Docker
client container, not a nested Docker daemon. It runs as root to access the
socket. The common `compose.yaml` is an overlay for either VPN backend;
`compose.build.yaml` supplies the watchdog build context when building locally.
The Ansible L2TP role can build from pinned source or pull a registry image.

The runtime Compose file must be a fully rendered combined manifest at
`VPN_PROJECT_DIR/compose.yaml`, including the watchdog service and volume.
The launcher and role render this from backend and watchdog inputs before
activation. Rendered files contain image names, connection settings and host
paths, but no credential values. Keep customer settings outside this source.

## Mounts and state

The overlay mounts `/var/run/docker.sock`, the instance runtime, its credentials
directory, and shared source. Runtime/source/credentials mounts are read-only
and use identical absolute host/container paths so Compose can recreate sibling
bind mounts and secrets. A project-scoped named volume at `/state` stores the
lock, last reported status, cooldown, and pending forwarding recovery. Checks also honor the consumer
operation lock while the launcher publishes or activates a manifest.
The container root filesystem is read-only, `/tmp` is temporary, and it needs
no network attachment. Its only added capability is `DAC_OVERRIDE`, allowing
it to read protected host-user runtime files through the read-only mounts.

Docker socket access grants broad host Docker control even when the container
filesystem is read-only. This is a trusted management container. See
[Docker daemon access](https://docs.docker.com/engine/security/protect-access/).

## Operations

Run on the Linux host, using the consumer runtime manifest:

```sh
docker compose -f /path/to/consumer/vpn/instances/example/compose.yaml logs --tail 50 watchdog
docker compose -f /path/to/consumer/vpn/instances/example/compose.yaml stop watchdog
docker compose -f /path/to/consumer/vpn/instances/example/compose.yaml up -d --no-deps watchdog
```

Stop the watchdog during planned VPN/container maintenance. `compose down`
removes the watcher along with the VPN and application; its volume persists.
Docker's `unless-stopped` policy restarts an exited watcher. Failed checks log
an error and the loop retries rather than exiting permanently.

## Verification

Unit tests exercise recovery, cooldown, namespace repair, manual MFA reporting,
and failed recovery without touching live resources. The optional smoke test
builds a watchdog image and creates a separate temporary project with Alpine
containers, fake health checks and no VPN credentials:

```sh
docker build -t vpn-sidecar-watchdog:validation watchdog/
VPN_WATCHDOG_IMAGE=vpn-sidecar-watchdog:validation sh tests/smoke_watchdog.sh
```

Run it on a Linux Docker host. It checks actual socket access, automatic
recovery, forwarding namespace repair, and manual-mode reporting. Its trap
removes its test containers, network, volume and temporary runtime directory.
