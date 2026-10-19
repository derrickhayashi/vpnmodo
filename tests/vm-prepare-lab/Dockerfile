FROM ubuntu:24.04

ENV DEBIAN_FRONTEND=noninteractive container=docker

# Docker deliberately is absent: vm_prepare installs and starts the real engine.
RUN apt-get update \
    && apt-get install -y --no-install-recommends \
       systemd systemd-sysv dbus python3 python3-venv python3-apt sudo \
       ca-certificates kmod iproute2 util-linux \
    && python3 -m venv /opt/ansible \
    && /opt/ansible/bin/pip install --no-cache-dir ansible-core==2.21.5 \
    && useradd --create-home --shell /bin/bash labdirect \
    && useradd --create-home --shell /bin/bash labsudo \
    && useradd --create-home --shell /bin/bash labdenied \
    && printf 'labsudo ALL=(ALL) NOPASSWD: ALL\n' > /etc/sudoers.d/lab-preauthorized \
    && chmod 0440 /etc/sudoers.d/lab-preauthorized \
    && mkdir -p /etc/docker /opt/collections/ansible_collections/vpn_sidecar \
    && printf '{"storage-driver":"vfs","ip6tables":false}\n' > /etc/docker/daemon.json \
    && ln -s /src /opt/collections/ansible_collections/vpn_sidecar/client \
    && systemctl mask systemd-modules-load.service systemd-sysctl.service \
       systemd-udevd.service systemd-remount-fs.service

COPY inside.py /opt/lab/inside.py
STOPSIGNAL SIGRTMIN+3
CMD ["/usr/bin/setpriv", "--bounding-set=-sys_module", "--", "/sbin/init"]
