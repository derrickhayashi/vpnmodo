FROM ubuntu:24.04
ENV DEBIAN_FRONTEND=noninteractive
RUN apt-get update && apt-get install -y --no-install-recommends ca-certificates docker.io docker-compose-v2 iproute2 iptables kmod openssh-server python3 sudo tcpdump && rm -rf /var/lib/apt/lists/* && useradd --create-home --uid 1001 --shell /bin/bash labuser && passwd -d labuser && usermod -aG docker labuser && printf 'labuser ALL=(ALL) NOPASSWD:ALL\n' > /etc/sudoers.d/labuser && chmod 0440 /etc/sudoers.d/labuser
RUN cp "$(command -v tcpdump)" /usr/local/bin/lab-capture
COPY start /usr/local/bin/start-lab
RUN chmod 0755 /usr/local/bin/start-lab
ENTRYPOINT ["/usr/local/bin/start-lab"]
