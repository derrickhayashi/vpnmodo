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
        util-linux \
        strongswan \
        xl2tpd \
    && rm -rf /var/lib/apt/lists/*

COPY scripts/docker-entrypoint.sh /usr/local/sbin/docker-entrypoint.sh
COPY scripts/ppp-ip-up /etc/ppp/ip-up.d/99-vpn-dns
COPY scripts/vpn-diagnostic scripts/vpn-diagnostic-filter /usr/local/bin/
COPY scripts/vpn-tunnelcheck /usr/local/bin/vpn-tunnelcheck
COPY scripts/vpn-servicecheck /usr/local/bin/vpn-servicecheck

RUN chmod 0755 \
        /usr/local/bin/vpn-diagnostic /usr/local/bin/vpn-diagnostic-filter \
        /usr/local/sbin/docker-entrypoint.sh \
        /etc/ppp/ip-up.d/99-vpn-dns \
        /usr/local/bin/vpn-tunnelcheck /usr/local/bin/vpn-servicecheck

ENTRYPOINT ["/usr/local/sbin/docker-entrypoint.sh"]

ARG VERSION=dev
ARG REVISION=unknown
LABEL org.opencontainers.image.source="https://github.com/derrickhayashi/vpnmodo" org.opencontainers.image.version=$VERSION org.opencontainers.image.revision=$REVISION
