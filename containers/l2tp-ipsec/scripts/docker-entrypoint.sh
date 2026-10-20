#!/bin/sh
set -eu

if [ "${VPN_RECOVERY_OWNER:-standalone}" = controller ] && [ -n "${VPN_DIAGNOSTIC_DIR:-}" ]; then
 mkdir -p /run/vpn
 mkfifo /run/vpn/diagnostic-pipe
 vpn-diagnostic-filter < /run/vpn/diagnostic-pipe >/dev/null 2>&1 &
 exec > /run/vpn/diagnostic-pipe 2>&1
 # charon otherwise logs to syslog, which is absent in this container.
 cat > /etc/strongswan.d/charon-vpnmodo-logging.conf <<'CONFIG'
charon {
 filelog {
  stdout {
   default = 1
   flush_line = yes
  }
 }
 syslog {
  daemon { default = -1 }
  auth { default = -1 }
 }
}
CONFIG
fi

log() {
    printf '%s %s\n' "$(date -u +%Y-%m-%dT%H:%M:%SZ)" "$*"
}

read_secret() {
    variable_name=$1
    file_variable_name="${variable_name}_FILE"
    eval "variable_value=\${$variable_name:-}"
    eval "file_value=\${$file_variable_name:-}"

    if [ -n "$variable_value" ] && [ -n "$file_value" ]; then
        log "Set only $variable_name or $file_variable_name, not both"
        exit 1
    fi
    if [ -n "$file_value" ]; then
        if [ ! -r "$file_value" ]; then
            log "Cannot read $file_variable_name path: $file_value"
            exit 1
        fi
        # Command substitution removes the conventional trailing newline while
        # retaining meaningful spaces in passwords and pre-shared keys.
        variable_value=$(sed -e '' "$file_value")
    fi
    if [ -z "$variable_value" ]; then
        log "$variable_name is required"
        exit 1
    fi
    newline='
'
    carriage_return=$(printf '\r')
    case "$variable_value" in
        *"$newline"*|*"$carriage_return"*)
            log "$variable_name must be a single line"
            exit 1
            ;;
    esac
    printf '%s' "$variable_value"
}

quote_value() {
    printf '%s' "$1" | sed -e 's/\\/\\\\/g' -e 's/"/\\"/g'
}

cleanup() {
    trap - EXIT INT TERM
    log "VPN stopping"
    rm -f /run/vpn/ready
    if [ -p /var/run/xl2tpd/l2tp-control ]; then
        printf '%s\n' 'd vpn' > /var/run/xl2tpd/l2tp-control 2>/dev/null || true
    fi
    ipsec down l2tp-psk >/dev/null 2>&1 || true
    ipsec stop >/dev/null 2>&1 || true
    [ -z "${DNSMASQ_PID:-}" ] || kill "$DNSMASQ_PID" 2>/dev/null || true
    [ -z "${XL2TPD_PID:-}" ] || kill "$XL2TPD_PID" 2>/dev/null || true
}
trap cleanup EXIT INT TERM
KEYINGTRIES=%forever
DPDACTION=restart
CLOSEACTION=restart
REDIAL=yes
PPP_IP_DEFAULT=
if [ "${VPN_RECOVERY_OWNER:-standalone}" = controller ]; then
    # Controller-owned profiles have no competing Docker/IKE/L2TP restart loop.
    KEYINGTRIES=1
    DPDACTION=clear
    CLOSEACTION=none
    REDIAL=no
    PPP_IP_DEFAULT=noipdefault
    trap cleanup EXIT
    trap 'exit 0' INT TERM
fi

: "${VPN_SERVER:?VPN_SERVER is required}"
VPN_USERNAME=$(read_secret VPN_USERNAME)
VPN_PASSWORD=$(read_secret VPN_PASSWORD)
VPN_PSK=$(read_secret VPN_PSK)
VPN_FORCE_ENCAPS=${VPN_FORCE_ENCAPS:-yes}
VPN_IKE_PROPOSALS=${VPN_IKE_PROPOSALS:-aes256-sha256-modp2048,aes256-sha1-modp2048,aes256-sha1-modp1024,aes128-sha1-modp1024,3des-sha1-modp1024}
VPN_ESP_PROPOSALS=${VPN_ESP_PROPOSALS:-aes256-sha256,aes256-sha1,aes128-sha1,3des-sha1}

