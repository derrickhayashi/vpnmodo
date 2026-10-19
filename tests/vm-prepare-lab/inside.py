#!/usr/bin/env python3
"""Exercise the real role only inside the explicitly owned systemd container."""
import argparse
import hashlib
import json
import os
from pathlib import Path
import re
import subprocess


def command(args, check=True):
    return subprocess.run(args, check=check, capture_output=True, text=True)


def snapshot():
    """Check-mode promises apply to managed state, not Ansible's temp files."""
    files = {}
    for filename in ("/etc/passwd", "/etc/group", "/etc/gshadow",
                     "/etc/sudoers", "/etc/docker/daemon.json"):
        path = Path(filename)
        files[filename] = hashlib.sha256(path.read_bytes()).hexdigest()
    for directory in ("/etc/sudoers.d", "/etc/modules-load.d"):
        for path in sorted(Path(directory).glob("*")):
            if path.is_file():
                files[str(path)] = hashlib.sha256(path.read_bytes()).hexdigest()
    return {
        "files": files,
        "packages": command(["dpkg-query", "-W", "-f=${Package}=${Version} ${db:Status-Status}\\n"]).stdout,
        "docker_enabled": command(["systemctl", "is-enabled", "docker.service"], False).stdout,
        "docker_active": command(["systemctl", "is-active", "docker.service"], False).stdout,
    }


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--scenario", choices=("full", "ipsec-only"), required=True)
    options = parser.parse_args()
    assert os.environ.get("VPNMODO_VM_PREPARE_LAB") == "1", "explicit isolated-lab flag required"
    assert Path("/proc/1/comm").read_text().strip() == "systemd", "genuine systemd PID 1 required"
    for process in ("1", "self"):
        status = Path("/proc", process, "status").read_text()
        bounding = re.search(r"^CapBnd:\s+([0-9a-f]+)$", status, re.MULTILINE)
        assert bounding and not (int(bounding.group(1), 16) & (1 << 16)), "CAP_SYS_MODULE must be unavailable"
    assert os.uname().machine in ("aarch64", "arm64"), "ARM64 acceptance only"
    assert not Path("/var/run/docker.sock").exists(), "fresh target must have no Docker daemon"
    if options.scenario == "ipsec-only":
        # --privileged exposes host device numbers even without --device. Docker
        # creates these nodes in the container's own /dev tmpfs. Never unlink a
        # bind-mounted node or anything on a host-provided device filesystem.
        mounts = {}
        for line in Path("/proc/self/mountinfo").read_text().splitlines():
            before, after = line.split(" - ", 1)
            mounts[before.split()[4]] = after.split()[0]
        assert mounts.get("/dev") == "tmpfs", "private container /dev tmpfs required"
        assert "/dev/ppp" not in mounts, "refuse to hide a mount-bound PPP device"
        device = Path("/dev/ppp")
        assert not device.is_symlink(), "refuse a symlink PPP device"
        if device.exists():
            assert device.is_char_device(), "expected private PPP character device node"
            device.unlink()
        assert not device.exists(), "IPsec-only fixture must begin without PPP device"
    work = Path("/tmp/vpnmodo-vm-prepare-evidence")
    work.mkdir(mode=0o700)
    env = dict(os.environ, ANSIBLE_COLLECTIONS_PATH="/opt/collections",
               ANSIBLE_NOCOLOR="1", ANSIBLE_STDOUT_CALLBACK="default",
               ANSIBLE_LOCAL_TEMP=str(work / "ansible-local"))
    l2tp = options.scenario == "full"
    base = {"vpnmodo_vm_management_user": "labdirect", "vpnmodo_vm_l2tp": l2tp,
            "vpn_sidecar_install_kernel_modules": False,
            "vpn_sidecar_l2tp_acceleration": False,
            "ansible_python_interpreter": "/usr/bin/python3"}

    def play(label, overrides=None, check=False, failure=None, changed=None):
        variables = dict(base, **(overrides or {}))
        playbook = work / (label + ".json")
        playbook.write_text(json.dumps([{"name": label, "hosts": "localhost",
            "gather_facts": True, "vars": variables,
            "roles": ["vpn_sidecar.client.vm_prepare"]}]))
        argv = ["/opt/ansible/bin/ansible-playbook", "-i", "localhost,", "-c", "local", str(playbook)]
        if check:
            argv.append("--check")
        result = subprocess.run(argv, env=env, capture_output=True, text=True)
        (work / (label + ".log")).write_text(result.stdout + result.stderr)
        if failure:
            assert result.returncode != 0 and failure in result.stdout + result.stderr, (label, "expected failure missing", str(work))
            print("PASS expected refusal:", label, flush=True)
            return
        assert result.returncode == 0, (label, "Ansible failed; evidence", str(work))
        recap = re.search(r"localhost\s*:\s+ok=\d+\s+changed=(\d+).*failed=(\d+)", result.stdout)
        assert recap and int(recap.group(2)) == 0, (label, "missing successful recap", str(work))
        count = int(recap.group(1))
        if changed is not None:
            assert count == changed, (label, "unexpected changed count", count, str(work))
        print("PASS", label, "changed=" + str(count), flush=True)
        return count

    # Input/kernel-policy refusals occur before package or account mutation.
    for label, overrides, marker in (
        ("missing-account", {"vpnmodo_vm_management_user": "lababsent"}, "Require the selected management account to exist"),
        ("invalid-access", {"vpnmodo_vm_docker_access": "implicit"}, "VM preparation requires"),
        ("invalid-l2tp", {"vpnmodo_vm_l2tp": "true"}, "VM preparation requires"),
        ("blocked-kernel", {"vpn_sidecar_kernel_blocklist": [os.uname().release]}, "does not satisfy"),
    ):
        before = snapshot()
        play(label, overrides, failure=marker)
        assert snapshot() == before, (label, "refusal mutated managed state")

    before = snapshot()
    play("fresh-check", check=True)
    assert snapshot() == before, "fresh check mode changed managed state"
    assert play("fresh-apply") > 0, "fresh apply must install Docker"
    play("second-apply", changed=0)
    before = snapshot()
    play("prepared-check", check=True, changed=0)
    assert snapshot() == before, "prepared check mode changed managed state"
    assert command(["systemctl", "is-active", "docker.service"]).stdout.strip() == "active"
    assert command(["systemctl", "is-enabled", "docker.service"]).stdout.strip() == "enabled"
    assert "docker" in command(["id", "-nG", "labdirect"]).stdout.split()

    sudo_policy = {str(p): p.read_bytes() for p in Path("/etc/sudoers.d").glob("*") if p.is_file()}
    sudo = {"vpnmodo_vm_management_user": "labsudo", "vpnmodo_vm_docker_access": "sudo"}
    play("preauthorized-sudo", sudo, changed=0)
    before = snapshot()
    play("preauthorized-sudo-check", sudo, check=True, changed=0)
    assert snapshot() == before, "sudo check mode changed managed state"
    assert "docker" not in command(["id", "-nG", "labsudo"]).stdout.split()
    denied = {"vpnmodo_vm_management_user": "labdenied", "vpnmodo_vm_docker_access": "sudo"}
    play("unauthorized-sudo", denied, failure="Validate pre-authorized noninteractive sudo")
    assert "docker" not in command(["id", "-nG", "labdenied"]).stdout.split()
    assert sudo_policy == {str(p): p.read_bytes() for p in Path("/etc/sudoers.d").glob("*") if p.is_file()}, "role changed sudo policy"

    module_file = Path("/etc/modules-load.d/vpn-sidecar-vpnmodo-preparation.conf")
    expected = ["xfrm_user", "esp4"] + (["ppp_generic", "ppp_async"] if l2tp else [])
    assert module_file.read_text().splitlines() == expected, "wrong persisted module selection"
    if l2tp:
        # /dev is private to this container. Hide only its synthetic device node.
        device = Path("/dev/ppp")
        hidden = Path("/dev/ppp-lab-hidden")
        device.rename(hidden)
        try:
            play("missing-ppp-device", failure="/dev/ppp is unavailable")
        finally:
            hidden.rename(device)
        play("ppp-restored", changed=0)
    else:
        assert not Path("/dev/ppp").exists(), "IPsec-only fixture must omit the PPP device"

    assert not command(["docker", "--host", "unix:///var/run/docker.sock", "ps", "-aq"]).stdout.strip(), "preparation created containers"
    assert not Path("/root/.local/share/vpnmodo").exists(), "preparation activated controller state"
    for username in ("labdirect", "labsudo", "labdenied"):
        assert not Path("/home", username, ".local/share/vpnmodo").exists(), "preparation activated user state"
    print("PASS real systemd/Docker preparation:", options.scenario, flush=True)
    print("Synthetic evidence inside owned disposable container:", work, flush=True)


if __name__ == "__main__":
    main()
