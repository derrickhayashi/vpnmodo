"""Validate deployment and local build models without starting containers."""
import json
import os
from pathlib import Path
import shutil
import subprocess
import tempfile
import unittest

ROOT = Path(__file__).resolve().parents[1]
BACKEND = ROOT / 'containers/l2tp-ipsec'


@unittest.skipUnless(shutil.which('docker'), 'Docker Compose is required')
class ComposeTests(unittest.TestCase):
    def resolve(self, override=False, settings=None, backend=BACKEND, watchdog=False):
        with tempfile.TemporaryDirectory() as directory:
            env_file = Path(directory) / '.env'
            settings = settings or (backend / '.env.example').read_text()
            if watchdog:
                settings += ('\nVPN_SOURCE_ROOT=' + str(ROOT) + '\nVPN_PROJECT_DIR=/srv/consumer/vpn\n'
                             'VPN_CREDENTIALS_DIR=/srv/consumer/vpn/secrets\n'
                             'VPN_WATCHDOG_DIR=' + str(ROOT / 'watchdog') + '\nVPN_WATCHDOG_MODE='
                             + ('auto' if backend == BACKEND else 'manual') + '\n')
            env_file.write_text(settings)
            env = {key: value for key, value in os.environ.items()
                   if not key.startswith(('VPN_', 'COMPOSE_'))}
            args = ['docker', 'compose', '--env-file', str(env_file),
                    '-f', str(backend / 'compose.yaml')]
            if watchdog:
                args += ['-f', str(ROOT / 'watchdog/compose.yaml')]
            if override:
                args += ['-f', str(backend / 'compose.build.yaml')]
                if watchdog:
                    args += ['-f', str(ROOT / 'watchdog/compose.build.yaml')]
            result = subprocess.run(args + ['config', '--format', 'json'],
                                    env=env, cwd=directory, capture_output=True,
                                    text=True, check=True)
            return json.loads(result.stdout)

    def test_registry_deployment_needs_no_build_context(self):
        settings = (BACKEND / '.env.example').read_text().replace(
            'l2tp-ipsec-vpn-sidecar:dev',
            'registry.example.com/networking/vpn@sha256:' + 'a' * 64)
        model = self.resolve(settings=settings)
        self.assertNotIn('build', model['services']['vpn'])
        self.assertEqual(model['services']['vpn']['image'],
                         'registry.example.com/networking/vpn@sha256:' + 'a' * 64)
        self.assertEqual(model['services']['application']['network_mode'], 'service:vpn')
        self.assertEqual(model['services']['application']['depends_on']['vpn']['condition'],
                         'service_healthy')

    def test_development_override_builds_the_source(self):
        model = self.resolve(override=True)
        self.assertEqual(Path(model['services']['vpn']['build']['context']), BACKEND)
        self.assertEqual(model['services']['vpn']['image'],
                         'l2tp-ipsec-vpn-sidecar:dev')

    def test_xauth_runtime_is_image_only(self):
        backend = ROOT / 'containers/ipsec-xauth'
        model = self.resolve(backend=backend)
        self.assertNotIn('build', model['services']['vpn'])
        self.assertEqual(model['services']['vpn']['image'], 'vpn-sidecar-ipsec-xauth:local')
        self.assertEqual(model['services']['application']['network_mode'], 'service:vpn')
        sources = {volume['source'] for volume in model['services']['vpn']['volumes']}
        self.assertIn('/path/to/vpn-sidecar/containers/ipsec-xauth/connect.exp', sources)

    def test_xauth_development_override_uses_backend_context(self):
        backend = ROOT / 'containers/ipsec-xauth'
        model = self.resolve(override=True, backend=backend)
        self.assertEqual(Path(model['services']['vpn']['build']['context']), backend)
        self.assertTrue((backend / 'Dockerfile').is_file())

    def test_shared_watchdog_overlay_supports_both_backends(self):
        for backend in [BACKEND, ROOT / 'containers/ipsec-xauth']:
            with self.subTest(backend=backend.name):
                model = self.resolve(backend=backend, watchdog=True)
                watcher = model['services']['watchdog']
                self.assertEqual(watcher['network_mode'], 'none')
                self.assertEqual(watcher['environment']['VPN_WATCHDOG_MODE'],
                                 'auto' if backend == BACKEND else 'manual')
                self.assertTrue(watcher['read_only'])
                mounts = {v['target']: v for v in watcher['volumes']}
                self.assertEqual(mounts['/var/run/docker.sock']['source'], '/var/run/docker.sock')
                self.assertTrue(mounts['/srv/consumer/vpn']['read_only'])
                self.assertTrue(mounts['/srv/consumer/vpn/secrets']['read_only'])
                self.assertEqual(mounts['/state']['type'], 'volume')

    def test_shared_watchdog_build_uses_its_own_context(self):
        for backend in [BACKEND, ROOT / 'containers/ipsec-xauth']:
            model = self.resolve(override=True, backend=backend, watchdog=True)
            self.assertEqual(Path(model['services']['watchdog']['build']['context']), ROOT / 'watchdog')
            self.assertEqual(Path(model['services']['vpn']['build']['context']), backend)

    def test_external_credentials_remain_runtime_mounts(self):
        settings = (BACKEND / '.env.example').read_text() + '\nVPN_CREDENTIALS_DIR=/srv/private/vpn\n'
        model = self.resolve(settings=settings)
        for key in ('username', 'password', 'psk'):
            self.assertEqual(model['secrets']['vpn_' + key]['file'], '/srv/private/vpn/' + key)
            self.assertEqual(model['services']['vpn']['environment']['VPN_' + key.upper() + '_FILE'],
                             '/run/secrets/vpn_' + key)


if __name__ == '__main__':
    unittest.main()
