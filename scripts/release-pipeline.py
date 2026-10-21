#!/usr/bin/env python3
"""Assemble, publish and promote matching vpnmodo release artifacts."""
import argparse
import hashlib
import importlib.util
import json
import os
from pathlib import Path
import re
import shutil
import subprocess
import tempfile
import tarfile

REPOSITORY = 'derrickhayashi/vpnmodo'
WORKFLOW = 'release.yml'
VERSION = re.compile(r'v(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)')
SHA = re.compile(r'[a-f0-9]{40}')
DIGEST = re.compile(r'sha256:[a-f0-9]{64}')
ROLES = {'controller', 'l2tp', 'xauth', 'ikev2', 'application'}
MAX_ASSET = 2 * 1024 * 1024 * 1024


class ReleaseError(Exception):
    pass


def require(condition, reason):
    if not condition:
        raise ReleaseError(reason)


def sha256(path):
    digest = hashlib.sha256()
    with path.open('rb') as stream:
        for block in iter(lambda: stream.read(1024 * 1024), b''):
            digest.update(block)
    return digest.hexdigest()


def read_json(path):
    require(path.stat().st_size <= 16384, 'release metadata exceeds size limit')
    def unique(pairs):
        value = {}
        for key, item in pairs:
            require(key not in value, 'duplicate release metadata field')
            value[key] = item
        return value
    return json.loads(path.read_text(), object_pairs_hook=unique)


def identity(version, revision, run_id):
    require(isinstance(version, str) and VERSION.fullmatch(version), 'use a permanent numeric release version such as v0.1.1')
    require(isinstance(revision, str) and SHA.fullmatch(revision), 'invalid release source revision')
    require(type(run_id) is int and run_id > 0, 'invalid build run identity')
    return f'<!-- vpnmodo-build:{run_id}:{revision} -->'


def context(promotion=False):
    require(os.environ.get('GITHUB_REPOSITORY') == REPOSITORY, 'release publishing is restricted to ' + REPOSITORY)
    require(os.environ.get('GITHUB_REF') == 'refs/heads/main', 'run this workflow from main')
    events = ('workflow_dispatch',) if promotion else ('push', 'workflow_dispatch')
    require(os.environ.get('GITHUB_EVENT_NAME') in events, 'publishing is unavailable for this event')
    revision = os.environ.get('GITHUB_SHA', '')
    run_id = int(os.environ.get('GITHUB_RUN_ID', '0'))
    require(SHA.fullmatch(revision) and run_id > 0, 'invalid workflow context')
    return revision, run_id


class GitHub:
    def api(self, path, method='GET', data=None, missing=False):
        args = ['gh', 'api', '--method', method, 'repos/' + REPOSITORY + '/' + path]
        if data is not None:
            args += ['--input', '-']
        result = subprocess.run(args, input=None if data is None else json.dumps(data).encode(), capture_output=True, timeout=120)
        if result.returncode:
            if missing and b'HTTP 404' in result.stderr:
                return None
            raise ReleaseError('GitHub API request failed: ' + method + ' ' + path)
        return json.loads(result.stdout) if result.stdout else None

    def download(self, asset, destination):
        require(type(asset.get('id')) is int and asset['id'] > 0, 'invalid release asset identity')
        require(type(asset.get('size')) is int and 0 < asset['size'] <= MAX_ASSET, 'release asset is empty or too large')
        require(isinstance(asset.get('digest'), str) and DIGEST.fullmatch(asset['digest']), 'release asset lacks a GitHub SHA-256 digest')
        with destination.open('wb') as output:
            result = subprocess.run(['gh', 'api', '-H', 'Accept: application/octet-stream',
                f'repos/{REPOSITORY}/releases/assets/{asset["id"]}'], stdout=output, stderr=subprocess.DEVNULL, timeout=600)
        require(result.returncode == 0 and destination.stat().st_size == asset['size'], 'release asset download failed')
        require('sha256:' + sha256(destination) == asset['digest'], 'release asset checksum differs from GitHub')


