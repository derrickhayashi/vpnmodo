#!/bin/sh
# Run only against explicitly selected local test images. No gateway is contacted.
set -eu
[ "$#" -gt 0 ] || { echo 'Usage: sh tests/smoke_logging_images.sh IMAGE [IMAGE ...]' >&2; exit 2; }
for image in "$@"; do
 docker run --rm --network none --entrypoint sh "$image" -ceu '
  mkdir -p /tmp/diagnostics
  chmod 700 /tmp/diagnostics
  touch /tmp/diagnostics/events /tmp/diagnostics/previous /tmp/diagnostics/lock
  chmod 600 /tmp/diagnostics/*
  chown -R 1000:1000 /tmp/diagnostics
  export VPN_DIAGNOSTIC_DIR=/tmp/diagnostics
  vpn-diagnostic established
  vpn-diagnostic peer_delete
  [ "$(wc -l < /tmp/diagnostics/events)" -eq 2 ]
  [ "$(stat -c "%u:%a" /tmp/diagnostics/events)" = 1000:600 ]
  if vpn-diagnostic synthetic-secret; then exit 1; fi
  if command -v vpn-diagnostic-filter >/dev/null 2>&1; then
   printf "%s\n" "unrecognized synthetic-secret" "peer not responding" "rekeying IKE_SA synthetic failed" | vpn-diagnostic-filter
   [ "$(wc -l < /tmp/diagnostics/events)" -eq 4 ]
  fi
  if grep -q synthetic-secret /tmp/diagnostics/events; then exit 1; fi
  printf "%s\n" "PASS: Linux diagnostic writer, flock, private ownership and fixed-code capture"
 '
done
