"""Synthetic ESP/NAT-T PMTU and fail-closed regression acceptance.

Run as root only inside the explicitly authorized isolated test container. Fresh
namespaces and SAs per scenario prevent learned PMTU from hiding regressions.
No real profiles, credentials, raw XFRM dumps or packet payload capture are used.
"""

import argparse
import json
import os
import pathlib
import platform
import re
import select
import shutil
import subprocess
import tempfile
import time
import uuid

if (
    os.environ.get("VPNMODO_ISOLATED_TEST_HOST") != "1"
    or platform.system() != "Linux"
    or os.geteuid() != 0
):
    raise SystemExit("run as root in the authorized isolated Linux test container")
parser = argparse.ArgumentParser(description=__doc__)
parser.add_argument(
    "--enforce",
    type=pathlib.Path,
    default=pathlib.Path(__file__).resolve().parents[1]
    / "containers/ipsec-xauth/managed/vpn-enforce",
)
options = parser.parse_args()
ENFORCE = options.enforce.read_text()
PMTU_RULE = "-A INPUT -p icmp --icmp-type fragmentation-needed -m conntrack --ctstate RELATED -j ACCEPT"
assert ENFORCE.count(PMTU_RULE) == 1, (
    "the PMTU regression control must remove exactly the correction"
)
BROKEN_ENFORCE = ENFORCE.replace(PMTU_RULE + "\n", "")
ROOT = pathlib.Path(tempfile.mkdtemp(prefix="pmtu-lab-"))


def command(args, required=True, input=None):
    try:
        r = subprocess.run(
            args, check=False, input=input, capture_output=True, text=True, timeout=15
        )
    except subprocess.TimeoutExpired:
        raise RuntimeError("synthetic command timed out: " + str(args[:3])) from None
    if required and r.returncode:
        raise RuntimeError(
            "synthetic command failed: " + str(args[:3]) + " " + r.stderr[-700:]
        )
    return r


def ip(ns, *args):
    return command(["ip", "netns", "exec", ns, "ip", *args]).stdout.strip()


def nsrun(ns, args, **kw):
    return command(["ip", "netns", "exec", ns, *args], **kw)


def popen(ns, code):
    return subprocess.Popen(
        ["ip", "netns", "exec", ns, "python3", "-u", "-c", code],
        stdout=subprocess.PIPE,
        stderr=subprocess.PIPE,
        text=True,
    )


def ready(child):
    assert select.select([child.stdout], [], [], 5)[0], "synthetic listener not ready"
    assert child.stdout.readline().strip() == "ready", "synthetic listener exited"


def counter_summary(ns):
    mangle = nsrun(ns, ["iptables-save", "-c", "-t", "mangle"]).stdout
    filters = nsrun(ns, ["iptables-save", "-c", "-t", "filter"]).stdout
    result = {"all": 0, "related": 0, "accepted_pmtu": 0, "input_drop": 0}
    for line in mangle.splitlines():
        match = re.match(r"\[(\d+):\d+\].*--comment pmtu-(all|related)", line)
        if match:
            result[match.group(2)] = int(match.group(1))
    for line in filters.splitlines():
        match = re.match(r":INPUT DROP \[(\d+):\d+\]", line)
        if match:
            result["input_drop"] = int(match.group(1))
        if (
            "-A INPUT -p icmp" in line
            and "--ctstate RELATED" in line
            and "-j ACCEPT" in line
        ):
            result["accepted_pmtu"] += int(re.match(r"\[(\d+):\d+\]", line).group(1))
    return result


