"""Two verified SSH hops and named forwarding through a real managed VPN.

Only synthetic identities in a disposable nested-Docker lab are supported.
"""
import json
import os
from pathlib import Path
import re
import signal
import socket
import time
import uuid

from smoke_controller import command


def free_port():
    with socket.socket() as s:
        s.bind(('127.0.0.1', 0))
        return s.getsockname()[1]


def last(result):
    values = [json.loads(line) for line in result.stdout.decode().splitlines()]
    return values[-1]


class ServiceLab:
    def __init__(self, cli, directory, vpn):
        self.cli, self.directory, self.vpn = cli, directory, vpn
        self.name = 'vpnmodo-service-lab-' + uuid.uuid4().hex[:12]
        self.created = False
        self.ports = [free_port(), free_port()]
        while self.ports[1] == self.ports[0]: self.ports[1] = free_port()
        self.config = directory / 'private-service-ssh'
        self.alias = 'lab-xauth' if 'xauth' in type(vpn).__module__ else 'lab-l2tp'
        self.source = directory / 'managed-services.json'

    def close(self):
        self.cli('service', 'stop', self.alias, '--json', required=False)
        if self.created:
            command(['docker', 'rm', '-f', self.name], required=False)
            self.created = False

    def fetch(self, port):
        with socket.create_connection(('127.0.0.1', port), timeout=5) as s:
            s.settimeout(5)
            s.sendall(b'GET / HTTP/1.0\r\nHost: private.example\r\n\r\n')
            data = bytearray()
            while True:
                block = s.recv(8192)
                if not block: break
                data.extend(block)
            assert b'vpnmodo-service-ok' in data, 'forward did not reach the private service'

    def start_http(self):
        command(['docker', 'exec', '-d', self.name, 'sh', '-c', 'python3 /run/service/http.py >/dev/null 2>&1 & echo $! >/run/service/http.pid; wait'])
        for _ in range(40):
            if command(['docker', 'exec', self.name, 'python3', '-c', 'import socket;s=socket.create_connection(("127.0.0.1",28082),timeout=1);s.close()'], required=False).returncode == 0: return
            time.sleep(.1)
        raise RuntimeError('synthetic private service did not start')

    def begin(self):
        key, server, wrong = (self.directory / name for name in ('service-client', 'service-server', 'service-wrong-client'))
        for path in (key, server, wrong):
            command(['ssh-keygen', '-q', '-t', 'ed25519', '-N', '', '-f', str(path)])
        command(['docker', 'run', '-d', '--name', self.name, '--network', 'container:' + self.vpn.gateway, '--log-driver', 'none', 'vpnmodo-service-lab:acceptance'])
        self.created = True
        known = self.directory / 'service-known-hosts'
        known.write_text('[198.51.100.1]:2222 ' + server.with_suffix('.pub').read_text())
        auth = self.directory / 'service-authorized'; auth.write_text(key.with_suffix('.pub').read_text()); auth.chmod(0o600)
        sshd = self.directory / 'service-sshd'
        sshd.write_text('''Port 2222
ListenAddress 0.0.0.0
HostKey /run/service/host-key
PidFile /run/service/sshd.pid
AuthorizedKeysFile /run/service/authorized
PasswordAuthentication no
KbdInteractiveAuthentication no
PermitRootLogin prohibit-password
StrictModes yes
UsePAM no
AllowAgentForwarding no
AllowTcpForwarding local
LogLevel ERROR
''')
        http = self.directory / 'service-http.py'
        http.write_text('''from http.server import HTTPServer,BaseHTTPRequestHandler
class H(BaseHTTPRequestHandler):
 def do_GET(self):
  self.send_response(200);self.end_headers();self.wfile.write(b'vpnmodo-service-ok\\n')
 def log_message(self,*a):pass
HTTPServer(('127.0.0.1',28082),H).serve_forever()
''')
        for src,dst in ((server,'host-key'),(auth,'authorized'),(sshd,'sshd_config'),(http,'http.py')):
            command(['docker','cp',str(src),self.name+':/run/service/'+dst])
        command(['docker','exec',self.name,'chmod','0600','/run/service/host-key','/run/service/authorized'])
        command(['docker','exec','-d',self.name,'/usr/sbin/sshd','-D','-e','-f','/run/service/sshd_config'])
        self.start_http()
        self.config.write_text(f'''Host private-jump
 HostName 198.51.100.1
 Port 2222
 User root
 IdentityFile {key}
 IdentitiesOnly yes
 UserKnownHostsFile {known}
 GlobalKnownHostsFile /dev/null
 # Legacy proxy/forwards must not be used by managed service sessions.
 ProxyCommand false
 LocalForward 127.0.0.1:1 127.0.0.1:1
''')
        definition={'schema_version':1,'services':[dict(name=name,ssh_target='private-jump',ssh_config=str(self.config),local_port=port,remote_host='127.0.0.1',remote_port=28082) for name,port in zip(('dashboard','metrics'),self.ports)]}
        self.source.write_text(json.dumps(definition)); self.source.chmod(0o600)
        self.cli('service','configure',self.alias,'--file',str(self.source),'--json')
        ready=last(self.cli('up',self.alias,'--json'))
        assert ready['ready'] and ready['vpn_ready'] and len(ready['services'])==2
        for port in self.ports: self.fetch(port)
        # Diagnose each hop independently while existing data streams stay live.
        vm_config=self.directory/'ssh_config'
        vm_original=vm_config.read_text()
        try:
            vm_config.write_text(vm_original.replace(str(self.directory/'known_hosts'),str(self.directory/'unknown-vm-hosts')))
            failed=self.cli('doctor',self.alias,'--json',required=False)
            report=last(failed)
            assert failed.returncode and report['error']=='vm_ssh_unavailable'
            assert all(item['code']=='ready' for item in report['services'])
            self.fetch(self.ports[0])
        finally: vm_config.write_text(vm_original)
        command(['docker','stop','vpnmodo-controller'])
        try:
            self.fetch(self.ports[0])
            failed=self.cli('doctor',self.alias,'--json',required=False)
            assert failed.returncode and last(failed)['error']=='controller_unavailable'
        finally: command(['docker','start','vpnmodo-controller'])
        for _ in range(40):
            if self.cli('doctor',self.alias,'--json',required=False).returncode==0:break
            time.sleep(.2)
        else:raise RuntimeError('controller inspection did not recover after restart')
        # No accidental listener on the destination's inherited LocalForward.
        assert b'127.0.0.1:1 ' not in command(['ss','-lnt']).stdout
        owned=command(['ss','-lnt']).stdout
        for port in self.ports:
            assert f'127.0.0.1:{port}'.encode() in owned
            assert f'0.0.0.0:{port}'.encode() not in owned
        refused=self.cli('service','configure',self.alias,'--file',str(self.source),'--json',required=False)
        assert refused.returncode and last(refused)['error']=='service_configuration_unavailable_stop_services_first'
        self.cli('service','stop',self.alias,'--service','metrics','--json')
        self.fetch(self.ports[0])
        with socket.socket() as occupied:
            occupied.bind(('127.0.0.1',self.ports[1]));occupied.listen()
            partial=self.cli('up',self.alias,'--json',required=False)
            report=last(partial)
            assert partial.returncode and report['outcome']=='partial' and report['vpn_ready'] and not report['ready']
            assert any(s['code']=='local_port_conflict' for s in report['services'])
            self.fetch(self.ports[0])
        self.cli('service','start',self.alias,'--service','metrics','--json')
        # An unavailable endpoint behind destination SSH cannot become "ready"
        # merely because the local forwarding listener and VPN still work.
        command(['docker','exec',self.name,'sh','-c','kill "$(cat /run/service/http.pid)"'])
        unavailable=self.cli('doctor',self.alias,'--json',required=False)
        report=last(unavailable)
        assert unavailable.returncode and report['vpn_ready'] and not report['ready']
        assert all(s['code']=='remote_service_unavailable' for s in report['services'])
        self.start_http()
        assert last(self.cli('doctor',self.alias,'--json'))['ready']
        # Unknown destination host keys and wrong destination credentials fail
        # even though VM SSH and the managed tunnel are healthy.
        self.cli('service','stop',self.alias,'--json')
        original=self.config.read_text()
        for modified in (original.replace(str(known),str(self.directory/'unknown-service-hosts')),original.replace(str(key),str(wrong))):
            self.config.write_text(modified)
            failure=self.cli('service','start',self.alias,'--json',required=False)
            report=last(failure)
            assert failure.returncode and report['vpn_ready'] and not report['ready']
            assert all(s['code']=='destination_ssh_unavailable' for s in report['services'])
            for port in self.ports:
                with socket.socket() as probe:
                    probe.setsockopt(socket.SOL_SOCKET,socket.SO_REUSEADDR,1)
                    probe.bind(('127.0.0.1',port));probe.listen()
        self.config.write_text(original)
        assert last(self.cli('up',self.alias,'--json'))['ready']
        # Kill only an owned test master, using OpenSSH's reported PID. Product
        # teardown never signals PIDs, so PID reuse cannot kill another process.
        registry=self.directory/'registry'/'hosts.json'
        states=json.loads(Path(str(registry)+'.access/'+self.alias+'.state.json').read_text())
        first=states[0]
        control=command(['ssh','-F','/dev/null','-S',first['directory']+'/s','-O','check','private-jump'])
        match=re.search(rb'pid=(\d+)',control.stderr);assert match
        os.kill(int(match[1]),signal.SIGKILL)
        time.sleep(.2)
        assert last(self.cli('service','start',self.alias,'--service',first['service']['name'],'--json'))['ready']
        for port in self.ports:self.fetch(port)
        print('PASS: managed two-hop SSH forwards, loopback ownership, partial conflicts, endpoint outage, strict destination keys/auth and master crash recovery',flush=True)
        return self

    def after_restart(self):
        # Existing forwarding is outside the controller container's lifecycle.
        for port in self.ports:self.fetch(port)
        assert last(self.cli('status',self.alias,'--json'))['ready']
        self.cli('down',self.alias,'--json')
        for port in self.ports:
            with socket.socket() as probe:
                probe.setsockopt(socket.SOL_SOCKET,socket.SO_REUSEADDR,1)
                probe.bind(('127.0.0.1',port));probe.listen()
        report=last(self.cli('doctor',self.alias,'--json',required=False))
        assert not report['vpn_ready'] and not report['ready']
        assert not next(c['ok'] for c in report['checks'] if c['layer']=='vpn')
        self.close()
        print('PASS: forwards survive controller replacement; composed down removes only owned listeners and disconnects the VPN',flush=True)
