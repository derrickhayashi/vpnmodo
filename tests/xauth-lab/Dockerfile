FROM ubuntu:24.04
RUN apt-get update && apt-get install -y --no-install-recommends strongswan strongswan-swanctl libcharon-extra-plugins libstrongswan-extra-plugins python3 iproute2 iptables tcpdump dnsmasq && rm -rf /var/lib/apt/lists/*
RUN cp "$(command -v tcpdump)" /usr/local/bin/lab-capture
COPY gateway radius.py sink.py /usr/local/bin/
RUN chmod 755 /usr/local/bin/gateway
ENTRYPOINT ["/usr/local/bin/gateway"]
