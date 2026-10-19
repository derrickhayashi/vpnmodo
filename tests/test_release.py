"""Distribution refusal paths: avoid damaging an existing install or image tags."""
import hashlib
import importlib.util
import io
import json
from pathlib import Path
import subprocess
import tarfile
import tempfile
import unittest
from unittest.mock import patch

ROOT = Path(__file__).resolve().parents[1]


def module(name, path):
    spec = importlib.util.spec_from_file_location(name, path)
    result = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(result)
    return result


builder = module('release_builder', ROOT / 'scripts/build-release.py')
loader = module('release_loader', ROOT / 'scripts/release/install-vm.py')


class ReleaseTests(unittest.TestCase):
    def test_snapshot_excludes_ignored_files_and_refuses_dirty_source(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory) / 'repo'
            root.mkdir()
            def git(*args):
                return subprocess.check_output(['git', '-c', 'user.name=Synthetic Builder', '-c', 'user.email=builder@example.invalid', *args], cwd=root, stderr=subprocess.DEVNULL)
            git('init')
            (root / '.gitignore').write_text('ignored\n')
            (root / 'source').write_text('committed source')
            (root / 'ignored').write_text('synthetic private material')
            git('add', '.gitignore', 'source')
            git('commit', '-m', 'synthetic source')
            temporary = Path(directory) / 'snapshot'
            temporary.mkdir()
            source, metadata = builder.source_snapshot(root, temporary, 'v0.1.0-rc.1')
            self.assertFalse((source / 'ignored').exists())
            self.assertFalse((source / '.git').exists())
            self.assertEqual(metadata['revision'], git('rev-parse', 'HEAD').decode().strip())
            (root / 'source').write_text('modified')
            next_dir = Path(directory) / 'next'
            next_dir.mkdir()
            with self.assertRaisesRegex(RuntimeError, 'dirty'):
                builder.source_snapshot(root, next_dir, 'v0.1.0-rc.1')

    def test_archive_does_not_depend_on_workstation_file_times(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            stage = root / 'stage'
            stage.mkdir()
            file = stage / 'vpnmodo'
            file.write_bytes(b'synthetic binary')
            file.chmod(0o755)
            builder.archive(stage, root / 'a.tar.gz', 123)
            file.touch()
            builder.archive(stage, root / 'b.tar.gz', 123)
            self.assertEqual(builder.digest(root / 'a.tar.gz'), builder.digest(root / 'b.tar.gz'))
            with tarfile.open(root / 'a.tar.gz') as tar:
                self.assertEqual(tar.getmember('vpnmodo').mode, 0o755)

    def bundle(self, root, tagged=False):
        roles = ('controller', 'l2tp', 'xauth', 'application')
        configs = {role: json.dumps({'architecture': 'arm64', 'os': 'linux', 'role': role}).encode() for role in roles}
        images = {role: {'id': 'sha256:' + hashlib.sha256(config).hexdigest()} for role, config in configs.items()}
        with tarfile.open(root / 'images.tar', 'w') as tar:
            entries = []
            for role, raw in configs.items():
                name = images[role]['id'][7:] + '.json'
                info = tarfile.TarInfo(name)
                info.size = len(raw)
                tar.addfile(info, io.BytesIO(raw))
                entries.append({'Config': name, 'RepoTags': ['legacy:existing'] if tagged else None, 'Layers': []})
            raw = json.dumps(entries).encode()
            info = tarfile.TarInfo('manifest.json')
            info.size = len(raw)
            tar.addfile(info, io.BytesIO(raw))
        data = {'schema_version': 1, 'os': 'linux', 'arch': 'arm64', 'version': 'v0.1.0-rc.1', 'revision': 'a' * 40,
                'binary_version': 'v0.1.0-rc.1+git.' + 'a' * 12, 'images': images, 'archive_sha256': builder.digest(root / 'images.tar')}
        (root / 'release.json').write_text(json.dumps(data))
        return data

    def test_tampering_fails_before_any_docker_command(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            self.bundle(root)
            with (root / 'images.tar').open('ab') as stream:
                stream.write(b'corruption')
            with patch.object(loader, 'docker') as docker:
                with self.assertRaisesRegex(loader.InstallError, 'checksum'):
                    loader.install(root)
                docker.assert_not_called()

    def test_tagged_archive_cannot_replace_legacy_image_tags(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            self.bundle(root, tagged=True)
            with patch.object(loader, 'docker') as docker:
                with self.assertRaisesRegex(loader.InstallError, 'tagged'):
                    loader.install(root)
                docker.assert_not_called()

    def test_wrong_vm_architecture_fails_before_loading_images(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            self.bundle(root)
            with patch.object(loader, 'docker', return_value=json.dumps({'OSType': 'linux', 'Architecture': 'x86_64'})) as docker:
                with self.assertRaisesRegex(loader.InstallError, 'architecture'):
                    loader.install(root)
                self.assertEqual([call.args[0] for call in docker.call_args_list], ['info'])

    def test_archive_image_identity_is_verified_before_load(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            data = self.bundle(root)
            data['images']['l2tp']['id'] = 'sha256:' + 'f' * 64
            (root / 'release.json').write_text(json.dumps(data))
            with self.assertRaisesRegex(loader.InstallError, 'identity'):
                loader.verify(root)


if __name__ == '__main__':
    unittest.main()
