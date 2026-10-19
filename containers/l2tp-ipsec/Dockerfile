FROM ubuntu:24.04

ENV DEBIAN_FRONTEND=noninteractive

RUN apt-get update \
    && apt-get install --yes --no-install-recommends \
        ca-certificates \
        dnsmasq-base \
        iproute2 \
        iptables \
        libcharon-extra-plugins \
        libstrongswan-standard-plugins \
        ppp \
        strongswan \
        xl2tpd \
    && rm -rf /var/lib/apt/lists/*

COPY scripts/docker-entrypoint.sh /usr/local/sbin/docker-entrypoint.sh
COPY scripts/ppp-ip-up /etc/ppp/ip-up.d/99-vpn-dns
COPY scripts/vpn-healthcheck /usr/local/bin/vpn-healthcheck
COPY scripts/vpn-tunnelcheck /usr/local/bin/vpn-tunnelcheck
COPY scripts/vpn-servicecheck /usr/local/bin/vpn-servicecheck

RUN chmod 0755 \
        /usr/local/sbin/docker-entrypoint.sh \
        /etc/ppp/ip-up.d/99-vpn-dns \
        /usr/local/bin/vpn-healthcheck /usr/local/bin/vpn-tunnelcheck /usr/local/bin/vpn-servicecheck

ENTRYPOINT ["/usr/local/sbin/docker-entrypoint.sh"]