case "$VPN_FORCE_ENCAPS" in
    yes|no) ;;
    *) log "VPN_FORCE_ENCAPS must be yes or no"; exit 1 ;;
esac

VPN_SERVER_IP=$(getent ahostsv4 "$VPN_SERVER" | awk 'NR == 1 { print $1 }')
if [ -z "$VPN_SERVER_IP" ]; then
    log "Could not resolve VPN_SERVER: $VPN_SERVER"
    exit 1
fi

ORIGINAL_DEFAULT=$(ip -4 route show default | awk 'NR == 1 { print }')
ORIGINAL_GATEWAY=$(printf '%s\n' "$ORIGINAL_DEFAULT" | awk '{ for (i=1; i<=NF; i++) if ($i == "via") print $(i+1) }')
ORIGINAL_DEVICE=$(printf '%s\n' "$ORIGINAL_DEFAULT" | awk '{ for (i=1; i<=NF; i++) if ($i == "dev") print $(i+1) }')
if [ -z "$ORIGINAL_GATEWAY" ] || [ -z "$ORIGINAL_DEVICE" ]; then
    log "The container needs a default route with a gateway"
    exit 1
fi

VPN_USERNAME_ESCAPED=$(quote_value "$VPN_USERNAME")
VPN_PASSWORD_ESCAPED=$(quote_value "$VPN_PASSWORD")
VPN_PSK_ESCAPED=$(quote_value "$VPN_PSK")

umask 077
mkdir -p /etc/strongswan.d /etc/xl2tpd /var/run/xl2tpd /run/vpn
rm -f /var/run/xl2tpd/l2tp-control /run/vpn/ready

# Allocate a fresh local NAT-T source port whenever the container starts. This
# avoids stale UDP 4500 mappings after an IKEv1/L2TP session expires. The peer
# is still contacted on the standard NAT-T destination port, UDP 4500.
cat > /etc/strongswan.d/charon-random-nat-t-port.conf <<'EOF'
charon {
    port_nat_t = 0
}
EOF

cat > /etc/ipsec.conf <<EOF
config setup
    uniqueids=no

conn l2tp-psk
    keyexchange=ikev1
    authby=secret
    type=transport
    left=%defaultroute
    leftprotoport=17/1701
    right=$VPN_SERVER_IP
    rightprotoport=17/1701
    ike=$VPN_IKE_PROPOSALS
    esp=$VPN_ESP_PROPOSALS
    forceencaps=$VPN_FORCE_ENCAPS
    keyingtries=$KEYINGTRIES
    dpdaction=$DPDACTION
    dpddelay=30s
    closeaction=$CLOSEACTION
    auto=add
EOF

cat > /etc/ipsec.secrets <<EOF
: PSK "$VPN_PSK_ESCAPED"
EOF

cat > /etc/xl2tpd/xl2tpd.conf <<EOF
[global]
port = 1701

[lac vpn]
lns = $VPN_SERVER_IP
pppoptfile = /etc/ppp/options.l2tpd.client
length bit = yes
redial = $REDIAL
redial timeout = 5
max redials = 6
EOF

cat > /etc/ppp/options.l2tpd.client <<EOF
name "$VPN_USERNAME_ESCAPED"
$PPP_IP_DEFAULT
noauth
refuse-eap
logfd 2
ipcp-accept-local
ipcp-accept-remote
mtu 1400
mru 1400
nodefaultroute
usepeerdns
noccp
connect-delay 5000
EOF

cat > /etc/ppp/chap-secrets <<EOF
"$VPN_USERNAME_ESCAPED" * "$VPN_PASSWORD_ESCAPED" *
EOF
cp /etc/ppp/chap-secrets /etc/ppp/pap-secrets

cat > /etc/dnsmasq-vpn.conf <<'EOF'
no-resolv
no-hosts
bind-interfaces
listen-address=127.0.0.1
cache-size=1000
conf-file=/run/vpn/dns-servers.conf
EOF

log "Starting IPsec connection to $VPN_SERVER_IP"
if [ "${VPN_RECOVERY_OWNER:-standalone}" = controller ] && [ -n "${VPN_DIAGNOSTIC_DIR:-}" ]; then
 ipsec start --nofork &
else
 ipsec start
