# Public-image setup and optional configuration

Status: the optional setup flag is implemented as a no-op; configuration fields
remain undecided. See the [README](../../README.md#installation) for published
release installation instructions.

## Setup interface

The target setup flow assumes publicly readable release images. Users do not
supply `registry-auth.json`, a registry username or a registry token.

Setup remains usable without a configuration file:

```sh
vpnmodo setup
```

Users can optionally pass a setup configuration path:

```sh
vpnmodo setup --config config.json
```

This option belongs to `setup` only. For now, its value is accepted but ignored:
setup does not open, validate, copy, save or otherwise act on the file. The file
does not need to exist. Configuration fields and their behavior remain undecided.

Setup continues to prepare the single managed Lima VM, `vpn-vm`, and its
controller. The optional configuration does not introduce additional VM paths.

## VPN profiles remain separate

The setup configuration is separate from the existing VPN profile JSON. Profile
import still supplies the VPN settings and credentials, and connection commands
still handle any required XAuth MFA. Defining setup configuration does not
change that profile contract.

## Implementation boundaries

The existing global `vpnmodo --config FILE` selects internal host-registry state.
It is separate from the setup-local option. The supplied setup configuration
does not select or overwrite that internal host-registry state.

Public image availability and anonymous pulls must be verified before publishing
this as the released installation flow. Maintainer authentication for pushing
release images to GHCR remains part of the [release process](../releases.md).
