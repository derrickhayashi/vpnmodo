#!/usr/bin/python3
"""Synthetic GP HTTPS peer for local acceptance, never a production VPN server.

Implements only the small gateway handshake and GPST framing needed to exercise
the real OpenConnect binary, TUN routing and kernel packet policy. No ESP keys
are advertised. Credentials, cookies and packet contents are never logged.
"""
import fcntl
import http.server
import json
import os
from pathlib import Path
import select
import socket
import socketserver
import ssl
import struct
import subprocess
import threading
import urllib.parse

ROOT = Path('/run/gp-test')
LOCK = threading.Lock()
STATS = {'login': 0, 'tunnel': 0}


def count(key):
    with LOCK:
        STATS[key] += 1
        (ROOT / 'stats').write_text(json.dumps(STATS))


def run(*args):
    subprocess.run(args, check=True, stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)


class Echo(socketserver.BaseRequestHandler):
    def handle(self):
        while data := self.request.recv(4096):
            self.request.sendall(data)


def udp_echo():
    sock = socket.socket(socket.AF_INET, socket.SOCK_DGRAM)
    sock.bind(('0.0.0.0', 4501))
    while True:
        data, address = sock.recvfrom(4096)
        sock.sendto(data, address)


class Gateway(http.server.BaseHTTPRequestHandler):
    protocol_version = 'HTTP/1.0'

    def log_message(self, *_):
        pass

    def xml(self, value, status=200):
        raw = value.encode()
        self.send_response(status)
        self.send_header('Content-Type', 'application/xml')
        self.send_header('Content-Length', str(len(raw)))
        self.end_headers()
        self.wfile.write(raw)

    def do_POST(self):
        size = int(self.headers.get('Content-Length', '0'))
        if size > 16384:
            self.send_error(413)
            return
        form = urllib.parse.parse_qs(self.rfile.read(size).decode())
        mode = (ROOT / 'mode').read_text().strip()
        path = self.path.split('?')[0]
        if path == '/ssl-vpn/prelogin.esp':
            extra = '<saml-auth-method>REDIRECT</saml-auth-method><saml-request>aHR0cHM6Ly9pZHAuZXhhbXBsZS50ZXN0Lw==</saml-request>' if mode == 'saml' else ''
            self.xml('<prelogin-response><status>Success</status><authentication-message>Synthetic login</authentication-message><username-label>Username</username-label><password-label>Password</password-label>' + extra + '</prelogin-response>')
        elif path == '/ssl-vpn/login.esp':
            count('login')
            if mode == 'reject' or form.get('user') != ['synthetic-user'] or form.get('passwd') != ['synthetic-password']:
                self.xml('<response status="error"><msg>Authentication failed</msg></response>', 403)
                return
            args = ['', 'synthetic-cookie', 'synthetic-persistent', 'synthetic-portal', 'synthetic-user', 'local', 'vsys1', 'synthetic-domain', '', '', '', '', 'tunnel', '-1', '4100', '192.0.2.2']
            self.xml('<jnlp><application-desc>' + ''.join('<argument>' + value + '</argument>' for value in args) + '</application-desc></jnlp>')
        elif path == '/ssl-vpn/getconfig.esp':
            self.xml('<response><ip-address>192.0.2.2</ip-address><netmask>255.255.255.255</netmask><mtu>0</mtu><ssl-tunnel-url>/ssl-tunnel-connect.sslvpn</ssl-tunnel-url><timeout>3600</timeout><dns><member>203.0.113.99</member></dns><access-routes><member>0.0.0.0/0</member></access-routes></response>')
        elif path == '/ssl-vpn/hipreportcheck.esp':
            self.xml('<response><hip-report-needed>' + ('yes' if mode == 'hip' else 'no') + '</hip-report-needed></response>')
        elif path == '/ssl-vpn/logout.esp':
            self.xml('<response status="success"/>')
        else:
            self.xml('<response status="error"/>', 404)

    def do_GET(self):
        if not self.path.startswith('/ssl-tunnel-connect.sslvpn?'):
            self.xml('<response status="error"/>', 404)
            return
        if (ROOT / 'mode').read_text().strip() == 'hip':
            self.xml('<response status="error"><msg>HIP required</msg></response>', 403)
            return
        count('tunnel')
        self.connection.sendall(b'START_TUNNEL')
        buffer = b''
        self.connection.settimeout(1)
        try:
            while True:
                ready, _, _ = select.select([self.connection, TUN], [], [], 0.2)
                if self.connection in ready or self.connection.pending():
                    data = self.connection.recv(65536)
                    if not data:
                        break
                    buffer += data
                    while len(buffer) >= 16:
                        magic, proto, size = struct.unpack('!IHH', buffer[:8])
                        if magic != 0x1a2b3c4d or size > 1500:
                            return
                        if len(buffer) < 16 + size:
                            break
                        packet, buffer = buffer[:16 + size], buffer[16 + size:]
                        if proto == 0:
                            self.connection.sendall(packet)
                        elif proto == 0x0800:
                            os.write(TUN, packet[16:])
                if TUN in ready:
                    data = os.read(TUN, 1500)
                    self.connection.sendall(struct.pack('!IHH', 0x1a2b3c4d, 0x0800, len(data)) + struct.pack('<II', 1, 0) + data)
        except (OSError, ssl.SSLError):
            pass


class Server(http.server.ThreadingHTTPServer):
    def handle_error(self, *_):
        # Do not render exceptions from a request containing credentials.
        pass


if __name__ == '__main__':
    TUN = os.open('/dev/net/tun', os.O_RDWR)
    fcntl.ioctl(TUN, 0x400454ca, struct.pack('16sH', b'gpserver', 0x0001 | 0x1000))
    run('ip', 'link', 'set', 'gpserver', 'up')
    run('ip', 'addr', 'add', '198.51.100.10/32', 'dev', 'lo')
    run('ip', 'addr', 'add', '198.51.100.53/32', 'dev', 'lo')
    run('ip', 'route', 'add', '192.0.2.2/32', 'dev', 'gpserver')
    run('sysctl', '-qw', 'net.ipv4.conf.all.rp_filter=0', 'net.ipv4.conf.gpserver.rp_filter=0')
    threading.Thread(target=udp_echo, daemon=True).start()
    echo = socketserver.ThreadingTCPServer(('0.0.0.0', 8443), Echo)
    threading.Thread(target=echo.serve_forever, daemon=True).start()
    subprocess.Popen(['dnsmasq', '--keep-in-foreground', '--no-resolv', '--no-hosts', '--bind-interfaces', '--listen-address=198.51.100.53', '--address=/service.example.test/198.51.100.10', '--user=root'], stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)
    server = Server(('0.0.0.0', 443), Gateway)
    context = ssl.SSLContext(ssl.PROTOCOL_TLS_SERVER)
    context.load_cert_chain(str(ROOT / 'server.crt'), str(ROOT / 'server.key'))
    server.socket = context.wrap_socket(server.socket, server_side=True)
    (ROOT / 'ready').touch()
    server.serve_forever()
