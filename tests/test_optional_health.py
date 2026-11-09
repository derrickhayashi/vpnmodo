"""All backend service helpers retain tunnel checks when private health is omitted."""
import os
from pathlib import Path
import subprocess
import tempfile
import unittest

ROOT = Path(__file__).resolve().parents[1]
SCRIPTS = [ROOT / path for path in (
    'containers/l2tp-ipsec/scripts/vpn-servicecheck',
    'containers/ipsec-xauth/managed/vpn-servicecheck',
    'containers/ipsec-ikev2/scripts/vpn-servicecheck',
    'containers/openconnect-globalprotect/scripts/vpn-servicecheck',
)]


class OptionalHealthTests(unittest.TestCase):
    def test_optional_probe_never_skips_tunnel_or_ignores_configured_failure(self):
        for script in SCRIPTS:
            with self.subTest(backend=script.parent.parent.name), tempfile.TemporaryDirectory() as directory:
                root = Path(directory)
                for name, contents in {
                    'vpn-tunnelcheck': '#!/bin/sh\nprintf tunnel >> "$CALLS"\nexit "${TUNNEL_EXIT:-0}"\n',
                    'nc': '#!/bin/sh\nprintf probe >> "$CALLS"\nexit "${PROBE_EXIT:-0}"\n',
                    'timeout': '#!/bin/sh\nprintf probe >> "$CALLS"\nexit "${PROBE_EXIT:-0}"\n',
                }.items():
                    path = root / name
                    path.write_text(contents)
                    path.chmod(0o700)
                for extra, expected, calls in [
                    ({}, 0, 'tunnel'),
                    ({'TUNNEL_EXIT': '1'}, 1, 'tunnel'),
                    ({'VPN_HEALTH_PORT': '22'}, 1, 'tunnel'),
                    ({'VPN_HEALTH_HOST': '192.0.2.10'}, None, 'tunnel'),
                    ({'VPN_HEALTH_HOST': '192.0.2.10', 'VPN_HEALTH_PORT': '22'}, 0, 'tunnelprobe'),
                    ({'VPN_HEALTH_HOST': '192.0.2.10', 'VPN_HEALTH_PORT': '22', 'PROBE_EXIT': '1'}, 1, 'tunnelprobe'),
                ]:
                    (root / 'calls').write_text('')
                    result = subprocess.run(['sh', str(script)], env={'PATH': str(root) + os.pathsep + os.defpath, 'CALLS': str(root / 'calls'), **extra}, capture_output=True)
                    if expected is None:
                        self.assertNotEqual(result.returncode, 0)
                    else:
                        self.assertEqual(result.returncode, expected)
                    self.assertEqual((root / 'calls').read_text(), calls)
