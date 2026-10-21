"""Registry releases reuse local image receipts and avoid repeat pulls on boot."""
import contextlib
import importlib.util
import io
import json
from pathlib import Path
import tempfile
import unittest
from unittest.mock import patch

ROOT = Path(__file__).resolve().parents[1]


def module(name, path):
    spec = importlib.util.spec_from_file_location(name, path)
    result = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(result)
    return result


loader = module('registry_loader', ROOT / 'scripts/release/install-vm.py')
builder = module('registry_builder', ROOT / 'scripts/build-release.py')
ROLES = ('controller', 'l2tp', 'xauth', 'application')
PREFIX = 'ghcr.io/derrickhayashi/vpnmodo/'


class RegistryTests(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.addCleanup(self.temp.cleanup)
        self.root = Path(self.temp.name)
        self.refs = {role: PREFIX + ('vpntools' if role == 'application' else role) + '@sha256:' + f'{n:064x}' for n, role in enumerate(ROLES, 1)}
        self.images = {ref: {'Id': 'sha256:' + f'{n + 16:064x}', 'Architecture': 'arm64', 'Os': 'linux'}
                       for n, ref in enumerate(self.refs.values(), 1)}
        self.release = {'schema_version': 1, 'version': 'v0.1.0-rc.7',
                        'binary_version': 'v0.1.0-rc.7+git.0123456789ab',
                        'arch': 'arm64', 'os': 'linux', 'images': self.refs}
        self.actual_version = self.release['binary_version']
        self.path = self.root / 'registry-release.json'
        self.cached = {}
        self.calls = []

    def docker(self, *args):
        self.calls.append(args)
        if args[0] == 'info':
            return json.dumps({'OSType': 'linux', 'Architecture': 'aarch64'})
        if args[0] == 'pull':
            self.cached[args[1]] = self.images[args[1]]
        if args[:2] == ('image', 'inspect'):
            return json.dumps([self.images[args[2]]])
        if args[0] == 'run':
            return self.actual_version
        return ''

    def install(self):
        self.path.write_text(json.dumps(self.release))
        output = io.StringIO()
        with patch.object(loader, 'docker', side_effect=self.docker), \
             patch.object(loader, 'inspect_optional', side_effect=self.cached.get), \
             contextlib.redirect_stdout(output):
            loader.install_registry(self.path)
        return json.loads(output.getvalue())

    def test_pull_produces_existing_receipt_with_destination_local_ids(self):
        receipt = self.install()
        self.assertEqual(receipt['controller_image'], self.images[self.refs['controller']]['Id'])
        self.assertEqual(set(receipt['backend_images']), {'l2tp', 'xauth', 'application'})
        self.assertFalse(receipt['activated'])
        self.assertEqual(receipt['version'], self.release['binary_version'])
        self.assertEqual(sum(c[0] == 'pull' for c in self.calls), 4)
        self.assertEqual(next(c for c in self.calls if c[0] == 'run')[1:5],
                         ('--rm', '--pull=never', '--network', 'none'))
        self.calls.clear()
        self.assertEqual(self.install(), receipt)
        self.assertFalse(any(c[0] == 'pull' for c in self.calls))

    def test_existing_published_application_reference_still_installs(self):
        current = self.refs['application']
        old = current.replace('/vpntools@', '/application@')
        self.refs['application'] = old
        self.images[old] = self.images.pop(current)
        receipt = self.install()
        self.assertEqual(receipt['backend_images']['application'], self.images[old]['Id'])

    def test_ikev2_release_includes_immutable_backend_receipt(self):
        ref = PREFIX + 'ikev2@sha256:' + 'f' * 64
        self.refs['ikev2'] = ref
        self.images[ref] = {'Id': 'sha256:' + 'e' * 64, 'Architecture': 'arm64', 'Os': 'linux'}
        receipt = self.install()
        self.assertEqual(receipt['backend_images']['ikev2'], self.images[ref]['Id'])
        self.assertEqual(sum(c[0] == 'pull' for c in self.calls), 5)
        self.calls.clear()
        self.release['images']['ikev2'] = PREFIX + 'ikev2:mutable'
        with self.assertRaises(loader.InstallError):
            self.install()
        self.assertEqual(self.calls, [])

    def test_missing_image_or_mutable_reference_fails_before_docker(self):
        del self.release['images']['xauth']
        with self.assertRaises(loader.InstallError):
            self.install()
        self.release['images']['xauth'] = PREFIX + 'xauth:latest'
        with self.assertRaises(loader.InstallError):
            self.install()
        self.assertEqual(self.calls, [])

    def test_wrong_architecture_or_controller_version_produces_no_receipt(self):
        self.release['arch'] = 'amd64'
        with self.assertRaisesRegex(loader.InstallError, 'architecture'):
            self.install()
        self.release['arch'] = 'arm64'
        self.images[self.refs['controller']]['Architecture'] = 'amd64'
        with self.assertRaisesRegex(loader.InstallError, 'architecture'):
            self.install()
        self.images[self.refs['controller']]['Architecture'] = 'arm64'
        self.actual_version = 'wrong-version'
        with self.assertRaisesRegex(loader.InstallError, 'controller version'):
            self.install()

    def test_cli_runtime_input_requires_same_revision_and_immutable_repository_images(self):
        metadata = {'version': 'v0.1.0-rc.7', 'revision': '0123456789ab' + '0' * 28}
        self.path.write_text(json.dumps(self.release))
        self.assertEqual(json.loads(builder.runtime_input(self.path, metadata)), self.release)
        changes = [
            ('binary_version', 'v0.1.0-rc.7+git.' + 'f' * 12),
            ('version', 'v0.1.0-rc.8'), ('arch', 'amd64'),
            ('images', dict(self.refs, xauth=PREFIX + 'xauth:latest')),
            ('images', dict(self.refs, application=PREFIX + 'vpntools:latest')),
            ('images', dict(self.refs, application=PREFIX + 'controller@sha256:' + 'a' * 64)),
            ('images', dict(self.refs, xauth='ghcr.io/another/repository/xauth@sha256:' + 'a' * 64)),
        ]
        for key, value in changes:
            with self.subTest(field=key, value=value):
                self.path.write_text(json.dumps(dict(self.release, **{key: value})))
                with self.assertRaises(RuntimeError):
                    builder.runtime_input(self.path, metadata)

    def test_publisher_records_registry_digests_not_builder_image_ids(self):
        self.path.write_text(json.dumps(dict(self.release, images={
            role: {'id': self.images[ref]['Id']} for role, ref in self.refs.items()
        })))
        output = self.root / 'output'
        output.mkdir()
        calls = []

        def docker(*args):
            calls.append(args)
            if args[0] == 'push':
                package = args[-1].split('/')[-1].split(':')[0]
                role = 'application' if package == 'vpntools' else package
                digest = self.refs[role].split('@')[1]
                return f'old sha256:{"f" * 64} -> {digest}\n{args[-1]}: digest: {digest} size: 1025'
            return ''

        with patch.object(builder, 'docker', side_effect=docker):
            builder.publish(self.path, output)
        published = json.loads((output / 'registry-release.json').read_text())
        self.assertEqual(published['images'], self.refs)
        self.assertEqual(sum(c[0] == 'push' for c in calls), 4)
        self.assertTrue(all(c[-1].endswith(':v0.1.0-rc.7-arm64') for c in calls if c[0] == 'push'))


if __name__ == '__main__':
    unittest.main()
