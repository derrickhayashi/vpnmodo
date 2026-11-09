# OpenConnect GlobalProtect backend

This image implements the experimental `openconnect-globalprotect` managed
backend. It runs a bounded noninteractive OpenConnect `gp` attempt with mandatory
TUN-based packet filtering and explicit TLS policy. Verification is enabled by
default; profiles can explicitly opt into accepting any server certificate. See the
[GlobalProtect guide](../../docs/globalprotect.md) for profile format, scope,
build instructions and validation limits.

Use a matching source-built controller and an authorized Linux environment with
`/dev/net/tun`. This image has no standalone legacy launcher or Compose project;
the controller supplies the per-profile manifest and protected credential files.
