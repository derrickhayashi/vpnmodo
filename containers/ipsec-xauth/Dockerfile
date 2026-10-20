FROM alpine:3.22 AS builder
RUN apk add --no-cache build-base curl bzip2 linux-headers openssl-dev gmp-dev
WORKDIR /build
RUN curl --fail --location --retry 2 --max-time 120 https://download.strongswan.org/strongswan-5.9.14.tar.bz2 -o source.tar.bz2 && tar -xjf source.tar.bz2
WORKDIR /build/strongswan-5.9.14
RUN sed -i '/case XAUTH_NEXT_PIN:/i\            case XAUTH_PASSCODE:' src/libcharon/plugins/xauth_generic/xauth_generic.c && sed -i 's/job, HALF_OPEN_IKE_SA_TIMEOUT);/job, lib->settings->get_int(lib->settings, "%s.half_open_timeout", HALF_OPEN_IKE_SA_TIMEOUT, lib->ns));/' src/libcharon/sa/ikev1/tasks/aggressive_mode.c && ./configure --prefix=/opt/strongswan --sysconfdir=/etc --enable-cmd --enable-xauth-generic --enable-openssl && make -j2 && make install && test -x /opt/strongswan/sbin/charon-cmd
COPY managed-client.patch /build/managed-client.patch
RUN patch -p1 < /build/managed-client.patch && make -j2 && make install
FROM alpine:3.22
RUN apk add --no-cache expect flock iproute2 iptables dnsmasq netcat-openbsd ca-certificates openssl gmp
COPY --from=builder /opt/strongswan /opt/strongswan
COPY --from=builder /etc/strongswan.conf /etc/strongswan.conf
COPY --from=builder /etc/strongswan.d /etc/strongswan.d
ENV PATH="/opt/strongswan/sbin:/opt/strongswan/bin:${PATH}"
COPY managed/ /usr/local/bin/
RUN chmod 755 /usr/local/bin/vpn-*
ENTRYPOINT ["charon-cmd"]

ARG VERSION=dev
ARG REVISION=unknown
LABEL org.opencontainers.image.version=$VERSION org.opencontainers.image.revision=$REVISION
