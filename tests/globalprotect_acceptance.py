#!/usr/bin/env python3
"""Exercise real OpenConnect against a synthetic GP HTTPS gateway on local Linux.

Requires VPNMODO_ISOLATED_TEST_HOST=1 and a prebuilt vpnmodo-globalprotect:dev
image. All resources use unique names. No existing VPN/controller is accessed.
This is implementation acceptance, not Palo Alto appliance interoperability.
"""
import json
import os
from pathlib import Path
import subprocess
import tempfile
import time
import uuid

IMAGE = os.environ.get('VPNMODO_GLOBALPROTECT_TEST_IMAGE', 'vpnmodo-globalprotect:dev')
ROOT = Path(__file__).resolve().parent


def docker(*args, data=None, required=True):
    result = subprocess.run(['docker', *args], input=data, capture_output=True, timeout=30)
    if required and result.returncode:
        raise RuntimeError('GlobalProtect lab command failed: ' + args[0])
    return result


def wait(check, seconds=25):
    deadline = time.monotonic() + seconds
    while time.monotonic() < deadline:
        if check():
            return
        time.sleep(.2)
    raise RuntimeError('GlobalProtect lab deadline exceeded')


class Lab:
    def __init__(self, root):
        self.root = root
        self.prefix = 'vpnmodo-gp-test-' + uuid.uuid4().hex[:12]
        self.net = self.prefix + '-net'
        self.created = []
        self.client = None

    def create(self, role, *args):
        name = self.prefix + '-' + role
        self.created.append(name)
        docker('create', '--name', name, '--label', 'io.vpnmodo.testlab=globalprotect', '--label', 'io.vpnmodo.testlab.id=' + self.prefix, '--log-driver', 'none', *args)
        return name

    def copy(self, source, container, target):
        docker('cp', str(source), container + ':' + target)

    def gateway(self):
        docker('network', 'create', '--label', 'io.vpnmodo.testlab=globalprotect', '--label', 'io.vpnmodo.testlab.id=' + self.prefix, self.net)
        # Generated key material stays in a private temporary directory.
        for name in ('ca', 'other'):
            subprocess.run(['openssl', 'req', '-x509', '-newkey', 'rsa:2048', '-nodes', '-days', '1', '-subj', '/CN=Synthetic ' + name,
                            '-addext', 'basicConstraints=critical,CA:TRUE', '-keyout', str(self.root / (name + '.key')), '-out', str(self.root / (name + '.crt'))], check=True, capture_output=True)
        subprocess.run(['openssl', 'req', '-newkey', 'rsa:2048', '-nodes', '-subj', '/CN=gateway.example.test', '-keyout', str(self.root / 'server.key'), '-out', str(self.root / 'server.csr')], check=True, capture_output=True)
        (self.root / 'extensions').write_text('subjectAltName=DNS:gateway.example.test\nbasicConstraints=critical,CA:FALSE\nextendedKeyUsage=serverAuth\n')
        subprocess.run(['openssl', 'x509', '-req', '-in', str(self.root / 'server.csr'), '-CA', str(self.root / 'ca.crt'), '-CAkey', str(self.root / 'ca.key'), '-CAcreateserial', '-days', '1', '-extfile', str(self.root / 'extensions'), '-out', str(self.root / 'server.crt')], check=True, capture_output=True)
        self.peer = self.create('gateway', '--network', self.net, '--cap-add', 'NET_ADMIN', '--device', '/dev/net/tun', '--entrypoint', 'python3', IMAGE, '/opt/gateway.py')
        # A stopped container accepts copies into its writable layer; no host
        # directories, Docker socket or consumer files are mounted into it.
        docker('cp', str(self.root), self.peer + ':/run/gp-test')
        self.copy(ROOT / 'globalprotect-lab/gateway.py', self.peer, '/opt/gateway.py')
        docker('start', self.peer)
        wait(lambda: docker('exec', self.peer, 'test', '-f', '/run/gp-test/ready', required=False).returncode == 0)
        self.ip = json.loads(docker('inspect', self.peer).stdout)[0]['NetworkSettings']['Networks'][self.net]['IPAddress']

    def mode(self, mode):
        docker('exec', '-i', self.peer, 'python3', '-c', "import sys;from pathlib import Path;Path('/run/gp-test/mode').write_bytes(sys.stdin.buffer.read())", data=mode.encode())

    def start(self, *, password='synthetic-password', trust='ca.crt', host='gateway.example.test', insecure=False, health=True, mtu=None):
        if self.client:
            docker('rm', '-f', self.client)
            self.created.remove(self.client)
        (self.root / 'username').write_text('synthetic-user')
        (self.root / 'password').write_text(password)
        (self.root / 'resolv.conf').write_text('nameserver 127.0.0.1\noptions ndots:0\n')
        args = ['--network', self.net, '--cap-add', 'NET_ADMIN', '--cap-add', 'NET_RAW', '--device', '/dev/net/tun', '--read-only', '--security-opt', 'no-new-privileges:true', '--init', '--tmpfs', '/run:rw,nosuid,nodev,mode=0755', '--tmpfs', '/tmp:rw,nosuid,nodev,noexec,mode=1777', '--sysctl', 'net.ipv6.conf.all.disable_ipv6=1', '--sysctl', 'net.ipv6.conf.default.disable_ipv6=1']
        settings = {'VPN_SERVER': self.ip, 'VPN_GATEWAY_HOST': host, 'VPN_DNS': '198.51.100.53', 'VPN_INSECURE_SKIP_TLS_VERIFY': str(insecure).lower()}
        if health:
            settings.update(VPN_HEALTH_HOST='service.example.test', VPN_HEALTH_PORT='8443')
        if mtu is not None:
            settings['VPN_MTU'] = str(mtu)
        for key, value in settings.items():
            args += ['-e', key + '=' + value]
        secrets = [('username', 'username'), ('password', 'password')]
        if not insecure:
            secrets.append(('ca_cert', trust))
        for key, source in secrets:
            path = self.root / source
            path.chmod(0o600)
            target = '/run/secrets/vpn_' + key
            args += ['--mount', 'type=bind,src=' + str(path) + ',dst=' + target + ',readonly', '-e', 'VPN_' + key.upper() + '_FILE=' + target]
        args += ['--mount', 'type=bind,src=' + str(self.root / 'resolv.conf') + ',dst=/etc/resolv.conf,readonly']
        self.client = self.create('client', *args, IMAGE)
        docker('start', self.client)

    def stopped(self):
        state = json.loads(docker('inspect', self.client).stdout)[0]['State']
        return not state['Running'] and state['ExitCode'] == 2

    def stats(self):
        return json.loads(docker('exec', self.peer, 'cat', '/run/gp-test/stats').stdout)

    def plaintext_count(self):
        rows = docker('exec', self.peer, 'iptables', '-nvxL', 'GPTEST').stdout.decode().splitlines()
        return int(rows[2].split()[0])

    def probe(self, host, port, *, network=None, blocked=False, udp=False):
        code = "import socket,sys;s=socket.socket(socket.AF_INET,socket.SOCK_DGRAM if sys.argv[3]=='udp' else socket.SOCK_STREAM);s.settimeout(1);s.connect((sys.argv[1],int(sys.argv[2])));s.send(b'synthetic-probe');assert s.recv(64)==b'synthetic-probe'"
        result = docker('run', '--rm', '--network', network or 'container:' + self.client, '--user', '65534:65534', '--cap-drop', 'ALL', '--security-opt', 'no-new-privileges:true', '--log-driver', 'none', '--entrypoint', 'python3', IMAGE, '-c', code, host, str(port), 'udp' if udp else 'tcp', required=False)
        if (result.returncode == 0) == blocked:
            raise RuntimeError('GlobalProtect packet policy assertion failed')

    def close(self):
        for name in reversed(self.created):
            owner = docker('inspect', '--format', '{{index .Config.Labels "io.vpnmodo.testlab.id"}}', name, required=False)
            if owner.returncode == 0 and owner.stdout.decode().strip() == self.prefix:
                docker('rm', '-f', name, required=False)
        owner = docker('network', 'inspect', '--format', '{{index .Labels "io.vpnmodo.testlab.id"}}', self.net, required=False)
        if owner.returncode == 0 and owner.stdout.decode().strip() == self.prefix:
            docker('network', 'rm', self.net, required=False)


