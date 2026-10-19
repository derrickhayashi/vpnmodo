"""Profile parsing and backend dispatch without a live VPN or Docker daemon."""
import os
from pathlib import Path
import shutil
import json
import shlex
import subprocess
import tempfile
import unittest

ROOT = Path(__file__).resolve().parents[1]


class ProfileTests(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.addCleanup(self.temp.cleanup)
        self.root = Path(self.temp.name)
        (self.root / 'scripts').mkdir()
        shutil.copy2(ROOT / 'scripts/vpn', self.root / 'scripts/vpn')
        self.consumer = self.root / 'consumer'
        self.consumer.mkdir()
        self.profiles = self.consumer / 'profiles'
        self.profiles.mkdir()

    def profile(self, name='one', extra='', backend='l2tp-ipsec'):
        path = self.profiles / name
        path.mkdir()
        (path / 'profile.env').write_text(
            f'VPN_BACKEND={backend}\nVPN_SERVER=203.0.113.10\n'
            f'VPN_HEALTH_HOST=192.0.2.10\nVPN_IMAGE=test:local\n{extra}'
        )

    def run_cli(self, *args, env=None):
        return subprocess.run(['bash', str(self.root / 'scripts/vpn'), '--profiles-dir', str(self.profiles), *args],
                              capture_output=True, text=True, env=env)

    def fake_runtime(self):
        """Record Docker calls without running containers or requiring sudo."""
        bin_dir = self.root / 'bin'
        bin_dir.mkdir()
        commands = {
            'sudo': 'shift\nexec "$@"\n',
            'flock': 'exit 0\n',
            'docker': (
                'printf "%s\\n" "$*" >> "$VPN_TEST_LOG"\n'
                'case "$*" in\n'
                '  *" config") printf "rendered manifest\\n";;\n'
                '  *" ps -q vpn") printf "%s" "${VPN_FAKE_ID-vpn-id}";;\n'
                '  inspect*) printf "%s\\n" "${VPN_FAKE_HEALTH-healthy}";;\n'
                'esac\n'
                'exit "${VPN_FAKE_EXIT:-0}"\n'
            ),
        }
        for name, body in commands.items():
            command = bin_dir / name
            command.write_text('#!/bin/sh\n' + body)
            command.chmod(0o755)
        return dict(os.environ, PATH=f'{bin_dir}:{os.environ["PATH"]}',
                    VPN_TEST_LOG=str(self.root / 'docker.log'))

    def test_usage_preserves_exit_code(self):
        result = self.run_cli('one')
        self.assertEqual(result.returncode, 2, result.stderr)
        self.assertIn('Usage:', result.stderr)
        self.assertNotIn('unbound variable', result.stderr)

    def test_failed_command_reports_phase_and_preserves_exit_code(self):
        private_value = 'value-that-must-not-appear-in-diagnostics'
        self.profile(extra=f'VPN_DNS={private_value}\n')
        env = self.fake_runtime()
        env['VPN_FAKE_EXIT'] = '23'
        result = self.run_cli('--local', 'one', 'build', env=env)
        self.assertEqual(result.returncode, 23, result.stderr)
        self.assertIn('run build (l2tp-ipsec) failed (exit 23; profile=one, action=build)',
                      result.stderr)
        self.assertNotIn(private_value, result.stderr)
        self.assertNotIn('docker compose', result.stderr)

    def test_failed_render_preserves_manifest_and_removes_temporary_file(self):
        self.profile()
        env = self.fake_runtime()
        env['VPN_FAKE_EXIT'] = '23'
        runtime = self.consumer / 'instances/one'
        runtime.mkdir(parents=True)
        manifest = runtime / 'compose.yaml'
        manifest.write_text('existing manifest')
        result = self.run_cli('--local', 'one', 'connect', env=env)
        self.assertEqual(result.returncode, 1, result.stderr)
        self.assertIn('Could not render', result.stderr)
        self.assertNotIn('unbound variable', result.stderr)
        self.assertEqual(manifest.read_text(), 'existing manifest')
        self.assertEqual(list(runtime.glob('.compose.*')), [])
        self.assertNotIn(' up ', Path(env['VPN_TEST_LOG']).read_text())

    def test_l2tp_status_does_not_create_runtime_or_lock(self):
        self.profile()
        env = self.fake_runtime()
        for health, status in [('healthy', 0), ('unhealthy', 1), ('starting', 1)]:
            with self.subTest(health=health):
                env['VPN_FAKE_HEALTH'] = health
                result = self.run_cli('--local', 'one', 'status', env=env)
                self.assertEqual(result.returncode, status, result.stderr)
                self.assertEqual(result.stdout, f'VPN: {health}\n')
        env['VPN_FAKE_ID'] = ''
        result = self.run_cli('--local', 'one', 'status', env=env)
        self.assertEqual(result.returncode, 1, result.stderr)
        self.assertIn('VPN is stopped.', result.stderr)
        self.assertFalse((self.consumer / 'instances').exists())

    def test_l2tp_connect_and_disconnect_dispatch(self):
        self.profile()
        env = self.fake_runtime()
        runtime = self.consumer / 'instances/one'
        (runtime / 'secrets').mkdir(parents=True)
        result = self.run_cli('--local', 'one', 'connect', env=env)
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertEqual((runtime / 'compose.yaml').read_text(), 'rendered manifest\n')
        self.assertIn('up -d --wait --wait-timeout 120', Path(env['VPN_TEST_LOG']).read_text())
        result = self.run_cli('--local', 'one', 'disconnect', env=env)
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertTrue(Path(env['VPN_TEST_LOG']).read_text().splitlines()[-1].endswith(' down'))

    def test_xauth_handoff_preserves_action_environment_and_exit_code(self):
        self.profile(backend='ipsec-xauth')
        env = self.fake_runtime()
        backend = self.root / 'containers/ipsec-xauth'
        backend.mkdir(parents=True)
        (backend / 'vpn').write_text(
            'printf "%s|%s|%s|%s\\n" "$1" "$VPN_COMPOSE_FILE" '
            '"$VPN_WATCHDOG_MODE" "${VPN_OPERATION_LOCKED:-0}"\nexit 7\n')
        runtime = self.consumer / 'instances/one'
        for action in ['connect', 'status', 'disconnect']:
            with self.subTest(action=action):
                result = self.run_cli('--local', 'one', action, env=env)
                self.assertEqual(result.returncode, 7, result.stderr)
                locked = '0' if action == 'status' else '1'
                self.assertEqual(result.stdout, f'{action}|{runtime}/compose.yaml|manual|{locked}\n')

    def test_explicit_consumer_directory_required(self):
        result = subprocess.run(['bash', str(self.root / 'scripts/vpn'), 'list'],
                                capture_output=True, text=True)
        self.assertNotEqual(result.returncode, 0)
        self.assertIn('--profiles-dir', result.stderr)

    @unittest.skipUnless(shutil.which('docker'), 'Docker Compose is required')
    def test_connect_persists_combined_manifest_outside_source(self):
        bin_dir = self.root / 'bin'
        bin_dir.mkdir()
        (bin_dir / 'flock').write_text('#!/bin/sh\nexit 0\n')
        (bin_dir / 'flock').chmod(0o755)
        real_docker = shutil.which('docker')
        sudo = bin_dir / 'sudo'
        sudo.write_text('#!/bin/sh\nshift\nexec "$@"\n')
        sudo.chmod(0o755)
        docker = bin_dir / 'docker'
        docker.write_text('#!/bin/sh\ncase "$*" in *" config") exec '
                          + shlex.quote(real_docker) + ' "$@";; *) exit 0;; esac\n')
        docker.chmod(0o755)
        (self.root / 'watchdog').mkdir()
        shutil.copy2(ROOT / 'watchdog/compose.yaml', self.root / 'watchdog/compose.yaml')
        env = dict(os.environ, PATH=f'{bin_dir}:{os.environ["PATH"]}')
        for name, backend in [('one', 'l2tp-ipsec'), ('two', 'ipsec-xauth')]:
            with self.subTest(backend=backend):
                self.profile(name, backend=backend)
                target = self.root / 'containers' / backend
                target.mkdir(parents=True)
                shutil.copy2(ROOT / 'containers' / backend / 'compose.yaml', target / 'compose.yaml')
                if backend == 'ipsec-xauth':
                    (target / 'vpn').write_text('#!/bin/sh\ntest -f "$VPN_COMPOSE_FILE"\n')
                runtime = self.consumer / 'instances' / name
                (runtime / 'secrets').mkdir(parents=True)
                result = self.run_cli('--local', name, 'connect', env=env)
                self.assertEqual(result.returncode, 0, result.stderr)
                rendered = runtime / 'compose.yaml'
                model = json.loads(subprocess.check_output(
                    [real_docker, 'compose', '-f', str(rendered), 'config', '--format', 'json'],
                    text=True))
                watcher = model['services']['watchdog']
                self.assertEqual(watcher['environment']['VPN_PROJECT_DIR'], str(runtime))
                self.assertEqual(watcher['environment']['VPN_WATCHDOG_MODE'],
                                 'auto' if backend == 'l2tp-ipsec' else 'manual')
                self.assertNotIn('build', watcher)
                self.assertNotIn('build', model['services']['vpn'])

    def test_busy_operation_cannot_replace_runtime_manifest(self):
        self.profile()
        runtime = self.consumer / 'instances/one'
        runtime.mkdir(parents=True)
        manifest = runtime / 'compose.yaml'
        manifest.write_text('existing manifest')
        bin_dir = self.root / 'bin'
        bin_dir.mkdir()
        (bin_dir / 'flock').write_text('#!/bin/sh\nexit 1\n')
        (bin_dir / 'flock').chmod(0o755)
        env = dict(os.environ, PATH=f'{bin_dir}:{os.environ["PATH"]}')
        result = self.run_cli('--local', 'one', 'connect', env=env)
        self.assertNotEqual(result.returncode, 0)
        self.assertIn('Another VPN operation is active', result.stderr)
        self.assertEqual(manifest.read_text(), 'existing manifest')

    def test_remote_dispatch_uses_consumer_directory(self):
        self.profile(extra='VPN_SSH_HOST=vpn-host\nVPN_REMOTE_ROOT=/srv/source\n'
                           'VPN_REMOTE_PROFILES_DIR=/srv/consumer/vpn/profiles\n')
        bin_dir = self.root / 'bin'
        bin_dir.mkdir()
        ssh = bin_dir / 'ssh'
        ssh.write_text('#!/bin/sh\nprintf "%s\\n" "$@"\n')
        ssh.chmod(0o755)
        env = dict(os.environ, PATH=f'{bin_dir}:{os.environ["PATH"]}')
        result = self.run_cli('one', 'status', env=env)
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertIn('/srv/source/scripts/vpn', result.stdout)
        self.assertIn('--profiles-dir\n/srv/consumer/vpn/profiles', result.stdout)
        self.assertIn('--local\none\nstatus', result.stdout)

    def test_list_excludes_templates(self):
        self.profile()
        (self.profiles / 'template').mkdir()
        (self.profiles / 'template/profile.env.example').touch()
        result = self.run_cli('list')
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertEqual(result.stdout, 'one\n')

    def test_both_backends_validate(self):
        for name, backend in [('one', 'l2tp-ipsec'), ('two', 'ipsec-xauth')]:
            self.profile(name, backend=backend)
            result = self.run_cli(name, 'validate')
            self.assertEqual(result.returncode, 0, result.stderr)

    def test_xauth_gateway_requires_ipv4(self):
        for name, gateway in [('one', 'vpn.example.com'), ('two', '2001:db8::1'),
                              ('three', '203.0.113.999')]:
            self.profile(name, backend='ipsec-xauth')
            path = self.profiles / name / 'profile.env'
            path.write_text(path.read_text().replace('203.0.113.10', gateway))
            result = self.run_cli(name, 'validate')
            self.assertNotEqual(result.returncode, 0)
            self.assertIn('IPv4', result.stderr)

    def test_duplicate_field_has_context(self):
        self.profile(extra='VPN_SERVER=203.0.113.20\n')
        result = self.run_cli('one', 'validate')
        self.assertNotEqual(result.returncode, 0)
        self.assertIn('Duplicate profile field VPN_SERVER', result.stderr)
        self.assertIn('one/profile.env', result.stderr)

    def test_noncanonical_names_rejected(self):
        for name in ['One', 'one_two', 'one--two', '../one', '-one']:
            result = self.run_cli(name, 'validate')
            self.assertNotEqual(result.returncode, 0)
            self.assertIn('Profile names', result.stderr)

    def test_profile_is_data_not_shell(self):
        sentinel = self.root / 'executed'
        self.profile(extra=f'VPN_DNS=$(touch {sentinel})\n')
        result = self.run_cli('one', 'validate')
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertFalse(sentinel.exists())

    def test_invalid_port_and_unknown_fields_rejected(self):
        for name, extra in [('one', 'VPN_HEALTH_PORT=70000\n'),
                            ('two', 'COMPOSE_PROJECT_NAME=shared\n')]:
            self.profile(name, extra=extra)
            self.assertNotEqual(self.run_cli(name, 'validate').returncode, 0)

    def test_ambient_settings_do_not_leak_between_profiles(self):
        self.profile()
        env = dict(os.environ, VPN_SSH_HOST='-bad', VPN_HEALTH_PORT='bad')
        result = self.run_cli('one', 'validate', env=env)
        self.assertEqual(result.returncode, 0, result.stderr)

    def test_projects_and_runtime_directories_are_isolated(self):
        bin_dir = self.root / 'bin'
        bin_dir.mkdir()
        (bin_dir / 'flock').write_text('#!/bin/sh\nexit 0\n')
        (bin_dir / 'flock').chmod(0o755)
        sudo = bin_dir / 'sudo'
        sudo.write_text('#!/bin/sh\nshift\nexec env -i PATH="$PATH" "$@"\n')
        docker = bin_dir / 'docker'
        docker.write_text('#!/bin/sh\nprintf "%s|%s|%s\\n" "$COMPOSE_PROJECT_NAME" "$VPN_PROJECT_DIR" "$*"\n')
        docker.chmod(0o755)
        sudo.chmod(0o755)
        env = dict(os.environ, PATH=f'{bin_dir}:{os.environ["PATH"]}')
        outputs = []
        for name in ['one', 'two']:
            self.profile(name)
            result = self.run_cli('--local', name, 'build', env=env)
            self.assertEqual(result.returncode, 0, result.stderr)
            self.assertIn(f'vpn-{name}|{self.consumer}/instances/{name}', result.stdout)
            self.assertIn(str(self.root / 'containers/l2tp-ipsec/compose.yaml'), result.stdout)
            self.assertIn(str(self.root / 'containers/l2tp-ipsec/compose.build.yaml'), result.stdout)
            self.assertIn('watchdog/compose.build.yaml', result.stdout)
            self.assertIn('build vpn watchdog', result.stdout)
            outputs.append(result.stdout)
        self.assertNotEqual(*outputs)


if __name__ == '__main__':
    unittest.main()
