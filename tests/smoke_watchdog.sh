#!/bin/sh
# Run on a disposable Linux Docker test project. No real VPN or credentials.
set -eu
source_root=$(CDPATH= cd -- "$(dirname "$0")/.." && pwd)
: "${VPN_WATCHDOG_IMAGE:?Set this to a built watchdog test image}"
smoke_root=$(mktemp -d /tmp/vpn-watchdog-smoke.XXXXXX)
export COMPOSE_PROJECT_NAME=watchdog-smoke-$(basename "$smoke_root" | tr '[:upper:].' '[:lower:]-')
export VPN_PROJECT_DIR=$smoke_root/runtime VPN_SOURCE_ROOT=$source_root
export VPN_CREDENTIALS_DIR=$smoke_root/runtime/secrets
export VPN_WATCHDOG_MODE=auto VPN_WATCHDOG_INTERVAL=1 VPN_WATCHDOG_COOLDOWN=0
export VPN_HEALTH_HOST=192.0.2.10 VPN_HEALTH_PORT=22
mkdir -p "$VPN_PROJECT_DIR/run" "$VPN_CREDENTIALS_DIR" "$smoke_root/health"
touch "$VPN_PROJECT_DIR/run/cli.lock"
# Exercise protected files owned by a different host UID, as in user deployments.
chown -R 65534:65534 "$VPN_PROJECT_DIR"
chmod 700 "$VPN_PROJECT_DIR" "$VPN_CREDENTIALS_DIR" "$VPN_PROJECT_DIR/run"
chmod 600 "$VPN_PROJECT_DIR/run/cli.lock"
compose() { docker compose -f "$VPN_PROJECT_DIR/compose.yaml" "$@"; }
cleanup() {
    result=$?
    trap - EXIT
    if [ -f "$VPN_PROJECT_DIR/compose.yaml" ]; then
        if [ "$result" -ne 0 ]; then compose logs watchdog >&2 || true; fi
        compose down --volumes >/dev/null 2>&1 || true
    fi
    rm -rf -- "$smoke_root"
    exit "$result"
}
trap cleanup EXIT
trap 'exit 130' INT
trap 'exit 143' TERM
cat > "$smoke_root/base.yaml" <<'YAML'
services:
  vpn:
    image: alpine:3.22
    command: [sh, -c, "touch /test-state/healthy; exec sleep infinity"]
    volumes:
      - ${VPN_PROJECT_DIR}/../health:/test-state
    healthcheck:
      test: [CMD, test, -f, /test-state/healthy]
      interval: 1s
      timeout: 1s
      retries: 1
  application:
    image: alpine:3.22
    command: [sleep, infinity]
    network_mode: service:vpn
    depends_on:
      vpn:
        condition: service_healthy
YAML
render() {
    docker compose -f "$smoke_root/base.yaml" -f "$source_root/watchdog/compose.yaml" config > "$VPN_PROJECT_DIR/compose.yaml"
}
wait_recreated() {
    service=$1 previous_id=$2
    attempt=0
    while [ "$attempt" -lt 45 ]; do
        current_id=$(compose ps -q "$service")
        if [ -n "$current_id" ] && [ "$current_id" != "$previous_id" ]; then return 0; fi
        attempt=$((attempt + 1)); sleep 1
    done
    echo "Timed out waiting for $service recreation" >&2; return 1
}
render
compose up -d --wait --wait-timeout 60
vpn_before=$(compose ps -q vpn)
app_before=$(compose ps -q application)
# While an update owns the lifecycle lock, no recovery may begin.
flock "$VPN_PROJECT_DIR/run/cli.lock" sh -c 'touch "$1"; sleep 3' sh "$smoke_root/locked" &
lock_pid=$!
while [ ! -f "$smoke_root/locked" ]; do sleep 1; done
rm "$smoke_root/health/healthy"
sleep 1
[ "$(compose ps -q vpn)" = "$vpn_before" ]
wait "$lock_pid"
wait_recreated vpn "$vpn_before"
wait_recreated application "$app_before"
echo 'PASS: unhealthy VPN and application recovered'
app_before=$(compose ps -q application)
compose up -d --no-deps --force-recreate vpn
wait_recreated application "$app_before"
vpn_id=$(compose ps -q vpn)
app_id=$(compose ps -q application)
[ "$(docker exec "$vpn_id" readlink /proc/self/ns/net)" = "$(docker exec "$app_id" readlink /proc/self/ns/net)" ]
echo 'PASS: application reattached to replacement VPN namespace'
export VPN_WATCHDOG_MODE=manual
render
compose up -d --no-deps --force-recreate watchdog
vpn_before=$(compose ps -q vpn)
app_before=$(compose ps -q application)
rm "$smoke_root/health/healthy"
attempt=0
until compose logs watchdog 2>/dev/null | grep -q 'manual reconnect'; do
    [ "$attempt" -lt 20 ] || { echo 'No manual reconnect report' >&2; exit 1; }
    attempt=$((attempt + 1)); sleep 1
done
[ "$(compose ps -q vpn)" = "$vpn_before" ]
[ "$(compose ps -q application)" = "$app_before" ]
echo 'PASS: manual mode reports failure without recreating siblings'
