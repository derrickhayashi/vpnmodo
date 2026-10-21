# L2TP/IPsec backend

Run commands in this guide from `containers/l2tp-ipsec/`, unless a command gives
an explicit absolute path. This backend provides project-specific SSH, Ansible, and localhost
service tunnels. Run Docker Engine natively on Ubuntu, including an Ubuntu VM
on macOS. The VPN uses strongSwan (IKEv1/IPsec transport mode), xl2tpd, and PPP.
An Alpine forwarding container shares its network namespace.

For managed profiles, use `backend.type: "l2tp-ipsec"` and optional
`backend.config.ike_proposals` / `esp_proposals` arrays in the
[complete JSON example](../../README.md#vpn-configuration). See
[proposal settings](../../docs/profiles.md#ike-and-esp-proposals) for defaults,
syntax, ordering and the trailing `!` needed to suppress additional strongSwan
engine defaults. The standalone Compose settings are comma-separated
`VPN_IKE_PROPOSALS` and `VPN_ESP_PROPOSALS` values in `.env`; the legacy launcher
uses the same keys in `profile.env`.

```text
Workstation -> SSH to Ubuntu host -> application container -> ppp0
            -> private destination -> optional downstream devices
```

## Storage and project separation

Keep this repository as the shared source. Pin a tested Git tag or commit in
project documentation. Keep non-secret SSH configurations, service mappings,
and Ansible overlays in each project's own repository. Store runtime copies
and credentials on the Ubuntu host, for example:

```text
/srv/vpn-sidecar/example/
  compose.yaml
  .env
  secrets/{username,password,psk}
```

Give each runtime deployment a unique `COMPOSE_PROJECT_NAME` in `.env` and a
separate directory. Docker namespaces isolate routes and DNS, allowing VPNs
with overlapping private address ranges. Each instance also needs its own
watchdog state and service-tunnel name. Avoid duplicate local listening ports.
Plaintext credentials and vault keys must stay outside Git. Consumer
repositories may track encrypted Ansible Vault files. The ignore files exclude
`.env` and plaintext secrets from Git and image builds; Compose mounts the credential files at runtime.

## Prepare the Ubuntu host

Install Docker Engine with its Compose plugin. Load PPP/L2TP modules:

```sh
sudo modprobe ppp_generic
sudo modprobe ppp_async
sudo modprobe l2tp_ppp
ls -l /dev/ppp
```

Persist the modules in `/etc/modules-load.d/l2tp-vpn.conf`, one per line.
The network must allow UDP 500 and 4500; ESP protocol 50 is needed when NAT-T
is disabled. Only the VPN container receives NET_ADMIN, NET_RAW, and /dev/ppp.

## Configure an instance

Copy `compose.yaml` and `.env.example` from a pinned release to a fresh
deployment directory. From there:

```sh
cp .env.example .env
mkdir -p secrets
chmod 700 secrets
```

Edit `.env`: set `VPN_IMAGE` to the published image tag or digest, a unique
`COMPOSE_PROJECT_NAME`, `VPN_SERVER`, and a stable
private `VPN_HEALTH_HOST` with a listening `VPN_HEALTH_PORT` (default 22).
The example IP addresses are documentation placeholders. Create `username`,
`password`, and `psk` files under `secrets/` using an editor or secure credential
provisioning. Do not put credentials in command history. Restrict permissions:

```sh
chmod 600 .env secrets/username secrets/password secrets/psk
sudo docker compose pull
sudo docker compose up -d --wait --wait-timeout 120
sudo docker compose ps
```

Compose reads the adjacent `.env`, including the project name. Keep that name
stable. When using `-p`, use the same value everywhere, including ProxyCommand
and watchdog commands; otherwise they can target a different deployment.

`VPN_DNS` optionally specifies comma-separated private DNS servers. Otherwise
PPP-provided DNS is used, and startup fails if PPP supplies none. Configure DNS explicitly when the
server does not provide it. Adjust IKE/ESP proposal settings for server
compatibility using the [proposal guide](../../docs/profiles.md#ike-and-esp-proposals).
Some servers allow one login per account.

The entrypoint keeps a host route to the VPN server, routes application traffic
through ppp0, applies an IPv4 OUTPUT kill switch, and disables IPv6. DNSmasq
listens on loopback inside the namespace. The forwarding container sets its
resolver to that listener. Preserve this resolver setup for replacement
workloads. Publish any workload ports on `vpn`, which owns the namespace.

The healthcheck checks readiness, PPP, routing, and the kill switch, then tests
the configured private TCP endpoint. Six failed probes mark it unhealthy;
startup has a 45-second grace period. A destination outage can also make the
VPN unhealthy. Health does not establish authenticated SSH access.

## Build and publish an image

Build from a tested source revision. Set `VPN_IMAGE` to your registry path and
version; keep consumer settings and credentials outside the image:

```sh
export VPN_IMAGE=registry.example.com/networking/l2tp-ipsec-vpn-sidecar:VERSION
docker login registry.example.com
docker buildx build --platform linux/amd64,linux/arm64 --tag "$VPN_IMAGE" --push .
```

Replace the example registry and version before publishing. Set the same image
reference in the deployment's `.env`, or use the published digest. Registry
authentication on the deployment host belongs to the account running Docker.
The runtime Compose file has no build step and needs no source checkout.

For a local build from this checkout, set `VPN_IMAGE` in `.env` to a local tag:

```sh
docker compose -f compose.yaml -f compose.build.yaml build vpn
docker compose up -d --wait --wait-timeout 120
```

The explicit build override is for development; deploy only `compose.yaml`.

## SSH and Ansible

Adapt `../../examples/ssh_config.example` to the deployment directory, Ubuntu SSH
host alias, destination, remote user, and identity. Reuse existing host-key
verification. Verify authenticated access:

```sh
ssh -F /absolute/path/to/project/inventory/vpn.ssh_config example-vpn-jump hostname
```

The ProxyCommand executes `nc` inside `application`. The workstation SSH
client handles destination authentication; no destination private key is
copied into the containers. The example assumes noninteractive sudo access to
Docker on the Ubuntu host. Provision that deliberately; Docker access grants
host-level control.

Adapt `../../examples/ansible-overlay.yaml` and load it after the native inventory.
Its ProxyJump is for devices behind the destination jump server. For the jump
server itself, use its SSH alias or a direct ProxyCommand instead. Playbook
connection variables can override inventory settings.

## Ansible deployment

This repository is an Ansible collection providing the role
`vpn_sidecar.client.vpn_sidecar` from `roles/vpn_sidecar`. Install a pinned
collection dependency with `ansible-galaxy`, then load the role normally from
the consumer playbook. Consumers own their inventory, settings, and encrypted
credentials. See [backend role source](../../roles/vpn_sidecar/tasks/main.yaml).

## Container watchdog

Use the [shared watchdog container](../../watchdog/README.md) for automatic VPN
recovery and forwarding namespace repair. The profile launcher and Ansible role
include it in the runtime Compose project. Direct Compose deployments must add
`watchdog/compose.yaml` from the shared source and supply its non-secret path
settings; see the watchdog guide for image build and lifecycle commands.

The watchdog checks every 30 seconds and has a 300-second recovery cooldown.
It stops forwarding, recreates an unhealthy VPN, waits for health, then
recreates forwarding. Failed recovery leaves forwarding stopped. Explicitly
stopped VPNs stay stopped. The image includes its Docker client; no watchdog
executable, systemd service, or timer is installed on the Linux host.

## Local service tunnels

Requires Python 3.9+ and OpenSSH. Copy `../../examples/services.json` into the project
repository and edit its SSH config path, target alias, and port mappings. A
relative SSH config path is resolved against the JSON file. All local forwards
bind 127.0.0.1; remote hosts are IPv4 addresses or DNS names.

```sh
/path/to/l2tp-ipsec-vpn-sidecar/scripts/vpn-services /path/to/project/services.json command
/path/to/l2tp-ipsec-vpn-sidecar/scripts/vpn-services /path/to/project/services.json start
/path/to/l2tp-ipsec-vpn-sidecar/scripts/vpn-services /path/to/project/services.json status
/path/to/l2tp-ipsec-vpn-sidecar/scripts/vpn-services /path/to/project/services.json stop
```

`command` prints the SSH invocation without connecting. On macOS, `start`
registers a per-user LaunchAgent for the current login session. It reconnects
when SSH exits, with a 15-second retry throttle. Keep the source checkout,
Python interpreter, and config file at their installed paths. Use `restart`
after changing mappings. `stop` removes only that instance's agent and tunnel,
leaving the VPN running. The Mac must be awake and able to reach the Ubuntu host.

Agent state is under `~/Library/Application Support/vpn-sidecar/<name>/`;
logs are under `~/Library/Logs/vpn-sidecar/<name>/`. Logs contain SSH diagnostics.
`foreground` works on macOS and Linux; automatic supervision is provided only
for macOS. Linux foreground control sockets use `~/.local/state/vpn-sidecar/`.

## Validation and releases

```sh
uv run --no-project python -m unittest discover -s ../../tests -v
VPN_IMAGE=validation:local VPN_SERVER=203.0.113.10 COMPOSE_PROJECT_NAME=validation-vpn VPN_HEALTH_HOST=192.0.2.10 docker compose config --quiet
```

Tests execute real watchdog decisions against fake Docker/host tools and check
forwarding configuration. They require neither credentials nor a live VPN.
Live acceptance on Ubuntu must additionally verify VPN health, authenticated
SSH, private DNS when required, localhost service access, and recovery after a
controlled tunnel interruption. Template validation cannot prove negotiation
with a particular VPN server.

Tag releases after validation. Build locally on Ubuntu initially; a private
registry can distribute versioned images later. Do not automatically update
running instances from the source checkout.