def check_existing(api, version, revision, run_id):
    marker = identity(version, revision, run_id)
    release = api.api('releases/tags/' + version, missing=True)
    tag = api.api('git/ref/tags/' + version, missing=True)
    if release:
        require(release.get('draft') is True and release.get('prerelease') is True,
                'version is already published; use a new workflow run instead of overwriting it')
        require(release.get('tag_name') == version and release.get('target_commitish') == revision
                and marker in (release.get('body') or ''), 'draft belongs to a different source or build run')
    else:
        require(tag is None, 'version tag already exists outside this build')
    if tag:
        require(tag.get('object', {}).get('sha') == revision and tag['object'].get('type') == 'commit',
                'version tag does not point directly to this source commit')
    return release


def filenames(version):
    return {
        'vpnmodo-darwin-arm64', 'vpnmodo-darwin-amd64',
        f'vpnmodo-{version}-darwin-arm64.tar.gz', f'vpnmodo-{version}-darwin-amd64.tar.gz',
        f'vpnmodo-{version}-source.tar.gz', f'vpnmodo-{version}-linux-arm64-images.tar.gz',
        'registry-release.json', 'install-vm.py', 'lima.yaml', 'SHA256SUMS-cli',
        'SHA256SUMS-linux-arm64', 'upgrade-darwin-arm64.json', 'build-metadata.json', 'SHA256SUMS',
    }


def runtime_manifest(directory, version, revision):
    registry = read_json(directory / 'registry-release.json')
    binary_version = version + '+git.' + revision[:12]
    require(registry.get('schema_version') == 1 and registry.get('version') == version
        and registry.get('binary_version') == binary_version and registry.get('os') == 'linux'
        and registry.get('arch') == 'arm64' and isinstance(registry.get('images'), dict)
        and set(registry['images']) == ROLES, 'runtime manifest does not match the complete ARM64 release')
    for role, ref in registry['images'].items():
        prefix = f'ghcr.io/{REPOSITORY}/{role}@'
        require(isinstance(ref, str) and ref.startswith(prefix) and DIGEST.fullmatch(ref[len(prefix):]),
                'runtime manifest contains a mutable or foreign image')
    return registry


def check_public_images(registry):
    # An empty Docker config excludes the publisher's credentials and helpers.
    with tempfile.TemporaryDirectory(prefix='vpnmodo-registry-') as config:
        for role, ref in registry['images'].items():
            result = subprocess.run(['docker', '--config', config, 'manifest', 'inspect', ref],
                stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL, timeout=120)
            require(result.returncode == 0, 'pinned ' + role + ' image is not anonymously accessible; check its GHCR package visibility and network access')


def check_metadata(directory, version, revision, run_id):
    identity(version, revision, run_id)
    metadata = read_json(directory / 'build-metadata.json')
    require(metadata == dict(schema_version=1, repository=REPOSITORY, version=version,
        revision=revision, run_id=run_id, workflow=WORKFLOW), 'build metadata does not match the release')
    registry = runtime_manifest(directory, version, revision)
    binary_version = version + '+git.' + revision[:12]
    for arch in ('arm64', 'amd64'):
        binary = directory / ('vpnmodo-darwin-' + arch)
        require(0 < binary.stat().st_size <= 64 * 1024 * 1024, 'invalid CLI binary size')
        with tarfile.open(directory / f'vpnmodo-{version}-darwin-{arch}.tar.gz') as archive:
            members = archive.getmembers()
            require({m.name for m in members} == {'vpnmodo', 'install.sh', 'release.json', 'README.txt'}
                and len(members) == 4 and all(m.isfile() for m in members), 'invalid CLI archive members')
            member = archive.getmember('release.json')
            require(member.size <= 16384, 'CLI archive metadata is too large')
            cli = json.load(archive.extractfile(member))
        require(cli.get('revision') == revision and cli.get('version') == version
            and cli.get('binary_version') == binary_version and cli.get('os') == 'darwin'
            and cli.get('arch') == arch and cli.get('binary_sha256') == sha256(binary), 'CLI archive source identity differs')
    # Recompute the upgrade descriptor from the binary, archive and runtime data.
    source = Path(__file__).with_name('build-upgrade-manifest.py')
    spec = importlib.util.spec_from_file_location('upgrade_manifest', source)
    builder = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(builder)
    require(read_json(directory / 'upgrade-darwin-arm64.json') == builder.build(directory, directory / 'registry-release.json'),
            'upgrade descriptor differs from the built artifacts')
    return registry


