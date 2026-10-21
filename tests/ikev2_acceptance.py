#!/usr/bin/env python3
"""Synthetic real IKEv2 acceptance on an explicitly isolated Linux Docker daemon.

No credentials enter arguments/logs. Created objects have unique names and are
removed individually; existing containers, networks and VPNs are never touched.
"""
import json
import os
from pathlib import Path
import subprocess
import tempfile
import time
import uuid

CLIENT = 'vpnmodo-ikev2:dev'
GATEWAY = 'vpnmodo-ikev2-gateway:acceptance'


def docker(*args, data=None, required=True):
    result = subprocess.run(['docker', *args], input=data, capture_output=True, timeout=150)
    if required and result.returncode:
        raise RuntimeError('IKEv2 acceptance command failed: ' + args[0])
    return result


def wait(check, seconds=125):
    deadline = time.monotonic() + seconds
    while time.monotonic() < deadline:
        if check():
            return
        time.sleep(.3)
    raise RuntimeError('IKEv2 acceptance deadline exceeded')


class BackendLab:
    def __init__(self, directory):
        self.root = Path(directory)
        self.prefix = 'vpnmodo-ikev2-test-' + uuid.uuid4().hex[:12]
        self.network = self.prefix + '-net'
        self.created = []
        self.client = None
        self.gateway = None
        docker('network', 'create', '--label', 'io.vpnmodo.testlab=ikev2', self.network)

    def run(self, role, image, *args):
        name = self.prefix + '-' + role
        docker('run', '-d', '--name', name, '--label', 'io.vpnmodo.testlab=ikev2', '--log-driver', 'none', *args, image)
        self.created.append(name)
        return name

    def start_gateway(self, mode='valid'):
        if self.gateway:
            docker('rm', '-f', self.gateway)
            self.created.remove(self.gateway)
        self.gateway = self.run('gateway', GATEWAY, '--network', self.network, '--cap-add', 'NET_ADMIN', '-e', 'LAB_CERT_MODE=' + mode)
        wait(lambda: docker('exec', self.gateway, 'test', '-f', '/run/lab/ready', required=False).returncode == 0, 40)
        self.peer = json.loads(docker('inspect', self.gateway).stdout)[0]['NetworkSettings']['Networks'][self.network]['IPAddress']
        for source, target in (('ca.pem', 'ca_cert'), ('other.pem', 'other_ca')):
            (self.root / target).write_bytes(docker('exec', self.gateway, 'cat', '/run/lab/' + source).stdout)
            (self.root / target).chmod(0o600)

    def start(self, *, password='synthetic-password', identity='vpn.example.test', trust='ca_cert', ike='aes128-sha256-modp2048', esp='aes128-sha256', peer=None):
        self.stop()
        (self.root / 'username').write_text('synthetic-user')
        (self.root / 'password').write_text(password)
        (self.root / 'resolv.conf').write_text('nameserver 127.0.0.1\noptions ndots:0\n')
        for key in ('username', 'password'):
            (self.root / key).chmod(0o600)
        args = ['--network', self.network, '--cap-add', 'NET_ADMIN', '--cap-add', 'NET_RAW', '--read-only', '--security-opt', 'no-new-privileges:true', '--init', '--sysctl', 'net.ipv6.conf.all.disable_ipv6=1', '--sysctl', 'net.ipv6.conf.default.disable_ipv6=1']
        for path in ('/run:rw,nosuid,nodev,mode=0755', '/tmp:rw,nosuid,nodev,noexec,mode=1777', '/etc/swanctl:rw,nosuid,nodev,noexec,mode=0700'):
            args += ['--tmpfs', path]
        env = {'VPN_SERVER': peer or self.peer, 'VPN_SERVER_IDENTITY': identity, 'VPN_CLIENT_IDENTITY': 'client@example.test', 'VPN_EAP_IDENTITY': 'synthetic-user', 'VPN_DNS': '198.51.100.1', 'VPN_HEALTH_HOST': 'private.example', 'VPN_HEALTH_PORT': '8080', 'VPN_IKE_PROPOSALS': ike, 'VPN_ESP_PROPOSALS': esp}
        for key, value in env.items():
            args += ['-e', key + '=' + value]
        for name, source in (('username', 'username'), ('password', 'password'), ('ca_cert', trust)):
            target = '/run/secrets/vpn_' + name
            args += ['--mount', 'type=bind,src=' + str(self.root / source) + ',dst=' + target + ',readonly', '-e', 'VPN_' + name.upper() + '_FILE=' + target]
        args += ['--mount', 'type=bind,src=' + str(self.root / 'resolv.conf') + ',dst=/etc/resolv.conf,readonly']
        diagnostic = self.root / 'diagnostics'
        diagnostic.mkdir(exist_ok=True)
        for name in ('events', 'previous', 'lock'):
            (diagnostic / name).write_text('')
            (diagnostic / name).chmod(0o600)
        args += ['--mount', 'type=bind,src=' + str(diagnostic) + ',dst=/var/log/vpnmodo', '-e', 'VPN_DIAGNOSTIC_DIR=/var/log/vpnmodo']
        self.client = self.run('client', CLIENT, *args)

    def stop(self):
        if self.client:
            docker('rm', '-f', self.client, required=False)
            self.created.remove(self.client)
            self.client = None

    def ready(self):
        return docker('exec', self.client, 'vpn-tunnelcheck', required=False).returncode == 0

    def wait_ready(self):
        def check():
            if self.ready(): return True
            state = json.loads(docker('inspect', self.client).stdout)[0]['State']
            if not state['Running']:
                events = (self.root / 'diagnostics/events').read_text()
                codes = [c for c in ('authentication_failed', 'certificate_untrusted', 'certificate_expired', 'server_identity_mismatch', 'proposal_mismatch', 'client_exit', 'support_process_exit', 'handshake_timeout') if c in events]
                raise RuntimeError('backend exited before authenticated readiness: ' + ','.join(codes))
            return False
        wait(check)

    def traffic(self):
        # Same UID/capability boundary as the managed application. No secrets or
        # daemon filesystem are mounted in the workload.
        payload = b'vpnmodo-synthetic-echo\n' * 1024
        result = docker('run', '--rm', '-i', '--network', 'container:' + self.client, '--user', '65534:65534', '--cap-drop', 'ALL', '--security-opt', 'no-new-privileges:true', '--read-only', '--entrypoint', 'nc', CLIENT, '-N', '-w', '3', '198.51.100.1', '8080', data=payload)
        assert result.stdout == payload, 'application echo failed'
        docker('exec', self.client, 'vpn-servicecheck')
        assert docker('run', '--rm', '--network', 'container:' + self.client, '--user', '65534:65534', '--cap-drop', 'ALL', '--entrypoint', 'nc', CLIENT, '-z', '-w', '1', '127.0.0.11', '53', required=False).returncode != 0, 'Docker DNS fallback'

    def freeze(self, stop):
        # SIGSTOP keeps Docker's container state running, permitting separate
        # namespace observers. It stops all backend userspace, including init,
        # while helpers use their own PID namespaces and cannot resume it.
        code = "import os,signal; own=os.getpid(); sig=signal.SIGSTOP if " + repr(stop) + " else signal.SIGCONT;\nfor p in os.listdir('/proc'):\n if p.isdigit() and int(p)!=own:\n  try: os.kill(int(p),sig)\n  except ProcessLookupError: pass"
        docker('exec', self.client, '/opt/vici/bin/python3', '-c', code)

    def children(self):
        # Return only SA IDs/algorithm names, no identities or key material.
        code = "import json,vici; s=vici.Session(); print(json.dumps([{k:(v.decode() if isinstance(v,bytes) else v) for k,v in c.items() if k in ('uniqueid','encr-alg','encr-keysize','integ-alg')} for e in s.list_sas({'ike':'vpn'}) for a in e.values() for c in a.get('child-sas',{}).values()]))"
        return json.loads(docker('exec', self.client, '/opt/vici/bin/python3', '-c', code).stdout)

    def probes(self):
        docker('run', '--rm', '--network', 'container:' + self.client, '--user', '65534:65534', '--cap-drop', 'ALL', '--entrypoint', 'sh', CLIENT, '-c', 'nc -z -w 1 198.51.100.1 8080; nc -zu -w 1 198.51.100.1 53', required=False)

    def capture(self, action, positive=False):
        name = self.prefix + '-observer'
        args = ['docker', 'run', '--rm', '--name', name, '--network', 'container:' + self.client, '--cap-add', 'NET_RAW', '--entrypoint', 'tcpdump', GATEWAY, '-n', '-l', '-i', 'eth0', '-Q', 'out', 'tcp port 8080 or port 53']
        proc = subprocess.Popen(args, stdin=subprocess.DEVNULL, stdout=subprocess.PIPE, stderr=subprocess.PIPE)
        try:
            time.sleep(1)
            assert proc.poll() is None, 'packet observer did not start'
            action()
            time.sleep(1)
        finally:
            docker('rm', '-f', name, required=False)
        output, _ = proc.communicate(timeout=10)
        if positive:
            assert output.strip(), 'packet observer positive control failed'
        else:
            assert not output.strip(), 'plaintext leaked on client Ethernet'

    def failure(self, expected=None, **options):
        self.start(**options)
        wait(lambda: not json.loads(docker('inspect', self.client).stdout)[0]['State']['Running'])
        state = json.loads(docker('inspect', self.client).stdout)[0]
        assert state['State']['ExitCode'] == 2, 'permanent auth/policy failure was retriable'
        events = (self.root / 'diagnostics/events').read_text()
        assert any(code in events for code in ('authentication_failed', 'certificate_untrusted', 'certificate_expired', 'server_identity_mismatch', 'proposal_mismatch')), 'missing sanitized reason'
        if expected:
            codes = (expected,) if isinstance(expected, str) else expected
            actual = [line.split()[-1] for line in events.splitlines() if line.split() and line.split()[-1] in ('authentication_failed', 'certificate_untrusted', 'certificate_expired', 'server_identity_mismatch', 'proposal_mismatch')]
            assert any(code in actual for code in codes), 'unexpected sanitized reason: ' + ','.join(actual)
        assert 'synthetic-password' not in events and 'synthetic-user' not in events
        assert state['HostConfig']['LogConfig']['Type'] == 'none'
        assert not docker('logs', self.client, required=False).stdout

    def close(self):
        for name in reversed(self.created):
            docker('rm', '-f', name, required=False)
        docker('network', 'rm', self.network, required=False)


