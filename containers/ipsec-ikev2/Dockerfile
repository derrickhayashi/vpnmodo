FROM ubuntu:24.04
ENV DEBIAN_FRONTEND=noninteractive
RUN apt-get update && apt-get install -y --no-install-recommends \
    strongswan-charon strongswan-swanctl libcharon-extauth-plugins libcharon-extra-plugins libstrongswan-extra-plugins \
    python3 python3-venv iproute2 iptables dnsmasq-base netcat-openbsd util-linux \
    && rm -rf /var/lib/apt/lists/*
COPY requirements.txt /tmp/vici-requirements.txt
RUN python3 -m venv /opt/vici && /opt/vici/bin/pip install --no-cache-dir --only-binary=:all: --require-hashes -r /tmp/vici-requirements.txt && rm /tmp/vici-requirements.txt
COPY strongswan.conf /etc/strongswan.conf
COPY scripts/ /usr/local/bin/
RUN chmod 755 /usr/local/bin/vpn-* && rm -rf /etc/swanctl/x509ca/*
ARG VERSION=dev
ARG REVISION=unknown
LABEL org.opencontainers.image.source="https://github.com/derrickhayashi/vpnmodo" org.opencontainers.image.version=$VERSION org.opencontainers.image.revision=$REVISION
ENTRYPOINT ["/usr/local/bin/vpn-managed-ikev2"]
