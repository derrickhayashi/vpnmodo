"""Synthetic backend output only; these checks never connect to a gateway."""
import os
from pathlib import Path
import subprocess
import tempfile
import unittest

ROOT = Path(__file__).resolve().parents[1]


class DiagnosticLogging(unittest.TestCase):
    def test_l2tp_filter_emits_only_allowlisted_codes(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            writer = root / 'vpn-diagnostic'
            writer.write_text('#!/bin/sh\nprintf "%s\\n" "$1"\n')
            writer.chmod(0o700)
            lines = '\n'.join([
                'secret password synthetic-password ignored',
                'peer not responding, trying again',
                'received DELETE for ESP CHILD_SA private-name',
                'rekeying IKE_SA private-name failed',
                'authentication of private-user failed',
                '2026-10-20T00:00:00Z VPN is ready; shared network traffic now uses ppp0',
                '2026-10-20T00:00:00Z ppp0 is no longer up; exiting for container restart',
                '2026-10-20T00:00:00Z A VPN support process exited; restarting the container',
                '2026-10-20T00:00:00Z VPN stopping', '',
            ])
            result = subprocess.run(['sh', str(ROOT / 'containers/l2tp-ipsec/scripts/vpn-diagnostic-filter')],
                                    input=lines, text=True, capture_output=True, check=True,
                                    env=dict(os.environ, PATH=str(root) + ':' + os.environ['PATH']))
            self.assertEqual(result.stdout.splitlines(), ['dpd_timeout', 'peer_delete', 'rekey_failed',
                             'authentication_failed', 'established', 'ppp_down', 'support_process_exit', 'stopping'])
            self.assertEqual(result.stderr, '')

    def test_writers_reject_arbitrary_input_and_preserve_bounded_private_slots(self):
        for backend, relative in [('l2tp-ipsec', 'scripts'), ('ipsec-xauth', 'managed')]:
            with self.subTest(backend=backend), tempfile.TemporaryDirectory() as directory:
                root = Path(directory)
                # Mock only Linux UUID and flock commands on macOS. Production
                # lock interoperability is covered by Go's protected spool reads.
                binary = root / 'bin'
                binary.mkdir()
                (binary / 'cat').write_text('#!/bin/sh\nif [ "$1" = /proc/sys/kernel/random/uuid ]; then printf "01234567-0123-0123-0123-0123456789ab\\n"; else exec /bin/cat "$@"; fi\n')
                (binary / 'flock').write_text('#!/bin/sh\nexit 0\n')
                for file in binary.iterdir():
                    file.chmod(0o700)
                for name in ('events', 'previous', 'lock'):
                    (root / name).write_text('')
                    (root / name).chmod(0o600)
                (root / 'events').write_text('x' * 65001)
                env = dict(os.environ, PATH=str(binary) + ':' + os.environ['PATH'], VPN_DIAGNOSTIC_DIR=str(root))
                source = ROOT / 'containers' / backend / relative / 'vpn-diagnostic'
                result = subprocess.run(['sh', str(source), 'peer_delete'], capture_output=True, env=env)
                self.assertEqual(result.returncode, 0)
                self.assertEqual(result.stdout + result.stderr, b'')
                self.assertEqual((root / 'previous').stat().st_size, 65001)
                self.assertLess((root / 'events').stat().st_size, 65536)
                self.assertTrue((root / 'events').read_text().endswith('peer_delete\n'))
                self.assertEqual((root / 'events').stat().st_mode & 0o777, 0o600)
                before = (root / 'events').read_bytes()
                result = subprocess.run(['sh', str(source), 'synthetic-secret'], capture_output=True, env=env)
                self.assertNotEqual(result.returncode, 0)
                self.assertEqual(before, (root / 'events').read_bytes())
