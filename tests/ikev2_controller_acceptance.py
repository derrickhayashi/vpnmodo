#!/usr/bin/env python3
"""Production-controller IKEv2 acceptance via its JSON-RPC bridge.

Run on the same explicitly authorized isolated Docker engine as the backend
suite. This uses a unique controller/profile, never the installed controller.
SSH transport is covered by the existing controller suite separately.
"""
import argparse
import json
import os
from pathlib import Path
import tempfile
import select
import subprocess
import time
import uuid

from ikev2_acceptance import BackendLab, CLIENT, docker, wait


class ControllerLab(BackendLab):
    def __init__(self, directory, image):
        super().__init__(directory)
        self.controller_image = image
        self.controller = self.prefix + '-controller'
        self.profile = 'ikev2-' + uuid.uuid4().hex[:12]
        self.project = 'vpnmodo-' + self.profile
        self.managed_network = self.project + '_default'
        self.volume = self.prefix + '-state'
        docker('volume', 'create', '--label', 'io.vpnmodo.testlab=ikev2', self.volume)
        mountpoint = json.loads(docker('volume', 'inspect', self.volume).stdout)[0]['Mountpoint']
        self.volume_root = Path(mountpoint)
        self.state = self.volume_root / 'state'
        # Native Linux storage avoids Docker Desktop's per-container ownership
        # projection for macOS shared files. Backends bind from this same path.
        docker('run', '--rm', '--user', '0', '--network', 'none', '--mount', 'type=volume,src=' + self.volume + ',dst=/state', '--entrypoint', 'sh', image, '-c', 'chown "$1:$2" /state; chmod 700 /state', 'sh', str(os.getuid()), str(os.getgid()))
        self.socket = '/run/vpnmodo/controller.sock'
        self.profile_created = False
        self.network_created = False

    def launch(self):
        uid, gid = str(os.getuid()), str(os.getgid())
        docker('run', '-d', '--name', self.controller, '--label', 'io.vpnmodo.testlab=ikev2', '--log-driver', 'none', '--network', 'none', '--user', uid + ':' + gid, '--group-add', '0', '--read-only', '--cap-drop', 'ALL', '--security-opt', 'no-new-privileges:true', '--tmpfs', '/run/vpnmodo:mode=0700,uid=' + uid + ',gid=' + gid, '--mount', 'type=volume,src=' + self.volume + ',dst=' + str(self.volume_root), '--mount', 'type=bind,src=/var/run/docker.sock,dst=/var/run/docker.sock', '-e', 'VPNMODO_STATE_DIR=' + str(self.state), '-e', 'VPNMODO_IKEV2_IMAGE=' + CLIENT, '-e', 'VPNMODO_APPLICATION_IMAGE=alpine:3.22', self.controller_image, 'serve', '--socket', self.socket)
        if self.controller not in self.created:
            self.created.append(self.controller)
        wait(lambda: docker('exec', self.controller, '/usr/local/bin/vpnmodo-controller', 'health', '--socket', self.socket, required=False).returncode == 0, 30)

    def rpc(self, method, params):
        messages = [{'jsonrpc': '2.0', 'id': 1, 'method': 'system.hello', 'params': {'api': {'major': 1, 'minor': 0}}}, {'jsonrpc': '2.0', 'id': 2, 'method': method, 'params': params}]
        args = ['docker', 'exec', '-i', self.controller, '/usr/local/bin/vpnmodo-controller', 'bridge', '--socket', self.socket]
        proc = subprocess.Popen(args, stdin=subprocess.PIPE, stdout=subprocess.PIPE, stderr=subprocess.DEVNULL)
        try:
            # Negotiate before sending the next RPC; the server dispatches
            # independent requests concurrently.
            for message in messages:
                proc.stdin.write((json.dumps(message) + '\n').encode())
                proc.stdin.flush()
                if not select.select([proc.stdout], [], [], 20)[0]:
                    raise RuntimeError('controller bridge response deadline')
                reply = json.loads(proc.stdout.readline())
                assert reply.get('id') == message['id'], 'bridge response ID mismatch'
                if 'error' in reply:
                    raise RuntimeError('controller RPC failed: ' + message['method'])
        finally:
            proc.stdin.close()
            try:
                proc.wait(timeout=5)
            except subprocess.TimeoutExpired:
                proc.kill(); proc.wait()
            proc.stdout.close()
        if 'error' in reply:
            raise RuntimeError('controller RPC failed: ' + method)
        encoded = json.dumps(reply).encode()
        assert b'synthetic-password' not in encoded and b'BEGIN CERTIFICATE' not in encoded
        return reply['result']

    def operation(self, method, terminal='succeeded'):
        op = self.rpc(method, {'profile': self.profile})
        def check():
            record = self.rpc('operation.status', {'operation_id': op['id']})
            assert record['state'] != 'awaiting_mfa', 'EAP entered MFA lifecycle'
            if record['state'] in ('failed', 'cancelled', 'succeeded'):
                if record['state'] != terminal:
                    logs = self.rpc('connection.logs', {'profile': self.profile, 'limit': 10})
                    reasons = ','.join(e.get('reason_code', e.get('reason', '')) for e in logs.get('events', []))
                    reasons = ','.join(e.get('reason', '') for e in logs.get('events', []))
                    raise RuntimeError('controller ' + method + ' failed: code ' + str(record.get('failure', {}).get('code')) + '; reasons ' + reasons)
                return True
            return False
        wait(check)
        return op

    def ids(self):
        ids = docker('ps', '-aq', '--filter', 'label=com.docker.compose.project=' + self.project).stdout.decode().split()
        return {c['Config']['Labels']['com.docker.compose.service']: c for c in (json.loads(docker('inspect', cid).stdout)[0] for cid in ids)}

    def setup_profile(self):
        self.start_gateway()
        args = ['network', 'create']
        for key, value in {'io.vpnmodo.managed-by': 'vpnmodo', 'io.vpnmodo.profile': self.profile, 'io.vpnmodo.backend': 'ipsec-ikev2', 'com.docker.compose.project': self.project, 'com.docker.compose.network': 'default'}.items():
            args += ['--label', key + '=' + value]
        docker(*args, self.managed_network)
        self.network_created = True
        docker('network', 'connect', self.managed_network, self.gateway)
        peer = json.loads(docker('inspect', self.gateway).stdout)[0]['NetworkSettings']['Networks'][self.managed_network]['IPAddress']
        p = {'schema_version': 1, 'name': self.profile, 'backend': {'type': 'ipsec-ikev2', 'config': {'server': peer, 'server_identity': 'vpn.example.test', 'client_identity': 'client@example.test', 'eap_identity': 'synthetic-user', 'dns': ['198.51.100.1'], 'ike_proposals': ['aes128-sha256-modp2048'], 'esp_proposals': ['aes128-sha256']}}, 'health': {'host': 'private.example', 'port': 8080}}
        self.rpc('profile.create', {'profile': p, 'credentials': {'username': 'synthetic-user', 'password': 'synthetic-password', 'ca_cert': (self.root / 'ca_cert').read_text()}})
        self.profile_created = True

    def close(self):
        if self.profile_created:
            try:
                self.operation('connection.disconnect')
                self.rpc('profile.delete', {'profile': self.profile, 'expected_revision': 1})
            except Exception:
                pass
        for c in self.ids().values():
            docker('rm', '-f', c['Id'], required=False)
        super().close()
        if self.network_created:
            docker('network', 'rm', self.managed_network, required=False)
        docker('volume', 'rm', self.volume, required=False)


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument('--controller-image', default='vpnmodo-controller:ikev2-acceptance')
    options = parser.parse_args()
    if os.environ.get('VPNMODO_ISOLATED_TEST_HOST') != '1':
        raise RuntimeError('requires explicitly authorized isolated Docker engine')
    docker('pull', 'alpine:3.22')
    with tempfile.TemporaryDirectory(prefix='vpnmodo-ikev2-controller-') as directory:
        # Docker Desktop bind mounts need the canonical host path.
        lab = ControllerLab(str(Path(directory).resolve()), options.controller_image)
        try:
            lab.launch()
            backends = lab.rpc('backend.list', {})
            desc = next(d for d in backends if d['type'] == 'ipsec-ikev2')
            assert not desc['capabilities']['mfa'] and desc['capabilities']['automatic_recovery']
            lab.setup_profile()
            lab.operation('connection.ensure')
            ids = lab.ids()
            assert ids['application']['Config']['User'] == '65534:65534'
            assert ids['application']['HostConfig']['NetworkMode'] == 'container:' + ids['vpn']['Id']
            lab.client = ids['vpn']['Id']
            lab.traffic()
            lab.client = None  # controller owns namespace teardown
            print('PASS: production controller import, EAP lifecycle, verification and application attachment', flush=True)
            before = {k: c['Id'] for k, c in ids.items()}
            docker('stop', '--time', '8', lab.controller)
            docker('rm', lab.controller)
            lab.launch()
            lab.operation('connection.ensure')
            assert before == {k: c['Id'] for k, c in lab.ids().items()}, 'controller restart replaced an authenticated namespace'
            print('PASS: production controller recreation preserves authenticated IKEv2/application namespace', flush=True)
            lab.operation('connection.disconnect')
            assert not lab.ids()
            assert not docker('exec', lab.controller, 'find', str(lab.state / 'deployments' / lab.profile / 'credentials'), '-mindepth', '1', '-print', '-quit').stdout.strip()
            lab.operation('connection.ensure')
            assert lab.ids()['vpn']['Id'] != before['vpn']
            lab.operation('connection.disconnect')
            print('PASS: explicit disconnect disposes credentials; unattended ensure starts fresh', flush=True)
        finally:
            lab.close()


if __name__ == '__main__':
    try:
        main()
    except (RuntimeError, AssertionError) as error:
        raise SystemExit('FAIL: ' + str(error))
    except Exception:
        raise SystemExit('FAIL: IKEv2 controller acceptance')
