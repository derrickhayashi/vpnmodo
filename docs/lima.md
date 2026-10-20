# Managed local environment

The supported installation path is one Apple Silicon Mac and one local Lima VM.
The user installs the standalone `vpnmodo` executable and runs:

```sh
vpnmodo setup
```

Setup installs Lima using Homebrew if needed, creates Ubuntu, provisions Docker,
kernel support and the release images, starts the controller, and waits for it to
be ready. It finishes when the controller is ready. VPN settings remain in a JSON profile
configured separately with `vpnmodo profile create NAME --file PROFILE.json`.
Profile creation automatically uses this managed environment.

The provisioning YAML, image installer and pinned image manifest are embedded in
the executable. VM account creation, SSH keys and networking are handled by native
Lima. The CLI uses Lima's generated SSH configuration directly. Users do not
select a VM, register a host, transfer setup files or run separate activation.

The images are private GHCR packages. Setup prompts for a GitHub username and a
token with `read:packages`; that account must have package read access. Credentials
are saved with mode 0600 and supplied to the guest through a protected file or
SSH stdin. The Mac does not need Python or Docker.

Running setup again reuses the managed environment and existing profiles. It can
refresh saved image credentials and finish an interrupted image installation.
Private setup files live under the vpnmodo configuration directory, normally
`~/Library/Application Support/vpnmodo/setup/vpn-vm/` on macOS. To correct an
incorrect registry token, remove that directory's `ghcr-config.json` and run setup
again.

Use `vpnmodo up NAME` to connect, respond to MFA when required, and
`vpnmodo down NAME` to disconnect. See [installation and usage](../README.md).
