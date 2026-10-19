"""Real aggressive IKEv1 + XAUTH_PASSCODE and Ethernet-observed fail-closed tests.

The escape sink shares the outer bridge and can route authenticated replies via
our gateway. Capture observes the host side of the VPN veth before bridge routing,
so a changed route cannot hide plaintext fallback.
"""
import json
import socket
import re
import os
import subprocess
from pathlib import Path
import time
import uuid

from smoke_controller import command
from operations_acceptance import last_json

NAME = "lab-xauth"
PROJECT = "vpnmodo-" + NAME
NETWORK = PROJECT + "_default"


class Lab:
    def __init__(self, cli, bridge, cli_args, directory):
        self.cli, self.bridge, self.cli_args = cli, bridge, cli_args
        self.directory = directory
        self.gateway = "vpnmodo-xauth-gateway-" + uuid.uuid4().hex[:8]
        self.sink = "vpnmodo-xauth-sink-" + uuid.uuid4().hex[:8]
        self.network = False
        self.created = []
        self.profile_created = False

    def ids(self):
        result = {}
        raw = command(["docker", "ps", "-aq", "--filter", "label=com.docker.compose.project=" + PROJECT])
        for cid in raw.stdout.decode().split():
            data = json.loads(command(["docker", "inspect", cid]).stdout)[0]
            assert data["Config"]["Labels"].get("io.vpnmodo.managed-by") == "vpnmodo"
            result[data["Config"]["Labels"]["com.docker.compose.service"]] = data
        return result

    def rpc(self, method, params):
        hello = {"jsonrpc": "2.0", "id": 1, "method": "system.hello", "params": {"api": {"major": 1, "minor": 0}}}
        request = {"jsonrpc": "2.0", "id": 2, "method": method, "params": params}
        raw = command(self.bridge, data=(json.dumps(hello) + "\n" + json.dumps(request) + "\n").encode()).stdout
        return [json.loads(line) for line in raw.splitlines()][-1]

    def wait_operation(self, op, state, timeout=100):
        deadline = time.monotonic() + timeout
        while time.monotonic() < deadline:
            snap = last_json(self.cli("operation", "status", op, "--host", "example", "--json"))
            if snap["state"] == state:
                return snap
            if snap["state"] in ("failed", "cancelled", "succeeded"):
                raise RuntimeError("XAuth operation reached unexpected terminal state: " + snap["state"])
            time.sleep(0.3)
        raise RuntimeError("XAuth operation deadline exceeded")

    def ensure_challenge(self):
        op = last_json(self.cli("connection", "ensure", NAME, "--json"))
        return self.wait_operation(op["id"], "awaiting_mfa")

    def authenticate(self, interactive=False):
        snap = self.ensure_challenge()
        if interactive:
            self.prompt_code(snap)
        else:
            self.cli("operation", "submit-mfa", snap["id"], "--host", "example", "--challenge", snap["challenge"]["id"], "--code-stdin", "--json", data=b"9876543210")
        self.wait_operation(snap["id"], "succeeded")
        return snap

    def prompt_code(self, snapshot):
        import fcntl
        import pty
        import select
        import termios
        master, slave = pty.openpty()
        previous = termios.tcgetattr(slave)
        def controlling_tty():
            os.setsid()
            fcntl.ioctl(0, termios.TIOCSCTTY, 0)
        process = subprocess.Popen([*self.cli_args, "operation", "watch", snapshot["id"], "--host", "example"], stdin=slave, stdout=slave, stderr=slave, preexec_fn=controlling_tty)
        transcript = bytearray()
        submitted = False
        try:
            deadline = time.monotonic() + 40
            while process.poll() is None:
                if time.monotonic() > deadline: raise RuntimeError("hidden MFA prompt did not complete")
                if select.select([master], [], [], 0.1)[0]:
                    transcript.extend(os.read(master, 8192))
                if not submitted and b"MFA code: " in transcript:
                    assert not termios.tcgetattr(slave)[3] & termios.ECHO
                    os.write(master, b"9876543210\n")
                    submitted = True
            while select.select([master], [], [], 0)[0]:
                transcript.extend(os.read(master, 8192))
            assert submitted and process.returncode == 0
            assert b"9876543210" not in transcript
            assert termios.tcgetattr(slave) == previous
        finally:
            if process.poll() is None: process.kill(); process.wait()
            os.close(master); os.close(slave)
        print("PASS: real XAuth hidden workstation prompt restores terminal and excludes code from output", flush=True)

    def disconnect(self):
        last_json(self.cli("connection", "disconnect", NAME, "--wait", "--json"))

    def capture(self, tag, action, expected=0, vpn=None):
        vpn = vpn or self.ids()["vpn"]["Id"]
        peer_index = int(command(["docker", "exec", vpn, "cat", "/sys/class/net/eth0/iflink"]).stdout)
        names = [name for index, name in socket.if_nameindex() if index == peer_index]
        assert len(names) == 1, "VPN veth peer is outside this isolated Docker host"
        observer = "/usr/local/bin/lab-capture"
        path = self.directory / ("wire-" + uuid.uuid4().hex)
        pcap, logfile = path.with_suffix(".pcap"), path.with_suffix(".log")
        output = logfile.open("wb")
        logfile.chmod(0o600)
        # Capture from the outer lab's stable network namespace. This retains no
        # VPN namespace descriptor, and sees the physical egress after encryption
        # before a plaintext route could forward through another bridge peer.
        process = subprocess.Popen(["sudo", "-n", observer, "--immediate-mode", "-Z", "root", "-Q", "in", "-n", "-i", names[0], "-U", "-w", str(pcap), "tcp port 28080 or udp port 28081 or port 53 or (udp and udp[8:4] = 0x76706e6d and udp[12:4] = 0x6f646f2d)"], stdin=subprocess.DEVNULL, stdout=subprocess.DEVNULL, stderr=output, start_new_session=True)
        try:
            deadline = time.monotonic() + 5
            while not pcap.exists() or pcap.stat().st_size < 24:
                if process.poll() is not None or time.monotonic() > deadline:
                    raise RuntimeError("synthetic wire observer could not start")
                time.sleep(0.05)
            action()
            time.sleep(0.3)
            assert process.poll() is None, "wire observer exited during the protected phase"
        finally:
            if process.poll() is None:
                command(["sudo", "-n", "kill", "-INT", str(process.pid)])
            process.wait(timeout=5)
            output.close()
        assert process.returncode == 0, "wire observer did not finish cleanly"
        raw = command(["sudo", "-n", observer, "-nn", "-r", str(pcap)]).stdout
        count = len(raw.splitlines())
        stats = logfile.read_text()
        received = re.search(r"(\d+) packets? received by filter",stats)
        dropped = re.search(r"(\d+) packets? dropped by kernel",stats)
        assert received and dropped and int(dropped[1]) == 0, "wire observer lost packets or statistics"
        received = int(received[1])
        pcap.unlink(); logfile.unlink()
        if expected:
            assert count >= expected, "wire observer/escape path positive control failed"
        else:
            assert count == 0 and received == 0, "plaintext escaped during " + tag
        print("PASS: wire capture", tag, "plaintext=" + str(count), flush=True)

    def probes(self, application=False):
        cid = self.ids()["application" if application else "vpn"]["Id"]
        # All attempts run as the same unprivileged UID as managed workloads.
        for script in (
            "nc -z -w 1 192.0.2.2 28080",
            "printf vpnmodo-probe | nc -u -w 1 192.0.2.2 28081",
            "timeout 2 nslookup private.example 192.0.2.2",
        ):
            command(["docker", "exec", "--user", "65534:65534", cid, "sh", "-c", script], required=False)

    def close(self):
        if self.profile_created:
            self.cli("connection", "disconnect", NAME, "--wait", "--json", required=False)
        for name in self.created:
            command(["docker", "rm", "-f", name], required=False)
        if self.network:
            for data in self.ids().values():
                command(["docker", "rm", "-f", data["Id"]], required=False)
            command(["docker", "network", "rm", NETWORK], required=False)


