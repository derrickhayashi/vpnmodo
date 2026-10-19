FROM vpnmodo-l2tp:dev
RUN apt-get update && apt-get install -y --no-install-recommends python3 && rm -rf /var/lib/apt/lists/*
COPY gateway /usr/local/bin/lab-gateway
COPY control /usr/local/bin/lab-control
RUN chmod 0755 /usr/local/bin/lab-gateway /usr/local/bin/lab-control
ENTRYPOINT ["/usr/local/bin/lab-gateway"]
