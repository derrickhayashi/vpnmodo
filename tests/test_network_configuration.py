"""Execute startup configuration with synthetic inputs and no network access."""
import importlib.machinery
import importlib.util
import os
from pathlib import Path
import subprocess
import tempfile
import unittest
from unittest.mock import patch

ROOT = Path(__file__).resolve().parents[1]
loader = importlib.machinery.SourceFileLoader('network_ikev2_runtime', str(ROOT / 'containers/ipsec-ikev2/scripts/vpn-managed-ikev2'))
spec = importlib.util.spec_from_loader(loader.name, loader)
runtime = importlib.util.module_from_spec(spec)
loader.exec_module(runtime)


class NetworkConfiguration(unittest.TestCase):
    def test_ikev2_network_configuration_validation(self):
        with patch.dict(os.environ, {}, clear=True):
            self.assertEqual(runtime.network_configuration(), '')
        for mtu in ('576', '1300', '1500'):
            with self.subTest(mtu=mtu), patch.dict(os.environ, {'VPN_MTU': mtu}, clear=True):
                config = runtime.network_configuration()
                self.assertIn('mtu = ' + mtu + '\n', config)
                self.assertIn('mss = ' + str(int(mtu) - 40) + '\n', config)
        for mtu in ('', '0', '575', '1501', '-1', '01300', '1300.5', '1300\n', '1300; true'):
            with self.subTest(mtu=mtu), patch.dict(os.environ, {'VPN_MTU': mtu}, clear=True):
                with self.assertRaises(ValueError):
                    runtime.network_configuration()

    def xauth_startup(self, mtu):
        with tempfile.TemporaryDirectory(prefix='vpnmodo-network-') as directory:
            root = Path(directory)
            binary = root / 'bin'
            binary.mkdir()
            # The firewall/client are replaced solely to exercise rendering
            # without authentication, privileges, secrets or network changes.
            (binary / 'vpn-enforce').write_text('#!/bin/sh\nexit 0\n')
            (binary / 'vpn-enforce').chmod(0o700)
            source = (ROOT / 'containers/ipsec-xauth/managed/vpn-managed-xauth').read_text()
            source = source.replace('/run/vpn', str(root / 'run'))
            source = source.replace('exec /usr/local/bin/vpn-xauth-expect >/dev/null 2>&1', 'exit 0')
            script = root / 'startup'
            script.write_text(source)
            env = {'PATH': str(binary) + os.pathsep + os.environ['PATH']}
            if mtu is not None:
                env['VPN_MTU'] = mtu
            result = subprocess.run(['sh', str(script)], env=env, capture_output=True, timeout=5)
            config = root / 'run/strongswan.conf'
            return result.returncode, config.read_text() if config.exists() else ''

    def test_xauth_startup_renders_native_route_settings(self):
        for mtu in ('576', '1300', '1500'):
            with self.subTest(mtu=mtu):
                code, config = self.xauth_startup(mtu)
                self.assertEqual(code, 0)
                self.assertIn('mtu = ' + mtu + '\n', config)
                self.assertIn('mss = ' + str(int(mtu) - 40) + '\n', config)
                self.assertIn('charon-cmd {', config)
        code, config = self.xauth_startup(None)
        self.assertEqual(code, 0)
        self.assertNotIn('mtu =', config)
        for mtu in ('0', '575', '1501', '-1', '01300', '1300.5', '1300; true'):
            with self.subTest(mtu=mtu):
                code, _ = self.xauth_startup(mtu)
                self.assertNotEqual(code, 0)
