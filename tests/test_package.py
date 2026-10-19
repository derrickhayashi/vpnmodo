"""Package and tunnel tests; see README.md for the complete suite environment."""
import importlib.machinery
import importlib.util
import json
import contextlib
import io
from unittest.mock import patch
import sys
from pathlib import Path
import runpy
import tempfile
import unittest

ROOT = Path(__file__).resolve().parents[1]
loader = importlib.machinery.SourceFileLoader('vpn_services', str(ROOT / 'scripts/vpn-services'))
spec = importlib.util.spec_from_loader(loader.name, loader)
services = importlib.util.module_from_spec(spec)
loader.exec_module(services)


class WatchdogTests(unittest.TestCase):
    def test_recovery_decisions(self):
        tests = runpy.run_path(str(ROOT / 'tests/test_vpn_watchdog.py'))
        for name, function in tests.items():
            if name.startswith('test_'):
                with self.subTest(name=name), tempfile.TemporaryDirectory() as directory:
                    function(Path(directory))


class TunnelTests(unittest.TestCase):
    def load(self, mutate=None):
        with tempfile.TemporaryDirectory() as directory:
            path = Path(directory)
            (path / 'ssh_config').write_text('Host example-vpn-jump\n')
            config = {'name': 'example', 'ssh_config': 'ssh_config', 'ssh_target': 'example-vpn-jump',
                      'forwards': [{'local_port': 18081, 'remote_port': 8081}]}
            if mutate:
                mutate(config)
            (path / 'services.json').write_text(json.dumps(config))
            return services.load_config(path / 'services.json')

    def test_forward_binds_localhost_and_resolves_config_relative_to_file(self):
        config = self.load()
        args = services.tunnel_args(config, '/tmp/example.sock')
        self.assertIn('127.0.0.1:18081:127.0.0.1:8081', args)
        self.assertTrue(Path(config['ssh_config']).is_absolute())
        self.assertEqual(args[-1], 'example-vpn-jump')

    def test_command_preview_does_not_create_state(self):
        with tempfile.TemporaryDirectory(dir='/tmp') as directory:
            output = io.StringIO()
            with patch.object(services.Path, 'home', return_value=Path(directory)), \
                 patch.object(sys, 'argv', ['vpn-services', str(ROOT / 'examples/services.json'), 'command']), \
                 contextlib.redirect_stdout(output):
                self.assertEqual(services.main(), 0)
            self.assertIn('127.0.0.1:18081:127.0.0.1:8081', output.getvalue())
            self.assertEqual(list(Path(directory).iterdir()), [])

    def test_duplicate_ports_rejected(self):
        with self.assertRaises(ValueError):
            self.load(lambda c: c['forwards'].append(c['forwards'][0].copy()))

    def test_out_of_range_port_rejected(self):
        with self.assertRaises(ValueError):
            self.load(lambda c: c['forwards'][0].update(local_port=70000))

    def test_ssh_option_injection_rejected(self):
        with self.assertRaises(ValueError):
            self.load(lambda c: c.update(ssh_target='-oProxyCommand=bad'))

    def test_unsafe_instance_name_rejected(self):
        with self.assertRaises(ValueError):
            self.load(lambda c: c.update(name='../other'))


if __name__ == '__main__':
    unittest.main()
