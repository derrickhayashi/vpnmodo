"""Upgrade transactions use fake Docker and synthetic private state only."""
import hashlib
import importlib.util
import json
import os
from pathlib import Path
import tempfile
import types
import unittest
from unittest.mock import patch

ROOT = Path(__file__).resolve().parents[1]


def module(path, name):
    result = types.ModuleType(name)
    exec(compile(path.read_text(), str(path), 'exec'), result.__dict__)
    return result


class UpgradeTests(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory()
        self.addCleanup(self.tmp.cleanup)
        self.home = Path(self.tmp.name).resolve()
        self.root = self.home / '.local/share/vpnmodo'
        self.state = self.root / 'controller-state'
        self.state.mkdir(parents=True, mode=0o700)
        self.root.chmod(0o700)
        (self.state / 'synthetic-vault').write_bytes(b'synthetic-secret')
        (self.root / 'controller.compose.json').write_text('original manifest')
        self.controller = {'Config': {'Labels': {'io.vpnmodo.managed-by': 'vpnmodo', 'io.vpnmodo.component': 'controller', 'com.docker.compose.project': 'vpnmodo-controller'}}, 'Mounts': [{'Type': 'bind', 'Source': str(self.root), 'Destination': str(self.root)}], 'State': {'Running': True, 'Paused': False}}
        self.calls = []
        self.fail = None
        self.ids = ['backend-1', 'application-1']
        self.version = 'old'
        self.script = module(ROOT / 'internal/host/bootstrap.py', 'bootstrap')
        code = (ROOT / 'internal/host/upgrade.py').read_text()
        exec(compile(code, 'upgrade.py', 'exec'), self.script.__dict__)
        self.script.docker = self.docker
        self.script.inspect = lambda *args: self.controller
        self.script.activate = self.activate
        self.script.I = {'install_registry': lambda path: print(json.dumps({'controller_image': 'sha256:' + 'a' * 64, 'backend_images': {}}))}
        self.params = {'action': 'prepare', 'token': 'a' * 32, 'runtime': {'binary_version': 'new'}}
        context = patch.object(self.script.Path, 'home', lambda: self.home)
        context.start()
        self.addCleanup(context.stop)

    def docker(self, *args, **kwargs):
        self.calls.append(args)
        if args[0] == 'ps':
            return '\n'.join(self.ids).encode()
        if args[0] == 'pause':
            self.controller['State']['Paused'] = True
        if args[0] == 'unpause':
            self.controller['State']['Paused'] = False
        if args[0] == 'run' and self.fail == 'check':
            raise self.script.SetupError('active operation')
        if args[0] == 'compose':
            self.version = 'old'
            self.controller['State']['Paused'] = False
        if args[:2] == ('exec', 'vpnmodo-controller') and args[-1] == 'version':
            return self.version.encode()
        return b''

    def activate(self, *args):
        self.version = 'new'
        self.controller['State']['Paused'] = False
        if self.fail == 'activate':
            (self.state / 'synthetic-vault').write_bytes(b'candidate changed state')
            raise self.script.SetupError('candidate failed')

    def action(self, action):
        return self.script.upgrade(dict(action=action, token=self.params['token']))

    def test_prepare_commit_preserves_settings_and_backend_ids(self):
        self.assertEqual(self.script.upgrade(self.params)['phase'], 'prepared')
        self.assertTrue((self.root / 'upgrade-pending').exists())
        self.assertEqual((self.state / 'synthetic-vault').read_bytes(), b'synthetic-secret')
        self.assertEqual(self.action('commit')['phase'], 'committed')
        self.assertFalse((self.root / 'upgrade-pending').exists())
        self.assertEqual(self.ids, ['backend-1', 'application-1'])
        for call in self.calls:
            if call[0] in ('pause', 'unpause', 'exec'):
                self.assertEqual(call[1], 'vpnmodo-controller')
            if call[0] == 'rm':
                self.assertEqual(call, ('rm', '--force', 'vpnmodo-controller'))
        backup = self.root / 'upgrades' / self.params['token']
        self.assertEqual(backup.stat().st_mode & 0o777, 0o700)
        with self.assertRaises(self.script.SetupError):
            self.action('rollback')

    def test_activation_failure_restores_state_and_old_controller(self):
        self.fail = 'activate'
        with self.assertRaises(self.script.SetupError):
            self.script.upgrade(self.params)
        self.assertEqual(self.version, 'old')
        self.assertEqual((self.state / 'synthetic-vault').read_bytes(), b'synthetic-secret')
        self.assertEqual((self.root / 'controller.compose.json').read_text(), 'original manifest')
        self.assertEqual(self.action('status')['phase'], 'rolled_back')
        self.assertFalse((self.root / 'upgrade-pending').exists())

    def test_active_operation_refusal_unpauses_original(self):
        self.fail = 'check'
        with self.assertRaises(self.script.SetupError):
            self.script.upgrade(self.params)
        self.assertFalse(self.controller['State']['Paused'])
        self.assertFalse(any(call[0] == 'rm' for call in self.calls))

    def test_pending_rollback_and_repeated_recovery(self):
        self.script.upgrade(self.params)
        self.assertEqual(self.action('rollback')['phase'], 'rolled_back')
        self.assertEqual(self.action('rollback')['phase'], 'rolled_back')
        self.assertEqual(self.version, 'old')

    def test_rollback_recovers_missing_live_state_after_interruption(self):
        import shutil
        self.script.upgrade(self.params)
        shutil.rmtree(self.state)
        self.assertEqual(self.action('rollback')['phase'], 'rolled_back')
        self.assertEqual((self.state / 'synthetic-vault').read_bytes(), b'synthetic-secret')

    def test_commit_interrupted_before_marker_removal(self):
        self.script.upgrade(self.params)
        record = self.root / 'upgrades' / self.params['token'] / 'transaction.json'
        data = json.loads(record.read_text())
        data['phase'] = 'committed'
        record.write_text(json.dumps(data))
        self.assertEqual(self.action('commit')['phase'], 'committed')
        self.assertFalse((self.root / 'upgrade-pending').exists())

    def test_foreign_deployment_and_symlink_are_refused(self):
        self.controller['Config']['Labels'] = {}
        with self.assertRaises(self.script.SetupError):
            self.script.upgrade(self.params)
        self.assertFalse(self.calls)

    def test_state_symlink_refused_without_copying_secrets(self):
        (self.state / 'unsafe').symlink_to('/etc/passwd')
        with self.assertRaises(self.script.SetupError):
            self.script.upgrade(self.params)
        self.assertFalse(any(call[0] == 'pause' for call in self.calls))

class UpgradeManifestTests(unittest.TestCase):
    def test_matches_cli_archive_and_refuses_mixed_revisions(self):
        import io
        import tarfile
        builder = module(ROOT / 'scripts/build-upgrade-manifest.py', 'upgrade_builder')
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            data = b'synthetic CLI'
            (root / 'vpnmodo-darwin-arm64').write_bytes(data)
            runtime = dict(schema_version=1, version='v1.2.3', binary_version='v1.2.3+git.0123456789ab', os='linux', arch='arm64', images={role: 'ghcr.io/derrickhayashi/vpnmodo/' + role + '@sha256:' + 'a' * 64 for role in ('controller', 'l2tp', 'xauth', 'application')})
            path = root / 'runtime.json'
            path.write_text(json.dumps(runtime))
            cli = dict(version=runtime['version'], binary_version=runtime['binary_version'], binary_sha256=hashlib.sha256(data).hexdigest(), os='darwin', arch='arm64')
            with tarfile.open(root / 'vpnmodo-v1.2.3-darwin-arm64.tar.gz', 'w:gz') as archive:
                raw = json.dumps(cli).encode()
                entry = tarfile.TarInfo('release.json')
                entry.size = len(raw)
                archive.addfile(entry, io.BytesIO(raw))
            self.assertEqual(builder.build(root, path)['cli']['size'], len(data))
            runtime['binary_version'] = 'v1.2.3+git.ffffffffffff'
            path.write_text(json.dumps(runtime))
            with self.assertRaises(ValueError):
                builder.build(root, path)


if __name__ == '__main__':
    unittest.main()
