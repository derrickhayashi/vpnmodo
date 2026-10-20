#!/usr/bin/env python3
"""Real delayed-MFA regression; run on the synthetic Docker lab host as root.

Uses no controller state or real credentials. Requires xauth-lab gateway image,
old/new managed client images, and IPsec kernel support. The old-image override
changes only the entrypoint, isolating the handshake timeout from image rebuilds.
"""
import argparse
import json
from pathlib import Path
import subprocess
import tempfile
import time
import uuid


def docker(*args, data=None, check=True):
    result = subprocess.run(["docker", *args], input=data, capture_output=True, timeout=60)
    if check and result.returncode:
        raise RuntimeError("synthetic Docker command failed: " + result.stderr.decode()[-1000:])
    return result


def wait_for(check, timeout=30):
    end = time.monotonic() + timeout
    while time.monotonic() < end:
        if check():
            return
        time.sleep(0.2)
    raise AssertionError("synthetic condition timed out")


def state(client):
    return docker("exec", client, "cat", "/run/vpn/state", check=False).stdout.strip()


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--old-image", required=True)
    parser.add_argument("--fixed-image", required=True)
    parser.add_argument("--fixed-entrypoint", type=Path, required=True)
    parser.add_argument("--delay", type=float, default=40)
    args = parser.parse_args()
    assert args.delay > 30, "regression must exceed strongSwan's default 30 seconds"
    entrypoint = args.fixed_entrypoint.resolve()
    assert entrypoint.is_file()
    prefix = "vpnmodo-timeout-" + uuid.uuid4().hex[:10]
    network = prefix + "-net"
    ids = docker("ps", "-aq").stdout.decode().split()
    existing = {cid: json.loads(docker("inspect", "--format", "{{json .State}}", cid).stdout) for cid in ids}
    docker("network", "create", "--subnet", "192.0.2.0/24", "--gateway", "192.0.2.254", network)
    try:
        with tempfile.TemporaryDirectory(prefix=prefix + "-") as directory:
            mounted_entrypoint = Path(directory) / "vpn-managed-xauth"
            mounted_entrypoint.write_bytes(entrypoint.read_bytes())
            mounted_entrypoint.chmod(0o755)
            for name, value in {"username": "synthetic-user", "password": "synthetic-password", "psk": "synthetic-shared"}.items():
                path = Path(directory) / ("vpn_" + name)
                path.write_text(value)
                path.chmod(0o600)
            cases = [("old-fast", args.old_image, 0, False, True),
                     ("old-delayed", args.old_image, args.delay, False, False),
                     ("old-with-timeout-fix", args.old_image, args.delay, True, True),
                     ("release-delayed", args.fixed_image, args.delay, False, True)]
            for label, image, delay, override, succeeds in cases:
                gateway, client = prefix + "-gateway", prefix + "-client"
                try:
                    docker("run", "-d", "--name", gateway, "--network", network, "--ip", "192.0.2.1",
                           "--cap-add", "NET_ADMIN", "--cap-add", "NET_RAW", "--log-driver", "none",
                           "vpnmodo-xauth-gateway:acceptance")
                    wait_for(lambda: docker("exec", gateway, "test", "-f", "/run/lab/ready", check=False).returncode == 0)
                    command = ["run", "-d", "--name", client, "--init", "--network", network, "--ip", "192.0.2.10",
                               "--cap-add", "NET_ADMIN", "--cap-add", "NET_RAW", "--read-only",
                               "--sysctl", "net.ipv6.conf.all.disable_ipv6=1", "--sysctl", "net.ipv6.conf.default.disable_ipv6=1",
                               "--tmpfs", "/run:rw,nosuid,nodev,mode=0755", "--tmpfs", "/tmp:rw,nosuid,nodev,noexec,mode=1777",
                               "--dns", "127.0.0.1", "--security-opt", "no-new-privileges:true", "--log-driver", "none",
                               "--mount", "type=bind,src=" + directory + ",dst=/run/secrets,readonly",
                               "--entrypoint", "/usr/local/bin/vpn-managed-xauth"]
                    for key, value in {"VPN_SERVER": "192.0.2.1", "VPN_DNS": "198.51.100.1",
                                       "VPN_IKE_PROPOSALS": "aes256-sha256-modp2048", "VPN_ESP_PROPOSALS": "aes256-sha256",
                                       "VPN_HEALTH_HOST": "198.51.100.1", "VPN_HEALTH_PORT": "28080"}.items():
                        command.extend(["--env", key + "=" + value])
                    if override:
                        command.extend(["--mount", "type=bind,src=" + str(mounted_entrypoint) + ",dst=/usr/local/bin/vpn-managed-xauth,readonly"])
                    docker(*command, image)
                    wait_for(lambda: state(client) == b"mfa")
                    started = time.monotonic()
                    print(label + ": challenge ready; delaying " + str(delay) + " seconds", flush=True)
                    time.sleep(delay)
                    docker("exec", "-i", client, "vpn-submit-code", data=b"9876543210")
                    if succeeds:
                        wait_for(lambda: state(client) == b"established")
                        docker("exec", client, "vpn-tunnelcheck")
                        echo = docker("exec", "-i", "--user", "65534:65534", client, "nc", "-N", "-w", "5", "198.51.100.1", "28080",
                                      data=b"synthetic-delayed-mfa\n")
                        assert echo.stdout == b"synthetic-delayed-mfa\n", "authenticated tunnel TCP echo failed"
                        outcome = "established; tunnel and TCP echo passed"
                    else:
                        wait_for(lambda: docker("inspect", "--format", "{{.State.Running}}", client).stdout.strip() == b"false")
                        code = docker("inspect", "--format", "{{.State.ExitCode}}", client).stdout.strip()
                        assert code == b"27", "expected EOF before CHILD_SA, got exit " + code.decode()
                        outcome = "reproduced EOF before CHILD_SA (exit 27)"
                    print(label + ": PASS, " + outcome + "; elapsed=" + str(round(time.monotonic() - started, 1)), flush=True)
                finally:
                    docker("rm", "-f", client, gateway, check=False)
                    for name in (client, gateway):
                        assert docker("inspect", name, check=False).returncode != 0, "test container cleanup failed"
    finally:
        docker("network", "rm", network)
        assert docker("network", "inspect", network, check=False).returncode != 0, "test network cleanup failed"
    for cid, prior in existing.items():
        current = json.loads(docker("inspect", "--format", "{{json .State}}", cid).stdout)
        assert current["Running"] == prior["Running"] and current["StartedAt"] == prior["StartedAt"], "existing container state changed"
    print("PASS: existing containers retained their running/start states", flush=True)


if __name__ == "__main__":
    main()