def verify_bundle(directory, version, revision, run_id):
    expected = filenames(version)
    require({p.name for p in directory.iterdir()} == expected
        and all(p.is_file() and not p.is_symlink() for p in directory.iterdir()), 'release asset set is incomplete or unexpected')
    checksums = {}
    for line in (directory / 'SHA256SUMS').read_text().splitlines():
        match = re.fullmatch(r'([a-f0-9]{64})  ([a-zA-Z0-9._-]+)', line)
        require(match is not None and match[2] not in checksums, 'invalid or duplicate checksum entry')
        checksums[match[2]] = match[1]
    require(set(checksums) == expected - {'SHA256SUMS'}, 'checksums do not cover the release asset set')
    for name, digest in checksums.items():
        require(sha256(directory / name) == digest, 'release checksum mismatch: ' + name)
    return check_metadata(directory, version, revision, run_id)


def assemble(inputs, output, version, revision, run_id):
    identity(version, revision, run_id)
    require(not output.exists(), 'release output must be a new directory')
    output.mkdir(parents=True)
    for folder in inputs:
        for source in sorted(folder.iterdir()):
            require(source.is_file() and not source.is_symlink(), 'unexpected build artifact path')
            require(source.name in filenames(version) - {'SHA256SUMS', 'build-metadata.json'}, 'unexpected build artifact name')
            destination = output / source.name
            if destination.exists():
                require(sha256(source) == sha256(destination), 'build jobs disagree on shared release metadata')
            else:
                shutil.copyfile(source, destination)
    (output / 'build-metadata.json').write_text(json.dumps(dict(schema_version=1, repository=REPOSITORY,
        version=version, revision=revision, run_id=run_id, workflow=WORKFLOW), indent=2) + '\n')
    lines = [sha256(path) + '  ' + path.name + '\n' for path in sorted(output.iterdir())]
    (output / 'SHA256SUMS').write_text(''.join(lines))
    verify_bundle(output, version, revision, run_id)


def publish(api, directory, version, revision, run_id):
    marker = identity(version, revision, run_id)
    verify_bundle(directory, version, revision, run_id)
    release = check_existing(api, version, revision, run_id)
    if release is None:
        notes = (f'{marker}\nBuilt from `{revision}`.\n\n'
            'This is an automated prerelease. Use `vpnmodo upgrade --prerelease` to opt in, '
            'or select this version with `vpnmodo upgrade --version ' + version + '`.\n\n'
            'Build checks and native ARM64 packaging checks run automatically. VPN protocol acceptance '
            'runs locally and must be confirmed before stable promotion.\n\n'
            f'[Build workflow](https://github.com/{REPOSITORY}/actions/runs/{run_id})')
        release = api.api('releases', 'POST', dict(tag_name=version, target_commitish=revision,
            name=version, body=notes, draft=True, prerelease=True, make_latest='false'))
    tag = api.api('git/ref/tags/' + version, missing=True)
    if tag is None:
        api.api('git/refs', 'POST', dict(ref='refs/tags/' + version, sha=revision))
    else:
        require(tag.get('object', {}).get('sha') == revision and tag['object'].get('type') == 'commit', 'release tag changed during publication')
    # Only this run's unpublished draft can be resumed and have assets replaced.
    check_existing(api, version, revision, run_id)
    result = subprocess.run(['gh', 'release', 'upload', version, '--repo', REPOSITORY, '--clobber',
        *[str(path) for path in sorted(directory.iterdir())]], stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL, timeout=900)
    require(result.returncode == 0, 'release asset upload failed; rerun the failed workflow jobs to resume the draft')
    uploaded = check_existing(api, version, revision, run_id)
    assets = uploaded.get('assets', [])
    require({a.get('name') for a in assets} == filenames(version) and len(assets) == len(filenames(version)), 'uploaded release asset set differs')
    for asset in assets:
        path = directory / asset['name']
        require(asset.get('state') == 'uploaded' and asset.get('size') == path.stat().st_size
            and asset.get('digest') == 'sha256:' + sha256(path), 'uploaded release asset digest differs')
    api.api('releases/' + str(release['id']), 'PATCH', dict(draft=False, prerelease=True, make_latest='false'))


