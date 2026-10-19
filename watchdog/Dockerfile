FROM docker:29.9.0-cli
RUN apk add --no-cache util-linux
COPY vpn-watchdog watchdog-loop /usr/local/bin/
RUN chmod 0755 /usr/local/bin/vpn-watchdog /usr/local/bin/watchdog-loop
ENTRYPOINT ["/usr/local/bin/watchdog-loop"]
