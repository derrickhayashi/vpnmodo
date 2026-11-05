#!/bin/sh
# Compare the initial IKEv1 proposal with/without managed behavior. All traffic
# stays on loopback inside disposable containers; no gateway or real credentials.
set -eu
[ "$#" -eq 1 ] || { echo 'Usage: sh tests/smoke_xauth_lifetime.sh XAUTH_IMAGE' >&2; exit 2; }
image=$1
arch=$(docker image inspect --format '{{.Architecture}}' "$image")
case "$arch" in arm64|amd64) ;; *) echo 'Unsupported test image architecture' >&2; exit 2;; esac
probe_dir=$(mktemp -d /tmp/vpnmodo-ike-lifetime.XXXXXX)
trap 'rm -rf "$probe_dir"' EXIT INT TERM
CGO_ENABLED=0 GOOS=linux GOARCH="$arch" go build -trimpath -o "$probe_dir/probe" ./tests/ike-lifetime-probe
for mode in 0 1; do
 docker run --rm --network none --cap-add NET_ADMIN \
  -e PROBE_MANAGED="$mode" -e PROBE_EXPECT=36600 \
  --mount "type=bind,src=$probe_dir/probe,dst=/probe,readonly" \
  --entrypoint /probe "$image"
done
