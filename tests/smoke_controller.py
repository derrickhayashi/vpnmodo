#!/usr/bin/env python3
"""Isolated Ubuntu-runner SSH/Docker acceptance. Never use customer deployments.

Requires an existing Linux Docker Engine/Compose, OpenSSH server, ssh-keygen and
passwordless sudo. Refuses existing vpnmodo storage/controller; creates ephemeral
SSH keys outside the checkout, pins its own server key, and removes test resources.
"""
import argparse
import getpass
import json
import os
from pathlib import Path
import shutil
import socket
import subprocess
import sys
import tempfile
import time
import uuid


def command(args, *, data=None, required=True):
    result = subprocess.run(args, input=data, capture_output=True, timeout=240)
    if required and result.returncode:
        raise RuntimeError("acceptance command failed: " + Path(str(args[0])).name)
    return result


def main():
    if not sys.platform.startswith("linux") or os.environ.get("VPNMODO_ISOLATED_TEST_HOST") != "1":
        raise RuntimeError("run only on an isolated Linux test VM with VPNMODO_ISOLATED_TEST_HOST=1")
    parser = argparse.ArgumentParser()
    parser.add_argument("--binary", required=True, type=Path)
    parser.add_argument("--image", required=True)
    options = parser.parse_args()
    data_root = Path.home() / ".local/share/vpnmodo"
    if data_root.exists() or data_root.is_symlink():
        raise RuntimeError("refusing existing vpnmodo data directory")
    if command(["docker", "container", "inspect", "vpnmodo-controller"], required=False).returncode == 0:
        raise RuntimeError("refusing an existing controller container")
    directory = Path(tempfile.mkdtemp(prefix="vpnmodo-ssh-"))
    token = uuid.uuid4().hex
    sshd = None
    legacy = "vpnmodo-legacy-test-" + uuid.uuid4().hex[:12]
    legacy_created = False
    logfile = None
    try:
        client_key, server_key = directory / "client-key", directory / "server-key"
        for key in (client_key, server_key):
            command(["ssh-keygen", "-q", "-t", "ed25519", "-N", "", "-f", str(key)])
        authorized = directory / "authorized_keys"
        authorized.write_text(client_key.with_suffix(".pub").read_text())
        authorized.chmod(0o600)
        with socket.socket() as probe:
            probe.bind(("127.0.0.1", 0))
            port = probe.getsockname()[1]
        known = directory / "known_hosts"
        known.write_text(f"[127.0.0.1]:{port} " + server_key.with_suffix(".pub").read_text())
        sshd_config = directory / "sshd_config"
        sshd_config.write_text(f"""Port {port}
ListenAddress 127.0.0.1
HostKey {server_key}
PidFile {directory / 'sshd.pid'}
AuthorizedKeysFile {authorized}
AllowUsers {getpass.getuser()}
PasswordAuthentication no
KbdInteractiveAuthentication no
PubkeyAuthentication yes
AuthenticationMethods publickey
UsePAM yes
StrictModes yes
AllowAgentForwarding no
AllowTcpForwarding no
LogLevel ERROR
""")
        command(["sudo", "-n", "mkdir", "-p", "/run/sshd"])
        logfile = (directory / "sshd.log").open("wb")
        sshd = subprocess.Popen(["sudo", "-n", "/usr/sbin/sshd", "-D", "-e", "-f", str(sshd_config)], stdout=subprocess.DEVNULL, stderr=logfile)
        for _ in range(100):
            try:
                with socket.create_connection(("127.0.0.1", port), timeout=0.1):
                    break
            except OSError:
                if sshd.poll() is not None:
                    raise RuntimeError("test SSH server exited")
                time.sleep(0.1)
        else:
            raise RuntimeError("test SSH server did not start")
        ssh_config = directory / "ssh_config"
        ssh_config.write_text(f"""Host test-vm
  HostName 127.0.0.1
  Port {port}
  User {getpass.getuser()}
  IdentityFile {client_key}
  IdentitiesOnly yes
  UserKnownHostsFile {known}
  StrictHostKeyChecking yes
""")
        registry = directory / "registry" / "hosts.json"

        def cli(*args, required=True):
            return command([str(options.binary.resolve()), "--config", str(registry), *args], required=required)

        command(["docker", "create", "--name", legacy, "--label", "com.docker.compose.project=vpn-example", options.image, "version"])
        legacy_created = True
        legacy_before = command(["docker", "inspect", legacy]).stdout
        cli("host", "add", "example", "--ssh", "test-vm", "--ssh-config", str(ssh_config))
        bad_config = directory / "untrusted_config"
        bad_config.write_text(ssh_config.read_text().replace(str(known), str(directory / "unknown_hosts")))
        cli("host", "add", "untrusted", "--ssh", "test-vm", "--ssh-config", str(bad_config))
        assert cli("host", "check", "untrusted", required=False).returncode != 0
        assert command(["docker", "inspect", "vpnmodo-controller"], required=False).returncode != 0
        print("PASS: unknown SSH host keys are rejected", flush=True)
        data_root.mkdir(parents=True, mode=0o700)
        (data_root / ".acceptance-owner").write_text(token)
        result = json.loads(cli("host", "setup", "example", "--image", options.image, "--json").stdout)
        assert result == {"ok": True, "action": "created"}

        def ready():
            for _ in range(30):
                result = cli("host", "check", "example", "--json", required=False)
                if result.returncode == 0:
                    status = json.loads(result.stdout)
                    assert status["docker_ready"] and status["state_ready"] and not status["runtime_enabled"]
                    return
                time.sleep(0.5)
            raise RuntimeError("controller did not become ready over SSH")

        ready()
        inspect = json.loads(command(["docker", "inspect", "vpnmodo-controller"]).stdout)[0]
        assert inspect["HostConfig"]["NetworkMode"] == "none"
        assert inspect["HostConfig"]["ReadonlyRootfs"]
        assert not inspect["HostConfig"].get("PortBindings")
        assert not inspect["HostConfig"]["Privileged"]
        assert inspect["HostConfig"]["CapDrop"] == ["ALL"]
        bridge = ["ssh", "-F", str(ssh_config), "-T", "-o", "StrictHostKeyChecking=yes", "--", "test-vm", "docker --host unix:///var/run/docker.sock exec -i vpnmodo-controller /usr/local/bin/vpnmodo-controller bridge"]
        hello = b'{"jsonrpc":"2.0","id":1,"method":"system.hello","params":{"api":{"major":1,"minor":0}}}\n'
        assert "result" in json.loads(command(bridge, data=hello).stdout)
        session = subprocess.Popen(bridge, stdin=subprocess.PIPE, stdout=subprocess.PIPE, stderr=subprocess.DEVNULL)
        try:
            session.stdin.write(hello)
            session.stdin.flush()
            assert "result" in json.loads(session.stdout.readline())
            session.stdin.write(b'{"jsonrpc":"2.0","id":2,"method":"backend.list","params":{}}\n')
            session.stdin.flush()
            descriptors = json.loads(session.stdout.readline())["result"]
            assert len(descriptors) == 2
            session.stdin.close()
            assert session.wait(timeout=10) == 0
        finally:
            if session.poll() is None:
                session.kill()
                session.wait()
            session.stdout.close()
        ready()
        assert json.loads(command(["docker", "inspect", "vpnmodo-controller"]).stdout)[0]["State"]["StartedAt"] == inspect["State"]["StartedAt"]
        print("PASS: non-PTY SSH RPC and bridge EOF preserve the running controller", flush=True)
        marker = data_root / "controller-state" / "acceptance-marker"
        marker.write_text("synthetic-state")
        command(["docker", "stop", "vpnmodo-controller"])
        result = json.loads(cli("host", "setup", "example", "--image", options.image, "--json").stdout)
        assert result["action"] == "started"
        ready()
        result = json.loads(cli("host", "setup", "example", "--image", options.image, "--replace", "--json").stdout)
        assert result["action"] == "replaced"
        ready()
        assert marker.read_text() == "synthetic-state"
        assert command(["docker", "inspect", legacy]).stdout == legacy_before
        print("PASS: stopped-controller repair, explicit recreation, persistent state and legacy isolation", flush=True)
    finally:
        # No wildcard cleanup or Compose down: remove only this test's resources.
        inspected = command(["docker", "inspect", "vpnmodo-controller"], required=False)
        if inspected.returncode == 0:
            current = json.loads(inspected.stdout)[0]
            labels = current["Config"].get("Labels") or {}
            if current["Config"]["Image"] == options.image and labels.get("io.vpnmodo.managed-by") == "vpnmodo" and labels.get("io.vpnmodo.component") == "controller":
                command(["docker", "rm", "-f", current["Id"]], required=False)
        if legacy_created:
            command(["docker", "rm", legacy], required=False)
        if sshd is not None:
            pid_file = directory / "sshd.pid"
            server_pid = pid_file.read_text().strip() if pid_file.is_file() else str(sshd.pid)
            if server_pid.isdigit():
                command(["sudo", "-n", "kill", "-TERM", server_pid], required=False)
            try:
                sshd.wait(timeout=5)
            except subprocess.TimeoutExpired:
                command(["sudo", "-n", "kill", "-KILL", str(sshd.pid)], required=False)
        if logfile is not None:
            logfile.close()
        owner = data_root / ".acceptance-owner"
        if owner.is_file() and owner.read_text() == token:
            shutil.rmtree(data_root)
        shutil.rmtree(directory)


if __name__ == "__main__":
    main()
