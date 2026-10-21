"""Publication and promotion preserve one verified build without network access."""
import copy
import hashlib
import importlib.util
import json
import os
from pathlib import Path
import shutil
import subprocess
import tarfile
import tempfile
import unittest
from unittest.mock import patch
import io

ROOT = Path(__file__).resolve().parents[1]
SPEC = importlib.util.spec_from_file_location('release_pipeline', ROOT / 'scripts/release-pipeline.py')
pipeline = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(pipeline)
VERSION = 'v0.1.42'
REVISION = 'a' * 40
RUN = 42


def archive(path, files):
    with tarfile.open(path, 'w:gz') as tar:
        for name, raw in files.items():
            member = tarfile.TarInfo(name)
            member.size = len(raw)
            tar.addfile(member, io.BytesIO(raw))


def assets(directory):
    return [dict(id=i, name=p.name, state='uploaded', size=p.stat().st_size,
                 digest='sha256:' + pipeline.sha256(p), download_count=0)
            for i, p in enumerate(sorted(directory.iterdir()), 1)]


class FakeGitHub:
    def __init__(self, directory):
        self.directory = directory
        self.calls = []
        self.release = dict(id=99, tag_name=VERSION, target_commitish=REVISION,
            draft=False, prerelease=True, body=pipeline.identity(VERSION, REVISION, RUN), assets=assets(directory))
        self.tag = {'object': dict(type='commit', sha=REVISION, url='https://api.github.com/synthetic')}
        self.latest = None
        self.run = dict(id=RUN, workflow_id=123, repository=dict(full_name=pipeline.REPOSITORY),
            head_branch='main', head_sha=REVISION, event='push', status='completed', conclusion='success')
        self.docker_result = 0

    def api(self, path, method='GET', data=None, missing=False):
        self.calls.append((path, method, copy.deepcopy(data)))
        if method == 'POST' and path == 'releases':
            self.release = dict(data, id=99, assets=[])
            return copy.deepcopy(self.release)
        if method == 'POST' and path == 'git/refs':
            self.tag = {'object': dict(type='commit', sha=data['sha'])}
            return copy.deepcopy(self.tag)
        if method == 'PATCH':
            self.release.update(data)
            return copy.deepcopy(self.release)
        if path.startswith('releases/tags/'):
            return copy.deepcopy(self.release)
        if path.startswith('git/ref/'):
            return copy.deepcopy(self.tag)
        if path == 'releases/latest':
            return copy.deepcopy(self.latest)
        if path.startswith('actions/runs/'):
            return copy.deepcopy(self.run)
        if path == 'actions/workflows/release.yml':
            return {'id': 123}
        raise AssertionError(path)

    def download(self, asset, destination):
        shutil.copyfile(self.directory / asset['name'], destination)
        # Downloads legitimately change these API fields during promotion.
        for item in self.release['assets']:
            if item['id'] == asset['id']:
                item['download_count'] += 1

    def command(self, args, **kwargs):
        if args[:3] == ['gh', 'release', 'upload']:
            self.release['assets'] = assets(self.directory)
            return subprocess.CompletedProcess(args, 0)
        if args[0] == 'docker' and args[3:5] == ['manifest', 'inspect']:
            return subprocess.CompletedProcess(args, self.docker_result)
        raise AssertionError(args)

    def mutations(self):
        return [call for call in self.calls if call[1] != 'GET']


