FROM ubuntu:24.04
ENV DEBIAN_FRONTEND=noninteractive
RUN apt-get update && apt-get install -y --no-install-recommends \
    openconnect openssl python3 iproute2 iptables dnsmasq-base netcat-openbsd util-linux \
    && rm -rf /var/lib/apt/lists/*
COPY scripts/ /usr/local/bin/
RUN chmod 755 /usr/local/bin/vpn-*
ARG VERSION=dev
ARG REVISION=unknown
LABEL org.opencontainers.image.source="https://github.com/derrickhayashi/vpnmodo" org.opencontainers.image.version=$VERSION org.opencontainers.image.revision=$REVISION
ENTRYPOINT ["/usr/local/bin/vpn-managed-globalprotect"]