def verify_build_run(api, metadata):
    run = api.api('actions/runs/' + str(metadata['run_id']))
    workflow = api.api('actions/workflows/' + WORKFLOW)
    require(type(workflow.get('id')) is int and workflow['id'] > 0
        and run.get('id') == metadata['run_id'] and run.get('workflow_id') == workflow['id']
        and run.get('repository', {}).get('full_name') == REPOSITORY
        and run.get('head_branch') == 'main' and run.get('head_sha') == metadata['revision']
        and run.get('event') in ('push', 'workflow_dispatch') and run.get('status') == 'completed'
        and run.get('conclusion') == 'success', 'candidate does not have a successful main-branch release build')


def promote(api, version, acceptance_confirmed):
    require(acceptance_confirmed, 'confirm local protocol acceptance before promotion')
    require(VERSION.fullmatch(version), 'use the permanent numeric candidate version')
    release = api.api('releases/tags/' + version)
    require(release.get('tag_name') == version and release.get('draft') is False, 'candidate is not a published release')
    assets = release.get('assets', [])
    require({a.get('name') for a in assets} == filenames(version) and len(assets) == len(filenames(version)), 'candidate has missing or duplicate assets')
    with tempfile.TemporaryDirectory(prefix='vpnmodo-promote-') as temporary:
        directory = Path(temporary)
        for asset in assets:
            api.download(asset, directory / asset['name'])
        metadata = read_json(directory / 'build-metadata.json')
        revision, run_id = metadata.get('revision'), metadata.get('run_id')
        registry = verify_bundle(directory, version, revision, run_id)
        require(identity(version, revision, run_id) in release.get('body', ''), 'release is not owned by the recorded build run')
        tag = api.api('git/ref/tags/' + version)
        require(tag.get('object', {}).get('type') == 'commit' and tag['object'].get('sha') == revision, 'candidate tag differs from built source')
        verify_build_run(api, metadata)
        latest = api.api('releases/latest', missing=True)
        if latest:
            match = re.fullmatch(r'v([0-9]+)\.([0-9]+)\.([0-9]+)(?:-[0-9A-Za-z.-]+)?', latest.get('tag_name', ''))
            require(match is not None and tuple(map(int, VERSION.fullmatch(version).groups())) >= tuple(map(int, match.groups())), 'promotion would move Latest backwards')
        check_public_images(registry)
        # Recheck identity immediately before the only promotion mutation.
        current = api.api('releases/tags/' + version)
        fields = ('id', 'name', 'size', 'digest', 'state')
        fingerprint = lambda items: sorted(tuple(a.get(key) for key in fields) for a in items)
        require(current.get('id') == release.get('id') and fingerprint(current.get('assets', [])) == fingerprint(assets)
            and current.get('body') == release.get('body') and current.get('draft') is False,
            'release changed during promotion checks')
        api.api('releases/' + str(release['id']), 'PATCH', dict(prerelease=False, make_latest='true'))


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('command', choices=('preflight', 'check-images', 'assemble', 'publish', 'promote'))
    parser.add_argument('--version', required=True)
    parser.add_argument('--input', type=Path, action='append', default=[])
    parser.add_argument('--output', type=Path)
    parser.add_argument('--directory', type=Path)
    parser.add_argument('--acceptance-confirmed', action='store_true')
    args = parser.parse_args()
    try:
        revision, run_id = context(args.command == 'promote')
        identity(args.version, revision, run_id)
        api = GitHub()
        if args.command == 'preflight':
            check_existing(api, args.version, revision, run_id)
        elif args.command == 'check-images':
            require(args.directory is not None, 'image checks need a runtime directory')
            check_public_images(runtime_manifest(args.directory, args.version, revision))
        elif args.command == 'assemble':
            require(args.input and args.output is not None, 'assembly needs inputs and output')
            assemble(args.input, args.output, args.version, revision, run_id)
        elif args.command == 'publish':
            require(args.directory is not None, 'publication needs a release directory')
            publish(api, args.directory, args.version, revision, run_id)
        else:
            promote(api, args.version, args.acceptance_confirmed)
        print('Release ' + args.command + ' completed: ' + args.version)
    except (ReleaseError, OSError, ValueError, KeyError, TypeError, tarfile.TarError, subprocess.SubprocessError) as error:
        parser.exit(1, 'release operation failed: ' + str(error) + '\n')


if __name__ == '__main__':
    main()