def main():
    if os.environ.get('VPNMODO_ISOLATED_TEST_HOST') != '1':
        raise RuntimeError('select an authorized local Linux Docker engine first')
    info = json.loads(docker('info', '--format', '{{json .}}').stdout)
    if info.get('OSType') != 'linux':
        raise RuntimeError('Linux Docker engine required')
    with tempfile.TemporaryDirectory(prefix='vpnmodo-gp-') as directory:
        root = Path(directory)
        os.chmod(root, 0o700)
        (root / 'mode').write_text('valid')
        lab = Lab(root)
        try:
            lab.gateway()
            lab.probe(lab.ip, 8443, network=lab.net)
            lab.probe(lab.ip, 4501, network=lab.net, udp=True)
            # Count plaintext private-service arrivals at the gateway's outer
            # interface. A reachable unfiltered control validates the observer.
            docker('exec', lab.peer, 'iptables', '-N', 'GPTEST')
            docker('exec', lab.peer, 'iptables', '-A', 'GPTEST', '-j', 'RETURN')
            docker('exec', lab.peer, 'iptables', '-I', 'INPUT', '-i', 'eth0', '-d', '198.51.100.10', '-p', 'tcp', '--dport', '8443', '-j', 'GPTEST')
            control = "import os,socket,subprocess,sys;subprocess.run(['ip','route','add','198.51.100.0/24','via',sys.argv[1],'dev','eth0'],check=True);os.setgid(65534);os.setuid(65534);s=socket.socket();s.settimeout(1);s.connect(('198.51.100.10',8443));s.sendall(b'synthetic-probe');assert s.recv(64)==b'synthetic-probe'"
            docker('run', '--rm', '--network', lab.net, '--cap-add', 'NET_ADMIN', '--log-driver', 'none', '--entrypoint', 'python3', IMAGE, '-c', control, lab.ip)
            if lab.plaintext_count() == 0:
                raise RuntimeError('plaintext observer positive control failed')
            lab.start()
            wait(lambda: docker('exec', lab.client, '/usr/local/bin/vpn-tunnelcheck', required=False).returncode == 0)
            docker('exec', lab.client, '/usr/local/bin/vpn-servicecheck')
            lab.probe('198.51.100.10', 8443)
            lab.probe(lab.ip, 8443, blocked=True)
            lab.probe(lab.ip, 4501, blocked=True, udp=True)
            # Peer HTTPS is reserved for root-owned OpenConnect sockets.
            code = 'import socket,sys;s=socket.socket();s.settimeout(1);s.connect((sys.argv[1],443))'
            if docker('exec', '--user', '65534:65534', lab.client, 'python3', '-c', code, lab.ip, required=False).returncode == 0:
                raise RuntimeError('unprivileged HTTPS bypass')
            if docker('exec', '--user', '65534:65534', lab.client, 'nc', '-z', '-w', '1', '127.0.0.11', '53', required=False).returncode == 0:
                raise RuntimeError('Docker DNS bypass')
            if docker('exec', '--user', '65534:65534', lab.client, 'nc', '-6', '-z', '-w', '1', '2001:db8::1', '8443', required=False).returncode == 0:
                raise RuntimeError('IPv6 bypass')
            flow_code = """import socket,time
from pathlib import Path
s=socket.socket();s.settimeout(1);s.connect(('198.51.100.10',8443))
try:
 while True:
  s.sendall(b'synthetic-flow');assert s.recv(64)==b'synthetic-flow'
  Path('/tmp/flow-ready').touch();time.sleep(.05)
except (OSError,AssertionError):
 Path('/tmp/flow-blocked').touch();time.sleep(30)
"""
            flow = lab.create('flow', '--network', 'container:' + lab.client, '--read-only', '--tmpfs', '/tmp', '--user', '65534:65534', '--cap-drop', 'ALL', '--security-opt', 'no-new-privileges:true', '--entrypoint', 'python3', IMAGE, '-c', flow_code)
            docker('start', flow)
            wait(lambda: docker('exec', flow, 'test', '-f', '/tmp/flow-ready', required=False).returncode == 0)
            before_loss = lab.plaintext_count()
            # Restore an outer default after losing TUN: the firewall must block
            # both the established workload flow and new plaintext attempts.
            loss = "import json,subprocess,sys;gateway=json.loads(subprocess.check_output(['ip','-j','-4','route','get',sys.argv[1]]))[0]['gateway'];subprocess.run(['ip','link','del','vpn0'],check=True);subprocess.run(['ip','route','replace','default','via',gateway,'dev','eth0'],check=True);subprocess.run(['ip','route','replace','198.51.100.0/24','via',sys.argv[1],'dev','eth0'],check=True)"
            docker('run', '--rm', '--network', 'container:' + lab.client, '--cap-add', 'NET_ADMIN', '--log-driver', 'none', '--entrypoint', 'python3', IMAGE, '-c', loss, lab.ip)
            wait(lambda: docker('exec', flow, 'test', '-f', '/tmp/flow-blocked', required=False).returncode == 0)
            lab.probe(lab.ip, 8443, network='container:' + flow, blocked=True)
            lab.probe('198.51.100.10', 8443, network='container:' + flow, blocked=True)
            if lab.plaintext_count() != before_loss:
                raise RuntimeError('plaintext packet escaped after TUN loss')
            docker('rm', '-f', flow)
            lab.created.remove(flow)
            wait(lambda: not json.loads(docker('inspect', lab.client).stdout)[0]['State']['Running'])
            print('PASS: real OpenConnect login, HTTPS tunnel, private TCP/DNS, peer/Docker DNS/IPv6 filtering and active-flow blocking after TUN loss')
            for mode, kwargs in [('valid', {'password': 'synthetic-wrong'}), ('saml', {}), ('hip', {}), ('valid', {'trust': 'other.crt'}), ('valid', {'host': 'wrong.example.test'})]:
                lab.mode(mode)
                before = lab.stats()['login']
                lab.start(**kwargs)
                wait(lab.stopped)
                time.sleep(.5)
                if lab.stats()['login'] - before > 1:
                    raise RuntimeError('login repeated without controller authorization')
            print('PASS: rejected credentials, SAML, required HIP, untrusted CA and hostname mismatch stop without login loops')
            lab.mode('valid')
            # An explicit opt-in accepts an untrusted certificate and name/IP
            # mismatch without even mounting a CA bundle.
            for host in (lab.ip, 'wrong.example.test'):
                lab.start(insecure=True, host=host)
                wait(lambda: docker('exec', lab.client, '/usr/local/bin/vpn-tunnelcheck', required=False).returncode == 0)
                docker('exec', lab.client, '/usr/local/bin/vpn-servicecheck')
                lab.probe('198.51.100.10', 8443)
            print('PASS: explicit insecure mode connects by IP or mismatched hostname without a CA bundle')
            lab.start(health=False, mtu=1300)
            wait(lambda: docker('exec', lab.client, '/usr/local/bin/vpn-tunnelcheck', required=False).returncode == 0)
            docker('exec', lab.client, '/usr/local/bin/vpn-servicecheck')
            link = json.loads(docker('exec', lab.client, 'ip', '-j', 'link', 'show', 'dev', 'vpn0').stdout)
            routes = docker('exec', lab.client, 'ip', '-o', '-4', 'route', 'show', 'dev', 'vpn0').stdout.decode()
            effective = link[0]['mtu']
            if not 576 <= effective <= 1300 or f'mtu {effective}' not in routes or f'advmss {effective - 40}' not in routes:
                raise RuntimeError('profile-wide MTU/MSS not applied to TUN')
            lab.probe('198.51.100.10', 8443)
            print('PASS: optional private health endpoint with configured tunnel MTU/MSS and usable private TCP')
            docker('stop', '--time', '2', lab.client)
            if json.loads(docker('inspect', lab.client).stdout)[0]['State']['ExitCode'] != 0:
                raise RuntimeError('unclean graceful stop')
            print('PASS: reconnect after explicit restart and bounded graceful stop')
        finally:
            lab.close()


if __name__ == '__main__':
    try:
        main()
    except (RuntimeError, OSError, ValueError, KeyError, subprocess.SubprocessError) as error:
        # Fixed exception categories only; command output can contain secrets.
        reason = str(error) if isinstance(error, RuntimeError) else type(error).__name__
        print('GlobalProtect acceptance failed: ' + reason)
        raise SystemExit(1)