class PipelineTests(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.addCleanup(self.temp.cleanup)
        self.root = Path(self.temp.name)
        self.cli, self.runtime = self.root / 'cli', self.root / 'runtime'
        self.cli.mkdir()
        self.runtime.mkdir()
        identity = VERSION + '+git.' + REVISION[:12]
        registry = dict(schema_version=1, version=VERSION, binary_version=identity,
            os='linux', arch='arm64', images={role: f"ghcr.io/{pipeline.REPOSITORY}/{'vpntools' if role == 'application' else role}@sha256:" + 'b' * 64 for role in pipeline.ROLES})
        for folder in (self.cli, self.runtime):
            (folder / 'registry-release.json').write_text(json.dumps(registry))
        for arch in ('arm64', 'amd64'):
            raw = ('synthetic binary ' + arch).encode()
            (self.cli / ('vpnmodo-darwin-' + arch)).write_bytes(raw)
            metadata = dict(version=VERSION, revision=REVISION, binary_version=identity,
                os='darwin', arch=arch, binary_sha256=hashlib.sha256(raw).hexdigest())
            archive(self.cli / f'vpnmodo-{VERSION}-darwin-{arch}.tar.gz', {
                'vpnmodo': raw, 'release.json': json.dumps(metadata).encode(),
                'install.sh': b'synthetic installer', 'README.txt': b'synthetic readme'})
        (self.cli / f'vpnmodo-{VERSION}-source.tar.gz').write_bytes(b'synthetic source archive')
        (self.cli / 'SHA256SUMS-cli').write_text('synthetic original CLI checksums\n')
        for name in (f'vpnmodo-{VERSION}-linux-arm64-images.tar.gz', 'install-vm.py', 'lima.yaml', 'SHA256SUMS-linux-arm64'):
            (self.runtime / name).write_bytes(b'synthetic runtime artifact')
        spec = importlib.util.spec_from_file_location('upgrade_builder', ROOT / 'scripts/build-upgrade-manifest.py')
        builder = importlib.util.module_from_spec(spec)
        spec.loader.exec_module(builder)
        (self.cli / 'upgrade-darwin-arm64.json').write_text(json.dumps(builder.build(self.cli, self.runtime / 'registry-release.json')))
        self.bundle = self.root / 'release'
        pipeline.assemble([self.cli, self.runtime], self.bundle, VERSION, REVISION, RUN)
        self.api = FakeGitHub(self.bundle)

    def checksums(self):
        (self.bundle / 'SHA256SUMS').write_text(''.join(pipeline.sha256(p) + '  ' + p.name + '\n'
            for p in sorted(self.bundle.iterdir()) if p.name != 'SHA256SUMS'))

    def promote(self, confirmed=True):
        with patch.object(pipeline.subprocess, 'run', side_effect=self.api.command):
            pipeline.promote(self.api, VERSION, confirmed)

    def test_assembly_preserves_matching_jobs_and_covers_every_asset(self):
        self.assertEqual(len(list(self.bundle.iterdir())), 14)
        pipeline.verify_bundle(self.bundle, VERSION, REVISION, RUN)
        self.assertEqual((self.cli / 'registry-release.json').read_bytes(), (self.bundle / 'registry-release.json').read_bytes())

    def test_build_jobs_must_agree_on_shared_manifest(self):
        (self.runtime / 'registry-release.json').write_text('{}')
        with self.assertRaisesRegex(pipeline.ReleaseError, 'disagree'):
            pipeline.assemble([self.cli, self.runtime], self.root / 'bad', VERSION, REVISION, RUN)

    def test_missing_or_unexpected_artifacts_refuse_assembly(self):
        (self.cli / 'unexpected').write_text('synthetic')
        with self.assertRaisesRegex(pipeline.ReleaseError, 'unexpected'):
            pipeline.assemble([self.cli, self.runtime], self.root / 'bad', VERSION, REVISION, RUN)
        (self.cli / 'unexpected').unlink()
        (self.cli / 'vpnmodo-darwin-arm64').unlink()
        with self.assertRaisesRegex(pipeline.ReleaseError, 'asset set'):
            pipeline.assemble([self.cli, self.runtime], self.root / 'missing', VERSION, REVISION, RUN)

    def test_tampering_and_checksum_duplicates_fail_before_publication(self):
        (self.bundle / 'vpnmodo-darwin-arm64').write_text('changed')
        with self.assertRaisesRegex(pipeline.ReleaseError, 'checksum mismatch'):
            pipeline.publish(self.api, self.bundle, VERSION, REVISION, RUN)
        self.assertEqual(self.api.calls, [])
        self.checksums()
        with (self.bundle / 'SHA256SUMS').open('a') as output:
            output.write((self.bundle / 'SHA256SUMS').read_text().splitlines()[0] + '\n')
        with self.assertRaisesRegex(pipeline.ReleaseError, 'duplicate'):
            pipeline.verify_bundle(self.bundle, VERSION, REVISION, RUN)

    def test_runtime_requires_complete_same_version_immutable_repository_images(self):
        path = self.bundle / 'registry-release.json'
        original = json.loads(path.read_text())
        variants = [dict(original, arch='amd64'), dict(original, version='v0.1.43'),
            dict(original, images=dict(original['images'], application=f'ghcr.io/{pipeline.REPOSITORY}/application@sha256:' + 'b' * 64)),
            dict(original, images=dict(original['images'], application=f'ghcr.io/{pipeline.REPOSITORY}/vpntools:latest')),
            dict(original, images={role: ref for role, ref in original['images'].items() if role != 'ikev2'}),
            dict(original, images=dict(original['images'], controller='ghcr.io/other/project/controller@sha256:' + 'b' * 64)),
            dict(original, images=dict(original['images'], controller=f'ghcr.io/{pipeline.REPOSITORY}/controller:latest'))]
        for bad in variants:
            path.write_text(json.dumps(bad))
            self.checksums()
            with self.subTest(bad=bad), self.assertRaises(pipeline.ReleaseError):
                pipeline.verify_bundle(self.bundle, VERSION, REVISION, RUN)

    def test_archive_full_source_revision_is_checked_not_only_short_identity(self):
        path = self.bundle / f'vpnmodo-{VERSION}-darwin-amd64.tar.gz'
        with tarfile.open(path) as package:
            files = {member.name: package.extractfile(member).read() for member in package.getmembers()}
        metadata = json.loads(files['release.json'])
        metadata['revision'] = REVISION[:12] + 'c' * 28
        files['release.json'] = json.dumps(metadata).encode()
        archive(path, files)
        self.checksums()
        with self.assertRaisesRegex(pipeline.ReleaseError, 'source identity'):
            pipeline.verify_bundle(self.bundle, VERSION, REVISION, RUN)

    def test_published_versions_and_other_run_drafts_cannot_be_overwritten(self):
        with self.assertRaisesRegex(pipeline.ReleaseError, 'already published'):
            pipeline.check_existing(self.api, VERSION, REVISION, RUN)
        self.api.release.update(draft=True, body=pipeline.identity(VERSION, REVISION, RUN + 1))
        with self.assertRaisesRegex(pipeline.ReleaseError, 'different source or build'):
            pipeline.check_existing(self.api, VERSION, REVISION, RUN)
        self.assertEqual(self.api.mutations(), [])

    def test_retry_can_resume_its_own_draft_but_not_an_external_tag(self):
        self.api.release['draft'] = True
        self.assertIsNotNone(pipeline.check_existing(self.api, VERSION, REVISION, RUN))
        self.api.release = None
        with self.assertRaisesRegex(pipeline.ReleaseError, 'tag already exists'):
            pipeline.check_existing(self.api, VERSION, REVISION, RUN)

    def test_publication_uploads_to_draft_and_never_sets_latest(self):
        self.api.release = self.api.tag = None
        with patch.object(pipeline.subprocess, 'run', side_effect=self.api.command):
            pipeline.publish(self.api, self.bundle, VERSION, REVISION, RUN)
        self.assertFalse(self.api.release['draft'])
        self.assertTrue(self.api.release['prerelease'])
        self.assertEqual(self.api.release['make_latest'], 'false')
        self.assertEqual(self.api.tag['object']['sha'], REVISION)
        self.assertEqual(self.api.mutations()[-1][2], dict(draft=False, prerelease=True, make_latest='false'))

    def test_failed_upload_leaves_only_a_resumable_draft(self):
        self.api.release = self.api.tag = None
        with patch.object(pipeline.subprocess, 'run', return_value=subprocess.CompletedProcess([], 1)):
            with self.assertRaisesRegex(pipeline.ReleaseError, 'upload failed'):
                pipeline.publish(self.api, self.bundle, VERSION, REVISION, RUN)
        self.assertTrue(self.api.release['draft'])
        self.assertFalse(any(call[1] == 'PATCH' for call in self.api.calls))
        with patch.object(pipeline.subprocess, 'run', side_effect=self.api.command):
            pipeline.publish(self.api, self.bundle, VERSION, REVISION, RUN)
        self.assertFalse(self.api.release['draft'])

    def test_promotion_changes_only_channel_flags_and_keeps_assets_and_tags(self):
        before = [(a['name'], a['digest']) for a in self.api.release['assets']]
        self.promote()
        self.assertEqual(self.api.mutations(), [('releases/99', 'PATCH', dict(prerelease=False, make_latest='true'))])
        self.assertEqual(before, [(a['name'], a['digest']) for a in self.api.release['assets']])
        self.assertEqual(self.api.tag['object']['sha'], REVISION)

    def test_acceptance_confirmation_is_required_before_any_api_call(self):
        with self.assertRaisesRegex(pipeline.ReleaseError, 'confirm local protocol'):
            self.promote(False)
        self.assertEqual(self.api.calls, [])

    def test_failed_incomplete_foreign_or_wrong_source_runs_block_promotion(self):
        original = copy.deepcopy(self.api.run)
        variants = [dict(original, conclusion='failure'), dict(original, status='in_progress'),
            dict(original, head_sha='d' * 40), dict(original, head_branch='other'),
            dict(original, event='pull_request'), dict(original, workflow_id=124),
            dict(original, repository=dict(full_name='other/project'))]
        for run in variants:
            self.api.run = run
            with self.subTest(run=run), self.assertRaisesRegex(pipeline.ReleaseError, 'successful main-branch'):
                self.promote()
        self.assertEqual(self.api.mutations(), [])

    def test_tag_retargeting_missing_images_and_latest_downgrade_block_promotion(self):
        self.api.tag['object']['sha'] = 'c' * 40
        with self.assertRaisesRegex(pipeline.ReleaseError, 'tag differs'):
            self.promote()
        self.api.tag['object']['sha'] = REVISION
        self.api.docker_result = 1
        with self.assertRaisesRegex(pipeline.ReleaseError, 'anonymously accessible'):
            self.promote()
        self.api.docker_result = 0
        self.api.latest = dict(tag_name='v0.1.43')
        with self.assertRaisesRegex(pipeline.ReleaseError, 'backwards'):
            self.promote()
        self.assertEqual(self.api.mutations(), [])

    def test_registry_checks_use_no_publisher_credentials(self):
        registry = pipeline.runtime_manifest(self.bundle, VERSION, REVISION)
        with patch.object(pipeline.subprocess, 'run', side_effect=self.api.command) as command:
            pipeline.check_public_images(registry)
        self.assertEqual(command.call_count, 5)
        for call in command.call_args_list:
            args = call.args[0]
            self.assertEqual(args[:2], ['docker', '--config'])
            self.assertNotIn('login', args)
            self.assertFalse(Path(args[2]).exists(), 'temporary credential directory retained')

    def test_missing_assets_block_promotion(self):
        self.api.release['assets'].pop()
        with self.assertRaisesRegex(pipeline.ReleaseError, 'missing or duplicate'):
            self.promote()
        self.assertEqual(self.api.mutations(), [])

    def test_workflow_context_refuses_forks_branches_and_pull_requests(self):
        env = dict(GITHUB_REPOSITORY=pipeline.REPOSITORY, GITHUB_REF='refs/heads/main',
            GITHUB_EVENT_NAME='push', GITHUB_SHA=REVISION, GITHUB_RUN_ID=str(RUN))
        with patch.dict(os.environ, env, clear=True):
            self.assertEqual(pipeline.context(), (REVISION, RUN))
        for overrides in (dict(GITHUB_REPOSITORY='other/project'), dict(GITHUB_REF='refs/heads/other'), dict(GITHUB_EVENT_NAME='pull_request')):
            with patch.dict(os.environ, dict(env, **overrides), clear=True), self.assertRaises(pipeline.ReleaseError):
                pipeline.context()
        with patch.dict(os.environ, env, clear=True), self.assertRaises(pipeline.ReleaseError):
            pipeline.context(promotion=True)

    def test_github_asset_download_checks_api_digest(self):
        raw = b'synthetic asset'
        asset = dict(id=1, size=len(raw), digest='sha256:' + hashlib.sha256(raw).hexdigest())
        def download(args, **kwargs):
            kwargs['stdout'].write(raw)
            return subprocess.CompletedProcess(args, 0)
        with patch.object(pipeline.subprocess, 'run', side_effect=download):
            pipeline.GitHub().download(asset, self.root / 'download')
            with self.assertRaisesRegex(pipeline.ReleaseError, 'checksum differs'):
                pipeline.GitHub().download(dict(asset, digest='sha256:' + 'f' * 64), self.root / 'corrupt')

    def test_api_only_treats_404_as_a_missing_release(self):
        api = pipeline.GitHub()
        for code in (403, 500):
            result = subprocess.CompletedProcess([], 1, stdout=b'', stderr=f'HTTP {code}'.encode())
            with patch.object(pipeline.subprocess, 'run', return_value=result), self.assertRaises(pipeline.ReleaseError):
                api.api('releases/tags/' + VERSION, missing=True)
        result = subprocess.CompletedProcess([], 1, stdout=b'', stderr=b'HTTP 404')
        with patch.object(pipeline.subprocess, 'run', return_value=result):
            self.assertIsNone(api.api('releases/tags/' + VERSION, missing=True))

    def test_asset_replacement_during_promotion_prevents_mutation(self):
        original = self.api.download
        def download(asset, destination):
            original(asset, destination)
            self.api.release['assets'][0]['digest'] = 'sha256:' + 'c' * 64
        self.api.download = download
        with self.assertRaisesRegex(pipeline.ReleaseError, 'changed during promotion'):
            self.promote()
        self.assertEqual(self.api.mutations(), [])

    def test_versions_are_permanent_numeric_tags(self):
        for value in ('v0.1.42-rc.1', 'v0.01.1', 'main', 'v0.1.1/other'):
            with self.subTest(version=value), self.assertRaises(pipeline.ReleaseError):
                pipeline.identity(value, REVISION, RUN)


if __name__ == '__main__':
    unittest.main()