def begin(cli, directory, state_root, bridge, cli_args):
    lab = Lab(cli, bridge, cli_args, directory)
    try:
        assert command(["docker", "network", "inspect", NETWORK], required=False).returncode != 0
        args = ["docker", "network", "create", "--subnet", "192.0.2.0/24", "--gateway", "192.0.2.254"]
        for label in ("io.vpnmodo.managed-by=vpnmodo", "io.vpnmodo.profile=" + NAME, "io.vpnmodo.backend=ipsec-xauth", "com.docker.compose.project=" + PROJECT, "com.docker.compose.network=default"):
            args += ["--label", label]
        command([*args, NETWORK])
        lab.network = True
        for name, address, extra in ((lab.gateway, "192.0.2.1", []), (lab.sink, "192.0.2.2", ["--entrypoint", "python3"])):
            command(["docker", "run", "-d", "--name", name, "--network", NETWORK, "--ip", address, "--cap-add", "NET_ADMIN", "--cap-add", "NET_RAW", "--log-driver", "none", *extra, "vpnmodo-xauth-gateway:acceptance", *(["/usr/local/bin/sink.py"] if extra else [])])
            lab.created.append(name)
        command(["docker", "exec", lab.sink, "mkdir", "-p", "/run/lab"])
        command(["docker", "exec", lab.sink, "ip", "route", "add", "198.51.100.0/24", "via", "192.0.2.1"])
        for _ in range(50):
            if command(["docker", "exec", lab.gateway, "test", "-f", "/run/lab/ready"], required=False).returncode == 0:
                break
            time.sleep(0.5)
        else:
            raise RuntimeError("synthetic XAuth gateway startup failed")
        # Before any VPN process binds IKE sockets, an adversarial workload can
        # bind ports 500/4500 but must not use root's plaintext IKE exception.
        guard = "vpnmodo-xauth-guard-" + uuid.uuid4().hex[:8]
        guard_id = command(["docker", "run", "-d", "--name", guard, "--network", NETWORK, "--cap-add", "NET_ADMIN", "--cap-add", "NET_RAW", "--sysctl", "net.ipv6.conf.all.disable_ipv6=1", "--sysctl", "net.ipv6.conf.default.disable_ipv6=1", "--sysctl", "net.ipv4.ip_unprivileged_port_start=0", "--env", "VPN_SERVER=192.0.2.1", "--read-only", "--tmpfs", "/run:mode=0755", "--entrypoint", "sh", "vpnmodo-xauth:dev", "-c", "mkdir -m 700 /run/vpn; vpn-enforce; exec sleep infinity"]).stdout.decode().strip()
        lab.created.append(guard)
        for _ in range(50):
            if command(["docker", "exec", guard_id, "test", "-f", "/run/vpn/enforcement.sha256"], required=False).returncode == 0: break
            time.sleep(0.1)
        else: raise RuntimeError("pre-login namespace enforcement did not start")
        raw_ike_probe = """import socket
for port in (500,4500):
    with socket.socket(socket.AF_INET,socket.SOCK_DGRAM) as s:
        s.bind(('0.0.0.0',port))
        assert s.sendto(b'vpnmodo-bypass',('192.0.2.1',port)) == len(b'vpnmodo-bypass')
"""
        def send_ike():
            command(["docker", "run", "--rm", "--network", "container:" + guard_id, "--user", "65534:65534", "--cap-drop", "ALL", "--security-opt", "no-new-privileges:true", "--read-only", "--entrypoint", "python3", "vpnmodo-xauth-gateway:acceptance", "-c", raw_ike_probe])
        command(["docker", "exec", guard_id, "iptables", "-P", "OUTPUT", "ACCEPT"])
        try: lab.capture("pre-vpn-ike-positive-control", send_ike, expected=1, vpn=guard_id)
        finally: command(["docker", "exec", guard_id, "vpn-enforce"])
        lab.capture("pre-vpn-workload-cannot-use-ike-exceptions", send_ike, vpn=guard_id)
        command(["docker", "rm", "-f", guard_id])
        lab.created.remove(guard)
        definition = {"schema_version": 1, "name": NAME, "backend": {"type": "ipsec-xauth", "config": {"server": "192.0.2.1", "dns": ["198.51.100.1"], "ike_proposals": ["aes256-sha256-modp2048"], "esp_proposals": ["aes256-sha256"]}}, "health": {"host": "198.51.100.1", "port": 28080}}
        source = directory / "xauth-profile.json"
        source.write_text(json.dumps(definition))
        credentials = {"username": "synthetic-user", "password": "synthetic-password", "psk": "synthetic-shared"}
        cli("profile", "create", NAME, "--host", "example", "--file", str(source), "--credentials-stdin", "--json", data=json.dumps(credentials).encode())
        lab.profile_created = True
        challenge = lab.ensure_challenge()
        assert "application" not in lab.ids(), "application attached before authentication"
        cid = lab.ids()["vpn"]["Id"]
        command(["docker", "exec", cid, "vpn-enforce", "check"])
        # Prove the identical sender/sink/filter would observe a plaintext bypass.
        command(["docker", "exec", cid, "iptables", "-P", "OUTPUT", "ACCEPT"])
        try:
            lab.capture("positive-control", lab.probes, expected=1)
        finally:
            command(["docker", "exec", cid, "vpn-enforce"])
        lab.capture("waiting-mfa", lab.probes)
        command(["docker", "exec", cid, "vpn-enforce", "check"])
        cli("operation", "submit-mfa", challenge["id"], "--host", "example", "--challenge", challenge["challenge"]["id"], "--code-stdin", "--json", data=b"9876543210")
        lab.wait_operation(challenge["id"], "succeeded")
        ids = lab.ids()
        cid = ids["vpn"]["Id"]
        command(["docker", "exec", cid, "vpn-tunnelcheck"])
        command(["docker", "exec", ids["application"]["Id"], "nc", "-z", "-w", "3", "198.51.100.1", "28080"])
        command(["docker", "exec", ids["application"]["Id"], "nslookup", "-type=A", "private.example", "127.0.0.1"])
        assert command(["docker", "exec", cid, "test", "-e", "/run/vpn/code"], required=False).returncode != 0
        assert ids["application"]["Config"]["User"] == "65534:65534"
        assert ids["application"]["HostConfig"]["CapDrop"] == ["ALL"]
        for name in ("username", "password", "psk"):
            assert credentials[name].encode() not in json.dumps(ids).encode()
        # The kernel alone can supply the post-encryption mark. A workload
        # with the same UID/capability boundary cannot forge it through SO_MARK.
        mark_probe = """import socket,errno
s=socket.socket(socket.AF_INET,socket.SOCK_DGRAM)
try: s.setsockopt(socket.SOL_SOCKET,socket.SO_MARK,0x76706e6d)
except PermissionError: pass
else: raise RuntimeError('workload forged output mark')
s=socket.socket(socket.AF_INET6,socket.SOCK_STREAM)
s.settimeout(1)
try: s.connect(('2001:db8::2',28080))
except OSError as e:
    assert e.errno in (errno.ENETUNREACH,errno.EADDRNOTAVAIL,errno.EAFNOSUPPORT,errno.EHOSTUNREACH)
else: raise RuntimeError('workload IPv6 unexpectedly connected')
"""
        command(["docker", "run", "--rm", "--network", "container:" + cid, "--user", "65534:65534", "--cap-drop", "ALL", "--security-opt", "no-new-privileges:true", "--read-only", "--entrypoint", "python3", "vpnmodo-xauth-gateway:acceptance", "-c", mark_probe])
        print("PASS: real aggressive PSK + XAUTH_PASSCODE, secure code consumption, private TCP/DNS", flush=True)
        def rekey():
            command(["docker", "exec", lab.gateway, "swanctl", "--rekey", "--child", "lab"])
            command(["docker", "exec", cid, "vpn-servicecheck"])
            lab.probes(application=True)
        lab.capture("authenticated-child-rekey", rekey)
        # Neither Docker DNS nor IPv6 can bypass the namespace policy.
        app = ids["application"]["Id"]
        assert command(["docker", "exec", app, "cat", "/proc/sys/net/ipv6/conf/all/disable_ipv6"]).stdout.strip() == b"1"
        assert command(["docker", "exec", app, "cat", "/proc/sys/net/ipv6/conf/default/disable_ipv6"]).stdout.strip() == b"1"
        assert command(["docker", "exec", app, "timeout", "2", "nslookup", "private.example", "127.0.0.11"], required=False).returncode != 0
        assert not command(["docker", "exec", app, "cat", "/proc/net/if_inet6"]).stdout.strip()
        assert command(["docker", "exec", app, "sh", "-c", "echo 0 > /proc/sys/net/ipv6/conf/all/disable_ipv6"], required=False).returncode != 0
        print("PASS: Docker DNS fallback and workload IPv6 bypass are blocked", flush=True)
        # A quiet interval longer than the old Expect login timeout must survive.
        command(["docker", "pause", "vpnmodo-controller"])
        try:
            time.sleep(95)
        finally:
            command(["docker", "unpause", "vpnmodo-controller"])
        command(["docker", "exec", cid, "vpn-servicecheck"])
        last_json(cli("connection", "ensure", NAME, "--wait", "--json"))
        assert lab.ids()["vpn"]["Id"] == cid
        print("PASS: quiet authenticated session survives login timeout and ensure reuses it", flush=True)
        # Active TCP/UDP flows continue sending while both daemon/controller fail
        # and kernel policies disappear. No ESTABLISHED bypass may leak them.
        app = ids["application"]["Id"]
        active = 'while :; do printf vpnmodo-active; sleep 0.1; done | nc -w 30 192.0.2.2 28080'
        command(["docker", "exec", "-d", app, "sh", "-c", active])
        command(["docker", "exec", "-d", app, "sh", "-c", 'while :; do printf vpnmodo-udp; sleep 0.1; done | nc -u -w 30 192.0.2.2 28081'])
        time.sleep(0.5)
        assert b"ESTAB" in command(["docker", "exec", cid, "ss", "-nt", "dst", "192.0.2.2"]).stdout
        command(["docker", "pause", "vpnmodo-controller"])
        try:
            def remove_states():
                command(["docker", "exec", cid, "sh", "-c", "kill -STOP $(pidof charon-cmd)"])
                command(["docker", "exec", cid, "ip", "xfrm", "state", "flush"])
                lab.probes(application=True)
            lab.capture("active-flows-no-sa-controller-adapter-paused", remove_states)
            def remove_policies():
                command(["docker", "exec", cid, "ip", "xfrm", "policy", "flush"])
                lab.probes(application=True)
                assert command(["docker", "exec", cid, "vpn-tunnelcheck"], required=False).returncode != 0
            lab.capture("active-flows-no-policies-controller-adapter-paused", remove_policies)
            # Validate the observer again after table 220 has changed routing:
            # an ordinary-interface fallback through the gateway must be visible.
            def lost_policy_positive():
                command(["docker", "exec", cid, "iptables", "-P", "OUTPUT", "ACCEPT"])
                try: lab.probes(application=True)
                finally: command(["docker", "exec", cid, "vpn-enforce"])
            lab.capture("lost-policy-positive-control", lost_policy_positive, expected=1)
            command(["docker", "exec", cid, "sh", "-c", "kill -KILL $(pidof charon-cmd)"])
            time.sleep(0.5)
        finally:
            command(["docker", "unpause", "vpnmodo-controller"])
        # No unattended PSK or password attempt after a loss requiring fresh MFA.
        time.sleep(8)
        connection = last_json(cli("connection", "status", NAME, "--json"))
        assert not connection["recovery_enabled"]
        lab.disconnect()
        snap = lab.ensure_challenge()
        lab.capture("recreated-before-authentication", lab.probes)
        cli("operation", "cancel", snap["id"], "--host", "example", "--json")
        lab.wait_operation(snap["id"], "cancelled")
        lab.disconnect()
        # Expiry and invalid codes are bounded without another login/challenge.
        expired = lab.ensure_challenge()
        lab.wait_operation(expired["id"], "failed", timeout=60)
        stale = cli("operation", "submit-mfa", expired["id"], "--host", "example", "--challenge", expired["challenge"]["id"], "--code-stdin", "--json", data=b"9876543210", required=False)
        assert stale.returncode != 0
        assert not lab.ids()
        lab.disconnect()
        bad = lab.ensure_challenge()
        cli("operation", "submit-mfa", bad["id"], "--host", "example", "--challenge", bad["challenge"]["id"], "--code-stdin", "--json", data=b"111111")
        lab.wait_operation(bad["id"], "failed")
        assert not lab.ids()
        lab.disconnect()
        print("PASS: expired/stale and incorrect MFA cannot replay or trigger unattended login", flush=True)
        # Request another PIN in the same real IKEv1 conversation. The managed
        # client must refuse rather than replay its credential cache.
        command(["docker", "exec", lab.gateway, "sed", "-i", "/passcode = Synthetic code/a\\          nextpin = Repeated synthetic code", "/etc/strongswan.conf"])
        command(["docker", "exec", lab.gateway, "swanctl", "--reload-settings"])
        repeated = lab.ensure_challenge()
        cli("operation", "submit-mfa", repeated["id"], "--host", "example", "--challenge", repeated["challenge"]["id"], "--code-stdin", "--json", data=b"9876543210")
        lab.wait_operation(repeated["id"], "failed")
        assert not lab.ids()
        command(["docker", "exec", lab.gateway, "sed", "-i", "/nextpin = Repeated synthetic code/d", "/etc/strongswan.conf"])
        command(["docker", "exec", lab.gateway, "swanctl", "--reload-settings"])
        lab.disconnect()
        print("PASS: repeated real XAuth PIN request is refused without cached code replay", flush=True)
        # Crash while awaiting a code: dispose the interrupted namespace and
        # challenge, preserve the vault, and require a new explicit attempt.
        pending = lab.ensure_challenge()
        old_vpn = lab.ids()["vpn"]["Id"]
        controller = json.loads(command(["docker", "inspect", "vpnmodo-controller"]).stdout)[0]
        assert controller["Config"]["Labels"]["io.vpnmodo.managed-by"] == "vpnmodo"
        assert controller["Config"]["Labels"]["io.vpnmodo.component"] == "controller"
        assert controller["State"]["Running"] and controller["State"]["Pid"] > 1
        # Docker kill is an intentional administrative stop and suppresses its
        # restart policy. Kill the owned process from this isolated parent PID
        # namespace to simulate a crash that Docker should restart automatically.
        command(["sudo", "-n", "kill", "-KILL", str(controller["State"]["Pid"])])
        for _ in range(60):
            if cli("host", "check", "example", "--json", required=False).returncode == 0:
                break
            time.sleep(0.3)
        else:
            raise RuntimeError("owned controller did not restart automatically after process crash")
        interrupted = lab.wait_operation(pending["id"], "failed")
        assert interrupted["failure"]["code"] == -32008
        for _ in range(60):
            if not lab.ids(): break
            time.sleep(0.2)
        assert not lab.ids(), "interrupted login namespace survived controller restart"
        lab.disconnect()
        print("PASS: controller crash discards interrupted MFA namespace without fresh login", flush=True)
        # Establish another real session for controller replacement acceptance.
        lab.authenticate(interactive=True)
        lab.vpn_before_restart = lab.ids()["vpn"]["Id"]
        return lab
    except Exception:
        # Only synthetic lab output is eligible for local failure diagnostics.
        for role, name in (("gateway", lab.gateway),):
            if name in lab.created:
                for file in ("load-output", "charon-output", "radius-output"):
                    data = command(["docker", "exec", name, "tail", "-n", "10", "/run/lab/" + file], required=False).stdout
                    print("Synthetic XAuth", role, file, data.decode(errors="replace"), flush=True)
        trace = state_root / "l2tp-trace.json"
        if trace.is_file(): print("XAuth command phases", json.loads(trace.read_text())[-35:], flush=True)
        for role, data in lab.ids().items() if lab.network else ():
            print("XAuth process", role, data["State"]["Running"], data["State"]["ExitCode"], flush=True)
        lab.close()
        raise


def after_restart(cli, state_root, lab):
    assert lab.ids()["vpn"]["Id"] == lab.vpn_before_restart
    command(["docker", "exec", lab.vpn_before_restart, "vpn-servicecheck"])
    last_json(cli("connection", "ensure", NAME, "--wait", "--json"))
    assert lab.ids()["vpn"]["Id"] == lab.vpn_before_restart
    lab.disconnect()
    assert not list((state_root / "deployments" / NAME / "credentials").glob("*/*"))
    lab.close()
    lab.network = False
    cli("profile", "delete", NAME, "--revision", "1", "--json")
    lab.profile_created = False
    print("PASS: controller recreation preserves XAuth without reauthentication; disconnect disposes runtime credentials", flush=True)
