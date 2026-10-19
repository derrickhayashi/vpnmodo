# Tailscale sidecar

A reusable Tailscale sidecar using the official image, kernel networking,
persistent enrollment, health-gated application
startup, and a shared network namespace with MagicDNS. The generic `vpn` and
`application` service names match the other backends in this repository.

The Docker engine needs `/dev/net/tun` and permission to grant `NET_ADMIN`.
The application image is a placeholder; consumer projects own their application,
credentials, hostnames, and deployment settings.

## Start a separate deployment

Keep the environment file in your consumer project. From this repository root:

```sh
cp containers/tailscale/.env.example /path/to/consumer/tailscale.env
chmod 600 /path/to/consumer/tailscale.env
# Edit that file: choose a unique project name and hostname, and optional auth key.
docker compose --env-file /path/to/consumer/tailscale.env -f containers/tailscale/compose.yaml config --quiet
docker compose --env-file /path/to/consumer/tailscale.env -f containers/tailscale/compose.yaml up -d vpn
docker compose --env-file /path/to/consumer/tailscale.env -f containers/tailscale/compose.yaml logs -f vpn
```

Set `TS_AUTHKEY` to an enrollment key from the
[Tailscale Keys page](https://login.tailscale.com/admin/settings/keys), or leave it
empty and follow the browser login URL in the logs. A single-use, non-ephemeral
key suits a persistent deployment. Approve the device if your tailnet requires it,
then clear the key from the environment file after enrollment.

```sh
docker compose --env-file /path/to/consumer/tailscale.env -f containers/tailscale/compose.yaml up -d application
docker compose --env-file /path/to/consumer/tailscale.env -f containers/tailscale/compose.yaml exec vpn tailscale status
docker compose --env-file /path/to/consumer/tailscale.env -f containers/tailscale/compose.yaml exec application nslookup target.example.ts.net
```

Use your target's full MagicDNS name. The health check confirms a tailnet IP;
verify actual target reachability and tailnet permissions separately.

## Application customization

Layer a consumer-owned Compose file after `compose.yaml` to set the `application`
image, command, working directory, environment, and mounts. For example:

```yaml
services:
  application:
    image: your-registry/your-application:tag
    command: ["your-command", "your-argument"]
```

The application inherits `network_mode: service:vpn`, the healthy dependency,
and the read-only MagicDNS resolver mount. Put any published application ports
on `vpn`, which owns the network namespace. Finish active jobs and recreate the
application when recreating `vpn` so it joins the new namespace.

Each Compose project owns a `tailscale-state` volume. Keep that volume to retain
enrollment across restarts; `down` preserves it, while `down --volumes` deletes
the identity. Start new deployments with their own enrollment and volume.

Normal internet traffic uses Docker networking. Set `TS_EXTRA_ARGS=--accept-routes`
to use approved subnet-router routes. No exit node or full-tunnel kill switch is
configured. `TS_ACCEPT_DNS=false` keeps the sidecar's resolver unchanged; the
application uses the supplied `100.100.100.100` resolver file.

This backend runs directly with Compose. `scripts/vpn`, the Ansible
`vpn_sidecar` role, the shared watchdog, and `ensure-vpn` support the IPsec
backends. Tailscale uses its daemon's connection management and Docker restart
policy. The upstream image needs no local Dockerfile or build override; set
`VPN_IMAGE` to a version tag or digest when you need a pinned image.

See [Tailscale's Docker parameters](https://tailscale.com/docs/features/containers/docker/docker-params)
for authentication, routing, state, and health-check settings.
