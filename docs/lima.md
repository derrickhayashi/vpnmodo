# Lima environment

`vpnmodo setup` prepares the single managed Lima VM `vpn-vm`. On an Apple Silicon
Mac it installs Lima if needed, creates or starts Ubuntu, installs Docker, pulls
the release images and starts the controller. It returns after readiness checks.

Registry authentication is optional and separate from VPN configuration. Public
images need no auth file:

```sh
vpnmodo setup
```

The current release images are private GHCR packages. For those, supply an
owner-only JSON file containing GitHub `username` and `token`:

```sh
vpnmodo setup --registry-auth "$HOME/vpnmodo-private/registry-auth.json"
```

The token needs `read:packages` and package access. See the complete
[installation instructions](../README.md#installation). There are no registry
credential prompts. Without a supplied file, setup reuses saved auth if available
or prepares anonymous image pulls. Private pulls still require valid access.

Setup does not read VPN profiles. Create one from its separate JSON file:

```sh
vpnmodo profile create work --file "$HOME/vpnmodo-private/work.json"
vpnmodo up work
```

The VPN JSON contains settings and username/password/PSK for either supported
backend. Only XAuth connection MFA is prompted interactively.

Lima manages SSH keys, the guest user, connection details and the generated SSH
configuration. No fixed VM IP, host registration or manual forwarding setup is
needed. Repeated setup reuses this environment and existing profiles. Correct a
registry login by editing its separate auth JSON and rerunning the same setup
command with `--registry-auth`.

Private setup files are cached under
`~/Library/Application Support/vpnmodo/setup/vpn-vm/` on macOS. Keep registry auth
and VPN input files protected outside Git. See [usage](../README.md#usage).
