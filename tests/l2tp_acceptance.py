"""Real strongSwan/L2TP/PPP interoperability on an isolated Ubuntu runner."""
import json
import os
from pathlib import Path
import time
import uuid

from operations_acceptance import last_json
from smoke_controller import command

NAME = "lab-l2tp"
PROJECT = "vpnmodo-" + NAME
NETWORK = PROJECT + "_default"


class Lab:
    def __init__(self, cli):
        self.cli = cli
        self.gateway = "vpnmodo-lab-gateway-" + uuid.uuid4().hex[:12]
        self.created = False
        self.network = False
        self.profile_created = False

    def close(self):
        if getattr(self, "services", None):
            self.services.close()
            self.services = None
        if self.profile_created:
            self.cli("connection", "disconnect", NAME, "--wait", "--json", required=False)
        if self.created:
            command(["docker", "rm", "-f", self.gateway], required=False)
        # Exact manager/profile/project intersection, never broad Docker cleanup.
        if not self.network:
            return
        raw = command(["docker", "ps", "-aq", "--filter", "label=io.vpnmodo.managed-by=vpnmodo", "--filter", "label=io.vpnmodo.profile=" + NAME, "--filter", "label=com.docker.compose.project=" + PROJECT], required=False)
        for cid in raw.stdout.decode().split():
            command(["docker", "rm", "-f", cid], required=False)
        if self.network:
            command(["docker", "network", "rm", NETWORK], required=False)

    def ids(self):
        result = {}
        raw = command(["docker", "ps", "-aq", "--filter", "label=com.docker.compose.project=" + PROJECT])
        for cid in raw.stdout.decode().split():
            data = json.loads(command(["docker", "inspect", cid]).stdout)[0]
            if data["Config"]["Labels"].get("io.vpnmodo.managed-by") == "vpnmodo":
                result[data["Config"]["Labels"]["com.docker.compose.service"]] = data
        return result

    def wait(self, predicate, timeout=90):
        deadline = time.monotonic() + timeout
        while time.monotonic() < deadline:
            c = last_json(self.cli("connection", "status", NAME, "--json"))
            if predicate(c):
                return c
            time.sleep(0.3)
        raise RuntimeError("isolated L2TP acceptance did not reach expected state")

    def control(self, action):
        command(["docker", "exec", self.gateway, "lab-control", action])

    def namespaces(self):
        ids = self.ids()
        vpn = command(["docker", "exec", ids["vpn"]["Id"], "readlink", "/proc/self/ns/net"]).stdout
        app = command(["docker", "exec", ids["application"]["Id"], "readlink", "/proc/self/ns/net"]).stdout
        assert vpn == app and vpn.startswith(b"net:[")