def main():
    if os.environ.get('VPNMODO_ISOLATED_TEST_HOST') != '1':
        raise RuntimeError('requires an explicitly isolated Linux Docker daemon')
    assert docker('info', '--format', '{{.OSType}}').stdout.strip() == b'linux'
    with tempfile.TemporaryDirectory(prefix='vpnmodo-ikev2-') as directory:
        lab = BackendLab(directory)
        try:
            lab.start_gateway()
            lab.start()
            lab.wait_ready()
            lab.capture(lab.traffic)
            children = lab.children()
            assert children and all(c.get('encr-keysize') == '128' for c in children), 'explicit ESP policy widened'
            before = {c['uniqueid'] for c in children}
            def rekey():
                docker('exec', lab.client, 'swanctl', '--rekey', '--child', 'vpn')
                wait(lambda: bool({c['uniqueid'] for c in lab.children()} - before), 30)
                lab.traffic()
            lab.capture(rekey)
            print('PASS: EAP-MSCHAPv2, explicit proposals, private TCP/DNS, CHILD_SA rekey', flush=True)
            # Pause all backend userspace before removing XFRM state/policies.
            # The observer and workload are separate processes in the same netns.
            lab.freeze(True)
            def remove_and_probe():
                docker('run', '--rm', '--network', 'container:' + lab.client, '--cap-add', 'NET_ADMIN', '--entrypoint', 'sh', CLIENT, '-c', 'ip xfrm policy flush; ip xfrm state flush')
                lab.probes()
            lab.capture(remove_and_probe)
            # Prove the identical observer and sender detect a deliberate bypass.
            def bypass():
                docker('run', '--rm', '--network', 'container:' + lab.client, '--cap-add', 'NET_ADMIN', '--entrypoint', 'iptables', CLIENT, '-P', 'OUTPUT', 'ACCEPT')
                lab.probes()
            try:
                lab.capture(bypass, positive=True)
            finally:
                docker('run', '--rm', '--network', 'container:' + lab.client, '--cap-add', 'NET_ADMIN', '--entrypoint', 'sh', '-e', 'VPN_SERVER=' + lab.peer, CLIENT, '-c', 'mkdir -p /run/vpn; vpn-enforce')
                lab.freeze(False)
            print('PASS: tunnel loss is fail closed with userspace paused; observer positive control', flush=True)
            lab.start(); lab.wait_ready(); lab.traffic()
            print('PASS: fresh unattended reconnect', flush=True)
            lab.start(ike='aes256-sha256-modp2048', esp='aes256-sha256')
            lab.wait_ready(); lab.traffic()
            assert all(c.get('encr-keysize') == '256' for c in lab.children())
            print('PASS: documented IKEv2 default proposals', flush=True)
            # The daemon's first attempt cannot authenticate to this synthetic
            # unreachable address; cancellation must promptly stop its processes.
            lab.start(peer='203.0.113.254')
            wait(lambda: docker('exec', lab.client, 'test', '-S', '/run/charon.vici', required=False).returncode == 0, 20)
            lab.capture(lab.probes)
            docker('stop', '--time', '5', lab.client)
            assert not json.loads(docker('inspect', lab.client).stdout)[0]['State']['Running']
            print('PASS: pre-authentication enforcement and bounded cancellation', flush=True)
            for expected, options in (('authentication_failed', {'password': 'synthetic-wrong'}), ('certificate_untrusted', {'trust': 'other_ca'}), (('server_identity_mismatch', 'authentication_failed', 'certificate_untrusted'), {'identity': 'wrong.example.test'}), ('proposal_mismatch', {'ike': 'aes256-sha512-modp4096'}), ('proposal_mismatch', {'esp': 'aes256-sha512'})):
                lab.failure(expected=expected, **options)
            lab.start_gateway('expired')
            lab.failure(expected=('certificate_expired', 'certificate_untrusted'))
            print('PASS: wrong password, CA, server identity, expired certificate and incompatible IKE/ESP fail safely', flush=True)
        finally:
            lab.close()


if __name__ == '__main__':
    try:
        main()
    except (RuntimeError, AssertionError) as error:
        raise SystemExit('FAIL: ' + str(error))
    except Exception:
        # No raw subprocess payload, configuration, certificate or credential.
        raise SystemExit('FAIL: IKEv2 acceptance (see the last completed check)')
