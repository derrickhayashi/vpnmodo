"""Runtime boundary tests; real OpenConnect/packet checks run separately on Linux."""
import importlib.machinery
import importlib.util
import json
import os
from pathlib import Path
import subprocess
import tempfile
import unittest
from unittest.mock import Mock, patch

ROOT = Path(__file__).resolve().parents[1]
loader = importlib.machinery.SourceFileLoader('gp_runtime', str(ROOT / 'containers/openconnect-globalprotect/scripts/vpn-managed-globalprotect'))
spec = importlib.util.spec_from_loader(loader.name, loader)
runtime = importlib.util.module_from_spec(spec)
loader.exec_module(runtime)


class GlobalProtectRuntimeTests(unittest.TestCase):
    def test_credentials_are_files_or_stdin_and_options_cannot_be_injected(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            values = {'username': 'synthetic-user', 'password': 'synthetic-password', 'ca_cert': (ROOT / 'internal/profile/testdata/ikev2-ca.txt').read_text()}
            for key, value in values.items():
                (root / key).write_text(value)
            env = {'VPN_SERVER': '203.0.113.1', 'VPN_GATEWAY_HOST': 'gateway.example.test', **{'VPN_' + k.upper() + '_FILE': str(root / k) for k in values}}
            with patch.dict(os.environ, env, clear=True), patch.object(runtime, 'RUN', root):
                self.assertEqual(runtime.configuration(), b'synthetic-password\n')
                config = (root / 'client.conf').read_text()
                self.assertNotIn('synthetic-password', config)
                for option in ('protocol=gp\n', 'usergroup=gateway\n', 'no-system-trust\n', 'non-inter\n', 'no-external-auth\n', 'no-proxy\n', 'resolve=gateway.example.test:203.0.113.1\n', 'mtu=1400\n'):
                    self.assertIn(option, config)
                for bad in ('synthetic-user\nservercert=sha256:0000', 'synthetic-user\rno-system-trust', 'bad\x00value'):
                    (root / 'username').write_text(bad)
                    with self.assertRaises(ValueError):
                        runtime.configuration()
                (root / 'username').write_text(values['username'])
                for bad in ('https://gateway.example.test', '-option', 'host\nscript=/bin/sh', '2001:db8::1'):
                    os.environ['VPN_GATEWAY_HOST'] = bad
                    with self.assertRaises(ValueError):
                        runtime.configuration()

    def test_hook_ignores_pushed_dns_and_routes_and_invalidates_reconnect(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            env = {'reason': 'connect', 'TUNDEV': 'vpn0', 'VPNGATEWAY': '203.0.113.1', 'VPN_SERVER': '203.0.113.1', 'INTERNAL_IP4_ADDRESS': '198.51.100.10', 'INTERNAL_IP4_DNS': '192.0.2.99; touch /tmp/bad', 'CISCO_SPLIT_INC': 'untrusted'}
            with patch.dict(os.environ, env, clear=True), patch.object(runtime, 'RUN', root), patch.object(runtime, 'command') as command:
                runtime.hook()
                self.assertEqual((root / 'connected').read_text(), '198.51.100.10')
                args = [call.args for call in command.call_args_list]
                self.assertEqual(args[0], ('/usr/local/bin/vpn-enforce', 'check'))
                self.assertIn(('ip', '-4', 'route', 'replace', 'default', 'dev', 'vpn0', 'mtu', '1400', 'advmss', '1360'), args)
                self.assertNotIn('192.0.2.99', str(args))
                os.environ['VPNGATEWAY'] = '203.0.113.2'
                with self.assertRaises(ValueError):
                    runtime.hook()

                os.environ['reason'] = 'attempt-reconnect'
                runtime.hook()
                self.assertFalse((root / 'connected').exists())
                self.assertTrue((root / 'lost').exists())
                os.environ['reason'] = 'connect'
                os.environ['VPNGATEWAY'] = env['VPNGATEWAY']
                with self.assertRaises(ValueError):
                    runtime.hook()

    def test_insecure_mode_needs_no_ca_and_is_never_an_implicit_fallback(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            for name, value in [('username', 'synthetic-user'), ('password', 'synthetic-password')]:
                (root / name).write_text(value)
            env = {'VPN_SERVER': '203.0.113.1', 'VPN_GATEWAY_HOST': '203.0.113.1', 'VPN_USERNAME_FILE': str(root / 'username'), 'VPN_PASSWORD_FILE': str(root / 'password')}
            with patch.dict(os.environ, env, clear=True), patch.object(runtime, 'RUN', root), patch.object(runtime, 'unverified_peer_key', return_value='sha256:' + 'a' * 64) as peer:
                with self.assertRaises(KeyError):
                    runtime.configuration()
                peer.assert_not_called()
                os.environ['VPN_INSECURE_SKIP_TLS_VERIFY'] = 'true'
                self.assertEqual(runtime.configuration(), b'synthetic-password\n')
                config = (root / 'client.conf').read_text()
                self.assertIn('servercert=sha256:' + 'a' * 64 + '\n', config)
                self.assertNotIn('cafile=', config)
                self.assertFalse((root / 'ca.crt').exists())
                peer.assert_called_once_with('203.0.113.1', '203.0.113.1')
                os.environ['VPN_INSECURE_SKIP_TLS_VERIFY'] = 'yes'
                with self.assertRaises(ValueError):
                    runtime.configuration()

    def test_large_outer_or_pushed_mtu_cannot_prevent_tunnel_setup(self):
        with tempfile.TemporaryDirectory() as directory:
            env = {'reason': 'connect', 'TUNDEV': 'vpn0', 'VPNGATEWAY': '203.0.113.1', 'VPN_SERVER': '203.0.113.1', 'INTERNAL_IP4_ADDRESS': '198.51.100.10', 'INTERNAL_IP4_MTU': '65454'}
            with patch.dict(os.environ, env, clear=True), patch.object(runtime, 'RUN', Path(directory)), patch.object(runtime, 'command') as command:
                runtime.hook()
                self.assertIn(('ip', 'link', 'set', 'dev', 'vpn0', 'mtu', '1400', 'up'), [call.args for call in command.call_args_list])
                self.assertTrue((Path(directory) / 'connected').is_file())
                for invalid in ('0', '575', '65536', '1400;true'):
                    os.environ['INTERNAL_IP4_MTU'] = invalid
                    with self.assertRaises(ValueError):
                        runtime.hook()

    def test_readiness_requires_firewall_process_interface_routes_and_address(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            (root / 'connected').write_text('198.51.100.10')
            (root / 'client.pid').write_text('42')
            def command(*args):
                if args[0].endswith('vpn-enforce'):
                    return b''
                if 'addr' in args:
                    return json.dumps([{'flags': ['UP'], 'addr_info': [{'local': '198.51.100.10'}]}]).encode()
                return json.dumps([{'dev': 'eth0' if 'get' in args else 'vpn0'}]).encode()
            original = Path.read_text
            def read(path, *args, **kwargs):
                return 'openconnect\n' if str(path) == '/proc/42/comm' else original(path, *args, **kwargs)
            with patch.object(runtime, 'RUN', root), patch.object(Path, 'read_text', read), patch.dict(os.environ, {'VPN_SERVER': '203.0.113.1'}), patch.object(runtime, 'command', side_effect=command) as cmd:
                self.assertTrue(runtime.protected())
                cmd.side_effect = subprocess.CalledProcessError(1, 'vpn-enforce')
                with self.assertRaises(subprocess.CalledProcessError):
                    runtime.protected()
                cmd.side_effect = command
                (root / 'lost').touch()
                self.assertFalse(runtime.protected())
                (root / 'lost').unlink()
                (root / 'connected').write_text('198.51.100.11')
                self.assertFalse(runtime.protected())

    def test_profile_mtu_overrides_default_and_sets_route_mss(self):
        with tempfile.TemporaryDirectory() as directory:
            env = {'reason': 'connect', 'TUNDEV': 'vpn0', 'VPNGATEWAY': '203.0.113.1', 'VPN_SERVER': '203.0.113.1', 'INTERNAL_IP4_ADDRESS': '198.51.100.10', 'INTERNAL_IP4_MTU': '1400', 'VPN_MTU': '1300'}
            with patch.dict(os.environ, env, clear=True), patch.object(runtime, 'RUN', Path(directory)), patch.object(runtime, 'command') as command:
                self.assertEqual(runtime.configured_mtu(), 1300)
                runtime.hook()
                self.assertIn(('ip', '-4', 'route', 'replace', 'default', 'dev', 'vpn0', 'mtu', '1300', 'advmss', '1260'), [call.args for call in command.call_args_list])
                for invalid in ('0', '575', '1501', 'bad'):
                    os.environ['VPN_MTU'] = invalid
                    with self.assertRaises(ValueError):
                        runtime.configured_mtu()

    def test_no_credentials_are_read_if_firewall_fails(self):
        with tempfile.TemporaryDirectory() as directory:
            with patch.object(runtime, 'RUN', Path(directory)), patch.object(runtime, 'command', side_effect=OSError), patch.object(runtime, 'configuration') as config, patch.object(runtime.subprocess, 'Popen') as popen:
                with self.assertRaises(OSError):
                    runtime.main()
                config.assert_not_called()
                popen.assert_not_called()

    def test_client_failure_is_permanent_and_children_are_reaped(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            dns = Mock()
            dns.poll.return_value = None
            client = Mock()
            client.pid = 42
            client.poll.return_value = 1
            with patch.object(runtime, 'RUN', root), patch.object(runtime, 'STOP', False), patch.object(runtime, 'command'), patch.object(runtime, 'pin_gateway'), patch.object(runtime, 'dns_configuration', return_value=''), patch.object(runtime, 'configuration', return_value=b'synthetic-password\n'), patch.object(runtime.subprocess, 'Popen', side_effect=[dns, client]) as popen:
                self.assertEqual(runtime.main(), 2)
                self.assertNotIn('synthetic-password', str(popen.call_args_list))
                client.stdin.write.assert_called_once_with(b'synthetic-password\n')
                client.stdin.close.assert_called_once()
                dns.terminate.assert_called_once()
                dns.wait.assert_called_once()
                client.wait.assert_called_once()
                self.assertFalse((root / 'client.pid').exists())


if __name__ == '__main__':
    unittest.main()