def unrelated_icmp(client, router):
    before = counter_summary(client)
    code = """import socket,struct

def checksum(data):
 if len(data)%2:data+=b'\\0'
 total=sum(struct.unpack('!%dH'%(len(data)//2),data))
 total=(total&65535)+(total>>16);total=(total&65535)+(total>>16)
 return (~total)&65535

header=struct.pack('!BBHHHBBH4s4s',0x45,0,28,77,0x4000,64,17,0,socket.inet_aton('192.0.2.2'),socket.inet_aton('198.51.100.2'))
header=header[:10]+struct.pack('!H',checksum(header))+header[12:]
quote=header+struct.pack('!HHHH',65001,65002,8,0)
packet=struct.pack('!BBHHH',3,4,0,0,1280)+quote
packet=packet[:2]+struct.pack('!H',checksum(packet))+packet[4:]
s=socket.socket(socket.AF_INET,socket.SOCK_RAW,socket.IPPROTO_ICMP);s.sendto(packet,('192.0.2.2',0))
"""
    nsrun(router, ["python3", "-c", code])
    deadline = time.monotonic() + 2
    while True:
        after = counter_summary(client)
        if after["all"] > before["all"]:
            break
        assert time.monotonic() < deadline, "forged-ICMP positive observation missing"
        time.sleep(0.02)
    assert after["related"] == before["related"], "unrelated quote matched conntrack"
    assert after["accepted_pmtu"] == before["accepted_pmtu"], (
        "forged ICMP accepted by correction"
    )
    assert after["input_drop"] > before["input_drop"], (
        "unrelated ICMP did not hit fail-closed drop"
    )
    return {"observed": True, "related": False, "dropped": True}


def plaintext_controls(client, router, server, tag, children, filtered):
    state = ROOT / (tag + "-plaintext.json")
    code = """import socket,threading,json,pathlib
p=pathlib.Path(STATE)
counts={'tcp':0,'udp':0,'post_policy':0};lock=threading.Lock()
def record(key):
 with lock:
  counts[key]+=1;p.write_text(json.dumps(counts))
def tcp():
 s=socket.socket();s.setsockopt(socket.SOL_SOCKET,socket.SO_REUSEADDR,1);s.bind(('0.0.0.0',19001));s.listen()
 while True:
  c,_=s.accept();record('tcp');c.close()
def udp():
 s=socket.socket(socket.AF_INET,socket.SOCK_DGRAM);s.bind(('0.0.0.0',19002))
 while True:
  data,_=s.recvfrom(2048);record('post_policy' if data==b'post-policy' else 'udp')
p.write_text(json.dumps(counts))
threading.Thread(target=tcp,daemon=True).start();threading.Thread(target=udp,daemon=True).start()
print('ready',flush=True);threading.Event().wait()
""".replace("STATE", repr(str(state)))
    listener = popen(server, code)
    children.append(listener)
    ready(listener)
    time.sleep(0.05)
    probe = """import socket,os,json
os.setgroups([]);os.setgid(65534);os.setuid(65534)
s=socket.socket();s.settimeout(.4)
try:s.connect(('198.51.100.2',19001))
except OSError:pass
s.close();u=socket.socket(socket.AF_INET,socket.SOCK_DGRAM)
for payload,port in [(b'plaintext-probe',19002),(bytes(4)+b'vpnmodo-probe'+bytes(32),4500)]:
 try:u.sendto(payload,('198.51.100.2',port))
 except OSError:pass
try:u.setsockopt(socket.SOL_SOCKET,36,0x76706e6d);denied=False
except PermissionError:denied=True
print(json.dumps({'mark_denied':denied}))
"""
    result = json.loads(nsrun(client, ["python3", "-c", probe]).stdout)
    assert result["mark_denied"], "unprivileged workload forged the kernel mark"
    time.sleep(0.1)
    counts = json.loads(state.read_text())
    natt = ROOT / (tag + "-natt-plain")
    counts["ike_port_udp"] = int(natt.read_text()) if natt.exists() else 0
    if filtered:
        assert counts == {"tcp": 0, "udp": 0, "post_policy": 0, "ike_port_udp": 0}, (
            "plaintext escaped with enforcement"
        )
    else:
        assert all(counts[key] >= 1 for key in ("tcp", "udp", "ike_port_udp")), (
            "plaintext escape positive control failed"
        )
    # Remove all synthetic IPsec state/policy on both endpoints. Routing stays
    # usable: an unfiltered sender must demonstrably reach the plaintext sink.
    for ns in (client, server):
        ip(ns, "xfrm", "policy", "flush")
        ip(ns, "xfrm", "state", "flush")
    nsrun(
        client,
        [
            "python3",
            "-c",
            "import socket,os;os.setgroups([]);os.setgid(65534);os.setuid(65534);s=socket.socket(socket.AF_INET,socket.SOCK_DGRAM)\ntry:s.sendto(b'post-policy',('203.0.113.2',19002))\nexcept OSError:pass",
        ],
    )
    time.sleep(0.1)
    after = json.loads(state.read_text())
    if filtered:
        assert after["post_policy"] == 0, "plaintext escaped after policy loss"
    else:
        assert after["post_policy"] >= 1, "post-policy positive control missing"
    return {
        "received": counts,
        "post_policy_received": after["post_policy"],
        "mark_denied": True,
    }


