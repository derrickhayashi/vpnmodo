FROM ubuntu:24.04
RUN apt-get update && apt-get install -y --no-install-recommends openssh-server python3 && rm -rf /var/lib/apt/lists/* && passwd -d root && mkdir -p /run/sshd /run/service
ENTRYPOINT ["sleep", "infinity"]
