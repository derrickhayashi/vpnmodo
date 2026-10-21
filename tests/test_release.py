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
            (source / 'extra-runtime-file').write_text('synthetic private material')
            exported = Path(directory) / 'exported'
            exported.mkdir()
            copied, _ = builder.source_snapshot(source, exported, 'v0.1.0-rc.1')
            self.assertFalse((copied / 'extra-runtime-file').exists())
            (source / 'source').write_text('changed exported source')
            bad = Path(directory) / 'tampered'
            bad.mkdir()
            with self.assertRaisesRegex(RuntimeError, 'modified'):
                builder.source_snapshot(source, bad, 'v0.1.0-rc.1')
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

    def bundle(self, root, tagged=False, ikev2=False):
        roles = ('controller', 'l2tp', 'xauth', 'application') + (('ikev2',) if ikev2 else ())
        configs = {role: json.dumps({'architecture': 'arm64', 'os': 'linux', 'role': role}).encode() for role in roles}
        images = {role: {'id': 'sha256:' + hashlib.sha256(config).hexdigest(), 'tag': 'vpnmodo-' + role + ':fixture'} for role, config in configs.items()}
        with tarfile.open(root / 'export.tar', 'w') as tar:
            entries = []
            for role, raw in configs.items():
                name = images[role]['id'][7:] + '.json'
                info = tarfile.TarInfo(name)
                info.size = len(raw)
                tar.addfile(info, io.BytesIO(raw))
                entries.append({'Config': name, 'RepoTags': None, 'Layers': []})
            raw = json.dumps(entries).encode()
            info = tarfile.TarInfo('manifest.json')
            info.size = len(raw)
            tar.addfile(info, io.BytesIO(raw))
        loader.normalize_archive(root / 'export.tar', root / 'images.tar', images)
        if tagged:
            with tarfile.open(root / 'images.tar') as src, tarfile.open(root / 'foreign.tar', 'w') as dst:
                for member in src.getmembers():
                    raw = src.extractfile(member).read()
                    if member.name == 'manifest.json':
                        entries = json.loads(raw)
                        entries[0]['RepoTags'] = ['legacy:existing']
                        raw = json.dumps(entries).encode()
                        member.size = len(raw)
                    dst.addfile(member, io.BytesIO(raw))
            (root / 'foreign.tar').replace(root / 'images.tar')
        data = {'schema_version': 2, 'os': 'linux', 'arch': 'arm64', 'version': 'v0.1.0-rc.1', 'revision': 'a' * 40,
                'binary_version': 'v0.1.0-rc.1+git.' + 'a' * 12, 'images': images, 'archive_sha256': builder.digest(root / 'images.tar')}
        (root / 'release.json').write_text(json.dumps(data))
        return data

    def test_ikev2_offline_archive_validates_all_five_images(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            self.bundle(root, ikev2=True)
            data = loader.verify(root)
            self.assertEqual(set(data['images']), {'controller', 'l2tp', 'xauth', 'ikev2', 'application'})

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
                with self.assertRaisesRegex(loader.InstallError, 'tag'):
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
            data['images']['l2tp']['config_digest'] = 'sha256:' + 'f' * 64
            (root / 'release.json').write_text(json.dumps(data))
            with self.assertRaises(loader.InstallError):
                loader.verify(root)


    def test_content_reference_collision_is_refused_before_load(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            self.bundle(root)
            with patch.object(loader, 'docker', return_value=json.dumps({'OSType': 'linux', 'Architecture': 'aarch64'})) as docker, patch.object(loader, 'inspect_optional', return_value={'Id': 'sha256:' + 'f' * 64}):
                with self.assertRaisesRegex(loader.InstallError, 'different content'):
                    loader.install(root)
                self.assertNotIn('load', [call.args[0] for call in docker.call_args_list])

    def test_loaded_ids_are_resolved_for_both_image_stores(self):
        from contextlib import redirect_stdout
        for kind in ('config_digest', 'manifest_digest'):
            with self.subTest(store=kind), tempfile.TemporaryDirectory() as directory:
                root = Path(directory)
                data = self.bundle(root)
                def docker(*args):
                    if args[0] == 'info':
                        return json.dumps({'OSType': 'linux', 'Architecture': 'aarch64'})
                    if args[0] == 'image':
                        image = next(i for i in data['images'].values() if i['ref'] == args[2])
                        return json.dumps([{'Id': image[kind], 'Architecture': 'arm64', 'Os': 'linux'}])
                    if args[0] == 'run':
                        return data['binary_version']
                    return ''
                result = io.StringIO()
                with patch.object(loader, 'docker', side_effect=docker), patch.object(loader, 'inspect_optional', return_value=None), redirect_stdout(result):
                    loader.install(root)
                installed = json.loads(result.getvalue())
                self.assertEqual(installed['controller_image'], data['images']['controller'][kind])
                self.assertEqual(installed['backend_images']['xauth'], data['images']['xauth'][kind])


if __name__ == '__main__':
    unittest.main()
