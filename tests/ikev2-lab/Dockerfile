FROM vpnmodo-ikev2:dev
RUN apt-get update && apt-get install -y --no-install-recommends openssl tcpdump && rm -rf /var/lib/apt/lists/*
COPY gateway /usr/local/bin/lab-gateway
COPY sink.py /usr/local/bin/sink.py
ENTRYPOINT ["/usr/local/bin/lab-gateway"]
