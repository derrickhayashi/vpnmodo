# Public-image setup and optional configuration

Status: agreed interface; implementation is pending the configuration fields.
This describes the next setup interface, not commands supported by the current
released CLI. See the [README](../../README.md#installation) for installation.

## Setup interface

The target setup flow assumes publicly readable release images. Users do not
supply `registry-auth.json`, a registry username or a registry token.

Setup remains usable without a configuration file:

```sh
vpnmodo setup
```

Users can optionally supply setup configuration with the planned command:

```sh
vpnmodo setup --config config.json
```

This option belongs to `setup` only. The file's fields and their behavior are
undecided. Do not invent settings or ship an option that silently ignores its
input; define its behavior before implementing it.

Setup continues to prepare the single managed Lima VM, `vpn-vm`, and its
controller. The optional configuration does not introduce additional VM paths.

## VPN profiles remain separate

The setup configuration is separate from the existing VPN profile JSON. Profile
import still supplies the VPN settings and credentials, and connection commands
still handle any required XAuth MFA. Defining setup configuration does not
change that profile contract.

## Implementation boundaries

The existing global `vpnmodo --config FILE` selects internal host-registry state.
It is not the planned setup configuration input. Implementing the setup option
must not treat the supplied configuration as mutable host-registry state.

Public image availability and anonymous pulls must be verified before publishing
this as the released installation flow. Maintainer authentication for pushing
release images to GHCR remains part of the [release process](../releases.md).