fi
attempt=0
until ipsec up l2tp-psk; do
    attempt=$((attempt + 1))
    if [ "$attempt" -ge 6 ]; then
        log "IPsec did not connect after $attempt attempts"
        exit 1
    fi
    sleep 5
done

# Preserve the only permitted non-tunnel path before replacing the default route.
ip route replace "$VPN_SERVER_IP/32" via "$ORIGINAL_GATEWAY" dev "$ORIGINAL_DEVICE"

# Kill switch: only IKE/IPsec may leave through the original interface. Traffic
# from this container and all containers sharing its network namespace must use
# ppp0 after the tunnel is established.
iptables -F OUTPUT
iptables -P OUTPUT DROP
iptables -A OUTPUT -o lo -j ACCEPT
if [ "${VPN_RECOVERY_OWNER:-standalone}" != controller ]; then
    iptables -A OUTPUT -m conntrack --ctstate ESTABLISHED,RELATED -j ACCEPT
fi
iptables -A OUTPUT -o ppp0 -j ACCEPT
iptables -A OUTPUT -o "$ORIGINAL_DEVICE" -d "$VPN_SERVER_IP" -p udp --dport 500 -j ACCEPT
iptables -A OUTPUT -o "$ORIGINAL_DEVICE" -d "$VPN_SERVER_IP" -p udp --dport 4500 -j ACCEPT
iptables -A OUTPUT -o "$ORIGINAL_DEVICE" -d "$VPN_SERVER_IP" -p esp -j ACCEPT
iptables -A OUTPUT -o "$ORIGINAL_DEVICE" -d "$VPN_SERVER_IP" -p udp --dport 1701 \
    -m policy --dir out --pol ipsec -j ACCEPT

log "Starting L2TP and PPP"
xl2tpd -D &
XL2TPD_PID=$!

attempt=0
until [ -p /var/run/xl2tpd/l2tp-control ]; do
    attempt=$((attempt + 1))
    if [ "$attempt" -ge 30 ]; then
        log "xl2tpd control pipe was not created"
        exit 1
    fi
    sleep 1
done
printf '%s\n' 'c vpn' > /var/run/xl2tpd/l2tp-control

attempt=0
until ip link show ppp0 2>/dev/null | grep -q 'UP' \
    && ip -4 address show dev ppp0 2>/dev/null | grep -q 'inet '; do
    attempt=$((attempt + 1))
    if [ "$attempt" -ge 60 ]; then
        log "PPP interface ppp0 did not become ready with an IPv4 address"
        exit 1
    fi
    sleep 1
done

ip route replace default dev ppp0

: > /run/vpn/dns-servers.conf
if [ -n "${VPN_DNS:-}" ]; then
    printf '%s\n' "$VPN_DNS" | tr ',' '\n' | while IFS= read -r dns_server; do
        [ -z "$dns_server" ] || printf 'server=%s\n' "$dns_server"
    done > /run/vpn/dns-servers.conf
else
    attempt=0
    while [ ! -s /run/vpn/ppp-dns ] && [ "$attempt" -lt 10 ]; do
        attempt=$((attempt + 1))
        sleep 1
    done
    if [ -s /run/vpn/ppp-dns ]; then
        while IFS= read -r dns_server; do
            [ -z "$dns_server" ] || printf 'server=%s\n' "$dns_server"
        done < /run/vpn/ppp-dns > /run/vpn/dns-servers.conf
    else
        log "PPP supplied no DNS servers; configure VPN_DNS explicitly"
        exit 1
    fi
fi

dnsmasq --keep-in-foreground --conf-file=/etc/dnsmasq-vpn.conf &
DNSMASQ_PID=$!
touch /run/vpn/ready
log "VPN is ready; shared network traffic now uses ppp0"

while :; do
    # Waiting for a background sleep lets the shell handle TERM immediately.
    # A foreground sleep defers the trap beyond the controller's stop budget,
    # so Docker can kill the container before L2TP/IPsec cleanup runs.
    sleep 5 &
    wait "$!"
    if ! ip link show ppp0 2>/dev/null | grep -q 'UP'; then
        log "ppp0 is no longer up; exiting for container restart"
        exit 1
    fi
    if ! kill -0 "$XL2TPD_PID" 2>/dev/null || ! kill -0 "$DNSMASQ_PID" 2>/dev/null; then
        log "A VPN support process exited; restarting the container"
        exit 1
    fi
done
