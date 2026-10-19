"""Replay fixed prompt outcomes with synthetic credentials and no network."""
import os
from pathlib import Path
import shutil
import subprocess
import tempfile
import time
import unittest


@unittest.skipUnless(shutil.which('expect'), 'native Expect required')
class XAuthReasons(unittest.TestCase):
    def replay(self, outcome, expected):
        with tempfile.TemporaryDirectory(prefix='vpnmodo-auth-') as directory:
            root = Path(directory)
            (root / 'run/vpn').mkdir(parents=True)
            (root / 'secrets').mkdir()
            for key in ('psk', 'password'):
                (root / 'secrets' / ('vpn_' + key)).write_text('synthetic-' + key)
            binary = root / 'bin'
            binary.mkdir()
            (binary / 'ip').write_text('#!/bin/sh\nprintf "192.0.2.1 src 192.0.2.2\\n"\n')
            client = '''#!/bin/sh
printf 'Preshared Key: '; IFS= read -r value
printf 'EAP password: '; IFS= read -r value
printf 'PIN: '; IFS= read -r value
'''
            if outcome == 'repeat':
                client += "printf 'PIN: '; sleep 4\n"
            elif outcome == 'eof':
                client += 'exit 1\n'
            elif outcome == 'timeout':
                client += 'sleep 4\n'
            elif outcome == 'established':
                client += "printf 'CHILD_SA example established with SPIs synthetic\\n'; sleep 4\n"
            (binary / 'charon-cmd').write_text(client)
            for file in binary.iterdir():
                file.chmod(0o700)
            source = Path(__file__).resolve().parents[1] / 'containers/ipsec-xauth/managed/vpn-xauth-expect'
            script = source.read_text().replace('/run/vpn', str(root / 'run/vpn')).replace('/run/secrets', str(root / 'secrets'))
            if outcome == 'timeout':
                # Shorten only the post-submission deadline. Shortening startup
                # too can expire before the synthetic MFA prompt is reached.
                before, marker, after = script.rpartition('set deadline [expr {[clock seconds] + 90}]')
                self.assertTrue(marker)
                script = before + 'set deadline [expr {[clock seconds] + 1}]' + after
            harness = root / 'harness.exp'
            harness.write_text(script)
            process = subprocess.Popen(['expect', str(harness)], stdout=subprocess.PIPE, stderr=subprocess.PIPE,
                                       env=dict(os.environ, PATH=str(binary) + ':' + os.environ['PATH'],
                                                VPN_SERVER='192.0.2.1', VPN_IKE_PROPOSALS='aes256-sha1-modp1024', VPN_ESP_PROPOSALS='aes256-sha1'))
            try:
                deadline = time.monotonic() + 8
                state = root / 'run/vpn/state'
                while time.monotonic() < deadline:
                    if state.exists() and state.read_text().strip() == 'mfa':
                        (root / 'run/vpn/code').write_text('123456')
                        break
                    if process.poll() is not None:
                        self.fail('synthetic client ended before MFA')
                    time.sleep(0.02)
                else:
                    self.fail('synthetic challenge missing')
                if outcome == 'established':
                    while time.monotonic() < deadline and not (root / 'run/vpn/ready').exists():
                        time.sleep(0.02)
                    self.assertTrue((root / 'run/vpn/ready').exists())
                    time.sleep(0.2)
                    self.assertIsNone(process.poll())
                    process.terminate()
                stdout, stderr = process.communicate(timeout=8)
                self.assertEqual(process.returncode, expected)
                for value in (b'123456', b'synthetic-psk', b'synthetic-password'):
                    self.assertNotIn(value, stdout + stderr)
            finally:
                if process.poll() is None:
                    process.kill()
                    process.communicate(timeout=3)

    def test_repeated_challenge_stops_without_second_code(self):
        self.replay('repeat', 22)

    def test_client_exit_after_mfa_is_distinct(self):
        self.replay('eof', 27)

    def test_post_mfa_timeout_is_distinct(self):
        self.replay('timeout', 25)

    def test_established_tunnel_survives_idle_until_explicit_shutdown(self):
        self.replay('established', 0)
