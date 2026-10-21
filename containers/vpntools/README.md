# vpntools container

`ghcr.io/derrickhayashi/vpnmodo/vpntools` provides the network tools used by
vpnmodo's proxied connections. It is separate from the `vpnmodo` CLI client
and the controller.

The controller starts one tools container per connected profile, sharing that
profile's VPN network namespace. Tools such as `nc` open destination sockets
through the tunnel. VPN credentials stay in the VPN backend; the tools container
receives no credentials, runs without capabilities and has a read-only filesystem.

The image uses Alpine's built-in network tools. Releases pin its exact digest.
The release manifest calls this runtime role `application`; its published image
name is `vpntools`. Use `vpnmodo` to manage it rather than starting it manually.
