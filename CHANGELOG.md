# Changelog

## Unreleased

- Remove automatic deployment rollback and temporary snapshots.
- Preserve original deployment errors and watchdog pause/resume handling.

- Package the role as the standard `vpn_sidecar.client` Ansible collection.
- Replace custom consumer role bootstrapping with Galaxy dependency installation.

- Add a shared Ansible deployment role with optional migration and SSH checks.
- Document pinned role consumption separate from the runtime package version.

- Provide a reusable L2TP/IPsec VPN container.
- Require unique Compose project names and configurable private TCP probes.
- Provide watchdog systemd instances with isolated recovery state.
- Provide configurable localhost SSH tunnels with macOS supervision.
- Include mocked recovery and forwarding-configuration tests.