def scenario(mode):
    tag = uuid.uuid4().hex[:6]
    c = "pc" + tag
    r = "pr" + tag
    s = "ps" + tag
    namespaces = []
    children = []
    result = {"mode": mode}

    def link(a, aname, addr, mtu, b, bname, baddr, bmtu):
        left = "a" + uuid.uuid4().hex[:8]
        right = "b" + uuid.uuid4().hex[:8]
        command(["ip", "link", "add", left, "type", "veth", "peer", "name", right])
        command(["ip", "link", "set", left, "netns", a])
        command(["ip", "link", "set", right, "netns", b])
        for ns, old, new, address, limit in [
            (a, left, aname, addr, mtu),
            (b, right, bname, baddr, bmtu),
        ]:
            ip(ns, "link", "set", old, "name", new)
            ip(ns, "addr", "add", address, "dev", new)
            ip(ns, "link", "set", new, "mtu", str(limit), "up")

    try:
        for name in (c, r, s):
            command(["ip", "netns", "add", name])
            namespaces.append(name)
            ip(name, "link", "set", "lo", "up")
        link(c, "eth0", "192.0.2.2/24", 1500, r, "left", "192.0.2.1/24", 1500)
        link(r, "right", "198.51.100.1/24", 1280, s, "eth0", "198.51.100.2/24", 1280)
        nsrun(r, ["sysctl", "-qw", "net.ipv4.ip_forward=1"])
        nsrun(
            r,
            [
                "iptables",
                "-t",
                "nat",
                "-A",
                "POSTROUTING",
                "-o",
                "right",
                "-j",
                "MASQUERADE",
            ],
        )
        ip(c, "addr", "add", "203.0.113.1/32", "dev", "lo")
        ip(s, "addr", "add", "203.0.113.2/32", "dev", "lo")
        ip(c, "route", "add", "default", "via", "192.0.2.1", "dev", "eth0")
        ip(s, "route", "add", "default", "via", "198.51.100.1", "dev", "eth0")
        ip(r, "route", "add", "203.0.113.2/32", "via", "198.51.100.2", "dev", "right")
        ip(r, "route", "add", "203.0.113.1/32", "via", "192.0.2.2", "dev", "left")
        route = [
            "route",
            "add",
            "203.0.113.2/32",
            "via",
            "192.0.2.1",
            "dev",
            "eth0",
            "src",
            "203.0.113.1",
            "mtu",
        ]
        if mode in ("legacy-auto", "managed-fixed-auto"):
            ip(c, *route[:-1])
        else:
            ip(c, *route, *(["lock", "1280"] if mode == "managed-locked" else ["1280"]))
        ip(
            s,
            "route",
            "add",
            "203.0.113.1/32",
            "via",
            "198.51.100.1",
            "dev",
            "eth0",
            "src",
            "203.0.113.2",
        )
        for ns in (c, s):
            udp_code = "import socket,time,pathlib; s=socket.socket(socket.AF_INET,socket.SOCK_DGRAM); s.setsockopt(socket.IPPROTO_UDP,100,2); s.bind(('0.0.0.0',4500)); print('ready',flush=True);\n"
            if ns == s:
                udp_code += (
                    "p=pathlib.Path("
                    + repr(str(ROOT / (tag + "-natt-plain")))
                    + ");count=0\nwhile True:\n data,_=s.recvfrom(4096)\n if b'vpnmodo-probe' in data:count+=1;p.write_text(str(count))\n"
                )
            else:
                udp_code += "time.sleep(120)"
            udp = popen(ns, udp_code)
            children.append(udp)
            ready(udp)
        keys = [(os.urandom(32).hex(), os.urandom(16).hex()) for _ in range(2)]

        def state(ns, src, dst, spi, reqid, key, mark=False):
            auth, enc = key
            args = [
                "xfrm",
                "state",
                "add",
                "src",
                src,
                "dst",
                dst,
                "proto",
                "esp",
                "spi",
                spi,
                "reqid",
                str(reqid),
                "mode",
                "tunnel",
                "replay-window",
                "32",
                "auth-trunc",
                "hmac(sha256)",
                "0x" + auth,
                "128",
                "enc",
                "cbc(aes)",
                "0x" + enc,
                "encap",
                "espinudp",
                "4500",
                "4500",
                "0.0.0.0",
            ]
            if mark:
                args += ["output-mark", "0x76706e6d"]
            ip(ns, *args)

        state(c, "192.0.2.2", "198.51.100.2", "0x100", 1, keys[0], True)
        state(s, "198.51.100.1", "198.51.100.2", "0x100", 1, keys[0])
        state(s, "198.51.100.2", "198.51.100.1", "0x200", 2, keys[1])
        state(c, "198.51.100.2", "192.0.2.2", "0x200", 2, keys[1])

        def policy(ns, direction, src, dst, outer_src, outer_dst, reqid):
            ip(
                ns,
                "xfrm",
                "policy",
                "add",
                "dir",
                direction,
                "src",
                src + "/32",
                "dst",
                dst + "/32",
                "tmpl",
                "src",
                outer_src,
                "dst",
                outer_dst,
                "proto",
                "esp",
                "reqid",
                str(reqid),
                "mode",
                "tunnel",
            )

        policy(c, "out", "203.0.113.1", "203.0.113.2", "192.0.2.2", "198.51.100.2", 1)
        policy(s, "in", "203.0.113.1", "203.0.113.2", "198.51.100.1", "198.51.100.2", 1)
        policy(
            s, "out", "203.0.113.2", "203.0.113.1", "198.51.100.2", "198.51.100.1", 2
        )
        policy(c, "in", "203.0.113.2", "203.0.113.1", "198.51.100.2", "192.0.2.2", 2)
        # The first encrypted SYN creates the NAT-T conntrack mapping.
        if mode != "legacy-auto":
            for setting in ("all", "default"):
                nsrun(
                    c, ["sysctl", "-qw", "net.ipv6.conf." + setting + ".disable_ipv6=1"]
                )
            script = (
                ENFORCE
                if mode in ("managed-fixed-unlocked", "managed-fixed-auto")
                else BROKEN_ENFORCE
            )
            path = pathlib.Path("/usr/local/bin/vpn-enforce")
            path.write_text(script)
            path.chmod(0o755)
            (pathlib.Path("/run/vpn")).mkdir(mode=0o700, parents=True, exist_ok=True)
            nsrun(c, ["env", "VPN_SERVER=198.51.100.2", "sh", str(path)])
        for extra, label in [
            ([], "all"),
            (["-m", "conntrack", "--ctstate", "RELATED"], "related"),
        ]:
            nsrun(
                c,
                [
                    "iptables",
                    "-t",
                    "mangle",
                    "-A",
                    "PREROUTING",
                    "-p",
                    "icmp",
                    "--icmp-type",
                    "3/4",
                    *extra,
                    "-m",
                    "comment",
                    "--comment",
                    "pmtu-" + label,
                ],
            )
        server_code = """import socket,threading
s=socket.socket();s.setsockopt(socket.SOL_SOCKET,socket.SO_REUSEADDR,1);s.bind(('203.0.113.2',18080));s.listen();print('ready',flush=True)
def handle(c):
 try:
  c.settimeout(12);data=bytearray()
  while len(data)<262144:
   b=c.recv(262144-len(data))
   if not b:break
   data.extend(b)
  if len(data)==262144:c.sendall(data)
 except OSError:pass
 finally:c.close()
while True:
 c,_=s.accept();threading.Thread(target=handle,args=(c,),daemon=True).start()
"""
        server = popen(s, server_code)
        children.append(server)
        ready(server)
        client_code = """import socket,os,json,time
os.setgroups([]);os.setgid(65534);os.setuid(65534)
s=socket.socket();s.settimeout(8);s.bind(('203.0.113.1',0));start=time.monotonic()
try:
 s.connect(('203.0.113.2',18080));mss=s.getsockopt(socket.IPPROTO_TCP,socket.TCP_MAXSEG);s.sendall(bytes(262144));data=bytearray()
 while len(data)<262144:
  b=s.recv(262144-len(data))
  if not b:break
  data.extend(b)
 print(json.dumps({'ok':data==bytes(262144),'bytes':len(data),'tcp_maxseg':mss,'seconds':round(time.monotonic()-start,2)}))
except OSError as e:print(json.dumps({'ok':False,'error_type':type(e).__name__,'seconds':round(time.monotonic()-start,2)}))
finally:s.close()
"""
        probe = nsrun(c, ["python3", "-c", client_code])
        result["transfer"] = json.loads(probe.stdout)
        result["pmtu_counters"] = counter_summary(c)
        if mode in ("managed-unlocked", "managed-fixed-unlocked", "managed-fixed-auto"):
            assert result["pmtu_counters"]["related"] >= 1, (
                "valid PMTU feedback positive control missing"
            )
        if mode in ("managed-fixed-unlocked", "managed-fixed-auto"):
            assert result["pmtu_counters"]["accepted_pmtu"] >= 1, (
                "correction did not accept valid feedback"
            )
            result["forged_icmp"] = unrelated_icmp(c, r)
        if mode == "managed-unlocked":
            assert (
                result["pmtu_counters"]["input_drop"]
                >= result["pmtu_counters"]["related"]
            ), "broken-rule drop did not reproduce"
        result["plaintext_controls"] = plaintext_controls(
            c, r, s, tag, children, mode != "legacy-auto"
        )
        return result
    finally:
        for child in children:
            if child.poll() is None:
                child.terminate()
            try:
                child.wait(timeout=3)
            except subprocess.TimeoutExpired:
                child.kill()
                child.wait()
        for name in reversed(namespaces):
            command(["ip", "netns", "delete", name], required=False)


try:
    results = []
    for mode in (
        "legacy-auto",
        "managed-unlocked",
        "managed-locked",
        "managed-fixed-unlocked",
        "managed-fixed-auto",
    ):
        result = scenario(mode)
        results.append(result)
        print(json.dumps(result), flush=True)
    assert results[0]["transfer"]["ok"], "legacy positive control failed"
    assert not results[1]["transfer"]["ok"], (
        "managed baseline did not reproduce failure"
    )
    assert results[2]["transfer"]["ok"], "locked route workaround failed"
    assert results[3]["transfer"]["ok"], "related-ICMP correction failed"
    assert results[4]["transfer"]["ok"], "automatic MTU with the correction failed"
    print("PASS: synthetic NAT-T PMTU regression reproduced and corrected", flush=True)
finally:
    shutil.rmtree(ROOT)