def begin(cli, directory, state_root, services_only=False):
    lab = Lab(cli)
    try:
        assert command(["docker", "network", "inspect", NETWORK], required=False).returncode != 0
        assert not command(["docker", "ps", "-aq", "--filter", "label=com.docker.compose.project=" + PROJECT]).stdout.strip()
        args = ["docker", "network", "create", "--subnet", "192.0.2.0/24", "--gateway", "192.0.2.254"]
        for label in ("io.vpnmodo.managed-by=vpnmodo", "io.vpnmodo.profile=" + NAME, "io.vpnmodo.backend=l2tp-ipsec", "com.docker.compose.project=" + PROJECT, "com.docker.compose.network=default"):
            args += ["--label", label]
        command([*args, NETWORK])
        lab.network = True
        command(["docker", "run", "-d", "--name", lab.gateway, "--network", NETWORK, "--ip", "192.0.2.1", "--cap-add", "NET_ADMIN", "--cap-add", "NET_RAW", "--device", "/dev/ppp:/dev/ppp", "--log-driver", "none", "vpnmodo-lab-gateway:acceptance"])
        lab.created = True
        deadline = time.monotonic() + 30
        while command(["docker", "exec", lab.gateway, "test", "-f", "/run/lab/ready"], required=False).returncode:
            if time.monotonic() > deadline:
                raise RuntimeError("synthetic gateway startup failed")
            time.sleep(0.5)
        listening = command(["docker", "exec", lab.gateway, "ss", "-lnt"]).stdout
        assert b":28080" in listening, "synthetic gateway TCP listener unavailable"
        definition = {"schema_version": 1, "name": NAME, "backend": {"type": "l2tp-ipsec", "config": {"server": "192.0.2.1", "dns": ["198.51.100.1"], "ike_proposals": ["aes256-sha256-modp2048!"], "esp_proposals": ["aes256-sha256!"]}}, "health": {"host": "198.51.100.1", "port": 28080}}
        source = directory / "l2tp-profile.json"
        credentials = {"username": "synthetic-user", "password": "synthetic-password", "psk": "synthetic-shared"}
        source.write_text(json.dumps(dict(definition, credentials=credentials)))
        source.chmod(0o600)
        last_json(cli("profile", "create", NAME, "--file", str(source), "--json"))
        lab.profile_created = True
        ready = last_json(cli("connection", "ensure", NAME, "--wait", "--json"))
        assert ready["state"] == "succeeded"
        ids = lab.ids()
        original = ids["vpn"]["Id"]
        # Protocol evidence: established IPsec, actual PPP, TCP through the tunnel,
        # and an application in the same kernel network namespace.
        command(["docker", "exec", original, "vpn-tunnelcheck"])
        command(["docker", "exec", original, "vpn-servicecheck"])
        ppp_address = command(["docker", "exec", original, "ip", "-4", "address", "show", "dev", "ppp0"]).stdout
        assert b"inet 198.51.100." in ppp_address and b"peer 198.51.100.1" in ppp_address
        command(["docker", "exec", ids["application"]["Id"], "nc", "-z", "-w", "3", "198.51.100.1", "28080"])
        lab.namespaces()
        command(["docker", "exec", ids["application"]["Id"], "nslookup", "-type=A", "private.example", "127.0.0.1"])
        ipv6 = command(["docker", "exec", ids["application"]["Id"], "cat", "/proc/sys/net/ipv6/conf/all/disable_ipv6"]).stdout
        assert ipv6.strip() == b"1"
        last_json(cli("connection", "ensure", NAME, "--wait", "--json"))
        assert lab.ids()["vpn"]["Id"] == original
        env = ids["vpn"]["Config"]["Env"]
        assert not any(item.startswith(("VPN_PASSWORD=", "VPN_PSK=", "VPN_USERNAME=")) for item in env)
        assert ids["vpn"]["HostConfig"]["RestartPolicy"]["Name"] == "no"
        assert ids["vpn"]["HostConfig"]["LogConfig"]["Type"] == "none"
        mounts = ids["vpn"]["Mounts"]
        secret_mounts = [m for m in mounts if m["Destination"].startswith("/run/secrets/")]
        assert len(secret_mounts) == 3 and all(not m["RW"] for m in secret_mounts)
        assert all(str(state_root / "deployments" / NAME / "credentials") in m["Source"] for m in secret_mounts)
        for path in (state_root / "deployments" / NAME / "credentials").glob("*/*"):
            assert path.stat().st_mode & 0o777 == 0o600
        print("PASS: real IKEv1/IPsec transport, L2TP/PPP and private TCP access; healthy reuse and isolated credential mounts", flush=True)
        if services_only:
            return service_stage(lab, cli, directory)
        foreign_service = "vpnmodo-foreign-service-" + uuid.uuid4().hex[:12]
        command(["docker", "create", "--name", foreign_service, "--network", "none", "--label", "com.docker.compose.project=" + PROJECT, "--label", "com.docker.compose.service=observer", "alpine:3.22", "sleep", "infinity"])
        untouched = command(["docker", "inspect", foreign_service]).stdout
        try:
            refused = cli("connection", "ensure", NAME, "--wait", "--json", required=False)
            assert refused.returncode != 0
            assert lab.ids()["vpn"]["Id"] == original
            assert command(["docker", "inspect", foreign_service]).stdout == untouched
        finally:
            command(["docker", "rm", foreign_service])
        last_json(cli("connection", "ensure", NAME, "--wait", "--json"))
        assert lab.ids()["vpn"]["Id"] == original
        print("PASS: an unknown service causes refusal of managed actions without altering either deployment", flush=True)
        lab.control("service-stop")
        lab.wait(lambda c: c.get("recovery", {}).get("health") == "service_unavailable")
        assert lab.ids()["vpn"]["Id"] == original
        lab.control("service-start")
        lab.wait(lambda c: c.get("recovery", {}).get("health") == "healthy")
        # Recreate only the VPN sandbox through Docker, leaving the old application
        # namespace alive. The controller must repair attachment without replacing
        # the now-healthy VPN again.
        app_before = lab.ids()["application"]["Id"]
        command(["docker", "pause", "vpnmodo-controller"])
        try:
            command(["docker", "compose", "--project-name", PROJECT, "--env-file", "/dev/null", "-f", str(state_root / "deployments" / NAME / "compose.json"), "up", "-d", "--no-deps", "--pull", "never", "--force-recreate", "vpn"])
            replacement = lab.ids()["vpn"]["Id"]
            # This case tests repair of an application beside a healthy VPN.
            # Wait for the externally recreated VPN to finish startup while the
            # controller is paused; tunnel-loss recovery is exercised separately.
            deadline = time.monotonic() + 150
            while True:
                current = lab.ids()["vpn"]
                assert current["Id"] == replacement, "manual VPN replacement changed before controller resumed"
                assert current["State"]["Running"], "manual VPN replacement failed during startup"
                ready = command(["docker", "exec", replacement, "test", "-f", "/run/vpn/ready"], required=False).returncode == 0
                if ready and command(["docker", "exec", replacement, "vpn-servicecheck"], required=False).returncode == 0:
                    break
                if time.monotonic() > deadline:
                    raise RuntimeError("manual VPN replacement did not become healthy")
                time.sleep(0.3)
        finally:
            command(["docker", "unpause", "vpnmodo-controller"])
        lab.wait(lambda c: c["observed"] == "connected" and "active_operation" not in c and lab.ids().get("application", {}).get("Id") != app_before, timeout=150)
        assert lab.ids()["vpn"]["Id"] == replacement
        original = replacement
        lab.namespaces()
        command(["docker", "exec", original, "ipsec", "down", "l2tp-psk"])
        lab.wait(lambda c: c["observed"] == "connected" and "active_operation" not in c and lab.ids().get("vpn", {}).get("Id") != original, timeout=150)
        recovered = lab.ids()["vpn"]["Id"]
        lab.namespaces()
        print("PASS: endpoint outage preserves VPN, stale application namespace repairs, and tunnel loss recovers through the controller", flush=True)
        # Fail verification after a genuine new authenticated tunnel, and prove
        # the application was never reattached to that failed recovery.
        lab.control("service-stop")
        command(["docker", "exec", recovered, "ipsec", "down", "l2tp-psk"])
        failed = lab.wait(lambda c: "last_operation" in c and "active_operation" not in c and c.get("recovery", {}).get("attempts", 0) > 0, timeout=150)
        record = last_json(cli("operation", "status", failed["last_operation"], "--json"))
        assert record["kind"] == "connection.recover" and record["state"] == "failed"
        assert "application" not in lab.ids()
        last_json(cli("connection", "disconnect", NAME, "--wait", "--json"))
        lab.control("service-start")
        time.sleep(4)
        assert lab.ids() == {}
        last_json(cli("connection", "ensure", NAME, "--wait", "--json"))
        print("PASS: failed real recovery leaves forwarding stopped, and explicit disconnect prevents unattended revival", flush=True)
        # Stop during a newly started idle sleep, when a foreground sleep would
        # postpone TERM handling past Docker's two-second grace period. Exercise
        # the real entrypoint and peer cleanup, then reconnect repeatedly.
        for _ in range(3):
            current = lab.ids()["vpn"]["Id"]
            command(["docker", "pause", "vpnmodo-controller"])
            try:
                def sleep_pid():
                    script = 'for p in $(cat /proc/1/task/1/children); do if [ "$(cat /proc/$p/comm 2>/dev/null)" = sleep ]; then printf "%s" "$p"; fi; done'
                    return command(["docker", "exec", current, "sh", "-c", script]).stdout.strip()
                previous = sleep_pid()
                deadline = time.monotonic() + 8
                while True:
                    fresh = sleep_pid()
                    if fresh and fresh != previous:
                        break
                    if time.monotonic() > deadline:
                        raise RuntimeError("VPN idle sleep did not advance")
                    time.sleep(0.05)
                command(["docker", "stop", "--time", "2", current])
                stopped = json.loads(command(["docker", "inspect", current]).stdout)[0]
                assert stopped["State"]["ExitCode"] == 0, "VPN was force-killed before graceful cleanup"
                status = command(["docker", "exec", lab.gateway, "ipsec", "status"]).stdout
                assert b"ESTABLISHED" not in status, "gateway retained an authenticated session after graceful stop"
            finally:
                command(["docker", "unpause", "vpnmodo-controller"])
            last_json(cli("connection", "ensure", NAME, "--wait", "--json"))
            lab.namespaces()
        print("PASS: idle-sleep TERM cleans the peer within Docker's stop budget, and three fresh reconnects work", flush=True)
        return service_stage(lab, cli, directory)
    except Exception:
        # Sanitized protocol/process presence only; never dump configs or logs.
        if lab.created:
            server_addr = command(["docker", "exec", lab.gateway, "ip", "-4", "address"], required=False).stdout
            server_tcp = command(["docker", "exec", lab.gateway, "ss", "-lnt"], required=False).stdout
            print("L2TP gateway diagnostic: expected PPP address=", b"198.51.100.1" in server_addr, "TCP listener=", b":28080" in server_tcp, flush=True)
        trace = state_root / "l2tp-trace.json"
        if trace.is_file():
            print("L2TP command phases:", json.dumps(json.loads(trace.read_text())[-45:]), flush=True)
        ids = lab.ids() if lab.network else {}
        for role in ("vpn", "application"):
            if role in ids:
                cid = ids[role]["Id"]
                print("L2TP diagnostic:", role, "running=", ids[role]["State"]["Running"], "exit=", ids[role]["State"]["ExitCode"], "tunnel_check=", command(["docker", "exec", cid, "vpn-tunnelcheck"], required=False).returncode, flush=True)
                if role == "vpn":
                    import tempfile
                    evidence=Path(tempfile.mkdtemp(prefix="vpnmodo-synthetic-startup-"))
                    evidence.chmod(0o700)
                    output=evidence/"startup.log"
                    copied=command(["docker","cp",cid+":/run/vpn/lab-startup-output",str(output)],required=False)
                    if copied.returncode==0:
                        output.chmod(0o600)
                        text=output.read_text(errors="replace")
                        print("Synthetic startup flags:", {key:key in text for key in ("Cannot find device", "RTNETLINK", "IPsec did not connect", "PPP interface ppp0 did not become ready", "Connection terminated", "Starting L2TP and PPP")}, "private evidence:",str(output),flush=True)

        lab.close()
        raise


def service_stage(lab, cli, directory):
    from service_acceptance import ServiceLab
    lab.services = ServiceLab(cli, directory, lab)
    lab.services.begin()
    lab.vpn_before_restart = lab.ids()["vpn"]["Id"]
    return lab


def after_restart(cli, state_root, lab):
    lab.wait(lambda c: c["observed"] == "connected" and c["recovery_enabled"])
    assert lab.ids()["vpn"]["Id"] == lab.vpn_before_restart
    lab.namespaces()
    lab.services.after_restart()
    lab.services = None
    last_json(cli("connection", "disconnect", NAME, "--wait", "--json"))
    assert not list((state_root / "deployments" / NAME / "credentials").glob("*/*"))
    # The gateway is a test resource using the managed network; remove it before
    # testing profile deletion's refusal to remove networks with active users.
    command(["docker", "rm", "-f", lab.gateway])
    lab.created = False
    last_json(cli("profile", "delete", NAME, "--revision", "1", "--json"))
    assert not (state_root / "deployments" / NAME).exists()
    lab.network = False
    lab.profile_created = False
    print("PASS: controller recreation preserves real VPN session; disconnect disposes runtime copies and deletion removes deployment state", flush=True)
