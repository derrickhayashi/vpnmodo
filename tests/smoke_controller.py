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


def command(args, *, data=None, required=True, pass_fds=()):
    result = subprocess.run(args, input=data, capture_output=True, timeout=240, pass_fds=pass_fds)
    if required and result.returncode:
        detail = ""
        if Path(str(args[0])).name == "vpnmodo":
            # This binary emits only fixed safe errors. Never dump wire data,
            # Docker inspection payloads, private keys or arbitrary remote output.
            detail = ": " + result.stderr.decode("utf-8", errors="replace").strip()
        raise RuntimeError("acceptance command failed: " + Path(str(args[0])).name + detail)
    return result


def main():
    if not sys.platform.startswith("linux") or os.environ.get("VPNMODO_ISOLATED_TEST_HOST") != "1":
        raise RuntimeError("run only on an isolated Linux test VM with VPNMODO_ISOLATED_TEST_HOST=1")
    parser = argparse.ArgumentParser()
    parser.add_argument("--binary", required=True, type=Path)
    parser.add_argument("--image", required=True)
    parser.add_argument("--operations", action="store_true", help="use only the synthetic Go test-binary image")
    parser.add_argument("--l2tp", action="store_true", help="real L2TP in an isolated synthetic gateway lab")
    parser.add_argument("--xauth", action="store_true", help="real XAuth MFA and packet isolation lab")
    parser.add_argument("--services-only", action="store_true", help="debug the real L2TP/service subset; not the full recovery suite")
    options = parser.parse_args()
    if options.services_only and (not options.l2tp or options.xauth or options.operations):
        parser.error("--services-only requires only --l2tp")
    data_root = Path.home() / ".local/share/vpnmodo"
    if data_root.exists() or data_root.is_symlink():
        raise RuntimeError("refusing existing vpnmodo data directory")
    if command(["docker", "container", "inspect", "vpnmodo-controller"], required=False).returncode == 0:
        raise RuntimeError("refusing an existing controller container")
    # StrictModes checks authorized_keys ancestors too: keep test keys under
    # the account's home, never under the world-writable /tmp ancestor.
    directory = Path(tempfile.mkdtemp(prefix=".vpnmodo-ssh-", dir=Path.home()))
    token = uuid.uuid4().hex
    sshd = None
    legacy = "vpnmodo-legacy-test-" + uuid.uuid4().hex[:12]
    legacy_created = False
    logfile = None
    lab = None
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
LogLevel VERBOSE
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
        preflight = command(["ssh", "-F", str(ssh_config), "-T", "-o", "BatchMode=yes", "--", "test-vm", "true"], required=False)
        if preflight.returncode:
            # This server has only test identities; the log contains no VPN input.
            logfile.flush()
            print(preflight.stderr.decode("utf-8", errors="replace"), file=sys.stderr)
            print((directory / "sshd.log").read_text(), file=sys.stderr)
            raise RuntimeError("isolated SSH key-authentication preflight failed")
        registry = directory / "registry" / "hosts.json"

        def cli(*args, required=True, data=None, pass_fds=()):
            return command([str(options.binary.resolve()), "--config", str(registry), *args], required=required, data=data, pass_fds=pass_fds)

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
                    assert status["docker_ready"] and status["state_ready"] and status["runtime_enabled"]
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
        # Profile input and secrets are synthetic; keep input bytes off argv/logs.
        profile_file = directory / "profile.json"
        source = Path(__file__).resolve().parents[1] / "examples/profiles/v1"
        definition = json.loads((source / "example-l2tp.json").read_text())
        profile_file.write_text(json.dumps(definition))
        credentials = {"username": "synthetic-user", "password": "synthetic-password", "psk": "synthetic-shared"}
        sensitive = list(credentials.values())

        def safe_result(result):
            output = result.stdout + result.stderr
            assert all(value.encode() not in output for value in sensitive)
            return json.loads(result.stdout) if result.returncode == 0 else None

        created = safe_result(cli("profile", "create", "example-profile", "--host", "example", "--file", str(profile_file), "--credentials-stdin", "--json", data=json.dumps(credentials).encode()))
        assert created["revision"] == 1 and created["credentials_present"]
        assert cli("profile", "create", "example-profile", "--host", "example", "--file", str(profile_file), "--credentials-stdin", data=b"{}", required=False).returncode != 0
        profile_name = definition["name"]
        owned_name = "vpnmodo-active-test-" + uuid.uuid4().hex[:12]
        owned_id = command(["docker", "run", "--detach", "--network", "none", "--tmpfs", "/tmp:mode=1777", "--name", owned_name,
                            "--label", "io.vpnmodo.managed-by=vpnmodo", "--label", f"io.vpnmodo.profile={profile_name}",
                            "--label", f"com.docker.compose.project=vpnmodo-{profile_name}", options.image,
                            "serve", "--socket", "/tmp/run/controller.sock", "--state-dir", "/tmp/state"]).stdout.decode().strip()
        try:
            definition["health"]["port"] = 443
            profile_file.write_text(json.dumps(definition))
            refused = cli("profile", "update", "example-profile", "--file", str(profile_file), "--revision", "1", required=False)
            assert refused.returncode != 0 and b"disconnect" in refused.stderr
        finally:
            command(["docker", "rm", "-f", owned_id])
        updated = safe_result(cli("profile", "update", "example-profile", "--file", str(profile_file), "--revision", "1", "--json"))
        assert updated["revision"] == 2
        assert cli("profile", "update", "example-profile", "--file", str(profile_file), "--revision", "1", required=False).returncode != 0
        read_fd, write_fd = os.pipe()
        try:
            os.write(write_fd, json.dumps(credentials).encode())
            os.close(write_fd)
            write_fd = None
            rotated = safe_result(cli("profile", "rotate-credentials", "example-profile", "--revision", "2", "--credentials-fd", str(read_fd), "--json", pass_fds=(read_fd,)))
            assert rotated["revision"] == 3
        finally:
            os.close(read_fd)
            if write_fd is not None:
                os.close(write_fd)
        # Exercise the actual hidden prompts and recovery when local registration
        # fails after a successful remote commit. No plaintext input files needed.
        interactive = json.loads((source / "example-xauth.json").read_text())
        profile_file.write_text(json.dumps(interactive))
        references = Path(str(registry) + ".profiles.json")
        saved_references = references.read_bytes()
        import fcntl
        import pty
        import select
        import termios

        def controlling_tty():
            os.setsid()
            fcntl.ioctl(0, termios.TIOCSCTTY, 0)

        def prompted_create(alias, cancel=False):
            master, slave = pty.openpty()
            previous = termios.tcgetattr(slave)
            process = subprocess.Popen([str(options.binary.resolve()), "--config", str(registry), "profile", "create", alias,
                                        "--host", "example", "--file", str(profile_file), "--json"],
                                       stdin=slave, stdout=slave, stderr=slave, preexec_fn=controlling_tty)
            transcript = bytearray()

            def until(marker):
                deadline = time.monotonic() + 20
                while marker not in transcript:
                    if time.monotonic() > deadline:
                        raise RuntimeError("hidden prompt did not appear")
                    ready, _, _ = select.select([master], [], [], 0.2)
                    if ready:
                        transcript.extend(os.read(master, 8192))
            try:
                until(b"Username: ")
                if cancel:
                    os.write(master, b"\x03")
                else:
                    references.write_text("invalid-local-registry")
                    for label, value in zip((b"Username: ", b"Password: ", b"Pre-shared key: "), credentials.values()):
                        until(label)
                        assert all(item.encode() not in Path(f"/proc/{process.pid}/cmdline").read_bytes() for item in sensitive)
                        os.write(master, value.encode() + b"\r")
                code = process.wait(timeout=20)
                while select.select([master], [], [], 0)[0]:
                    transcript.extend(os.read(master, 8192))
                assert code != 0  # explicit cancellation or local registration failure
                assert all(value.encode() not in transcript for value in sensitive)
                assert termios.tcgetattr(slave) == previous
                if not cancel:
                    assert b"local registration failed" in transcript
            finally:
                if process.poll() is None:
                    process.kill()
                    process.wait()
                os.close(master)
                os.close(slave)

        prompted_create("recovery-profile")
        references.write_bytes(saved_references)
        assert any(item["name"] == interactive["name"] for item in safe_result(cli("profile", "list", "--host", "example", "--json")))
        safe_result(cli("profile", "register", "recovery-profile", "--host", "example", "--profile", interactive["name"], "--json"))
        interactive["name"] = "cancelled-profile"
        profile_file.write_text(json.dumps(interactive))
        prompted_create("cancelled-profile", cancel=True)
        assert all(item["name"] != "cancelled-profile" for item in safe_result(cli("profile", "list", "--host", "example", "--json")))
        state = data_root / "controller-state"
        for file in (state / "profiles").iterdir():
            assert file.stat().st_mode & 0o777 == 0o600
            assert all(value.encode() not in file.read_bytes() for value in sensitive)
        for file in (state / "credentials").iterdir():
            assert file.stat().st_mode & 0o777 == 0o600
        assert (state / "credentials").stat().st_mode & 0o777 == 0o700
        assert all(value.encode() not in references.read_bytes() for value in sensitive)
        assert all(value.encode() not in command(["docker", "logs", "vpnmodo-controller"]).stdout for value in sensitive)
        print("PASS: secure profile creation/rotation, active/stale refusal, hidden prompts/cancellation and registration recovery", flush=True)
        interrupted_id = None
        if options.operations:
            import operations_acceptance
            interrupted_id = operations_acceptance.begin(cli, bridge, [str(options.binary.resolve()), "--config", str(registry)], directory, state)
            import recovery_acceptance
            cooldown = recovery_acceptance.begin(cli, directory, state)
        if options.xauth:
            import xauth_acceptance
            lab = xauth_acceptance.begin(cli, directory, state, bridge, [str(options.binary.resolve()), "--config", str(registry)])
        if options.l2tp:
            import l2tp_acceptance
            lab = l2tp_acceptance.begin(cli, directory, state, services_only=options.services_only)
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
        if options.operations:
            operations_acceptance.after_restart(cli, state, interrupted_id)
            recovery_acceptance.after_restart(cli, state, cooldown)
        if options.xauth:
            xauth_acceptance.after_restart(cli, state, lab)
        if options.l2tp:
            l2tp_acceptance.after_restart(cli, state, lab)
        retained = safe_result(cli("profile", "inspect", "example-profile", "--json"))
        assert retained["revision"] == 3 and retained["credentials_present"]
        safe_result(cli("profile", "delete", "example-profile", "--revision", "3", "--json"))
        safe_result(cli("profile", "delete", "recovery-profile", "--revision", "1", "--json"))
        assert list((state / "credentials").iterdir()) == []
        assert safe_result(cli("profile", "list", "--host", "example", "--json")) == []
        print("PASS: profile/credential restart persistence and explicit deletion/disposal", flush=True)
        assert command(["docker", "inspect", legacy]).stdout == legacy_before
        print("PASS: stopped-controller repair, explicit recreation, persistent state and legacy isolation", flush=True)
    finally:
        if lab is not None:
            lab.close()
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
