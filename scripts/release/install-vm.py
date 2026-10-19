#!/usr/bin/env python3
"""Verify and load a release bundle; activation is an explicit workstation action."""
import hashlib
import json
from pathlib import Path
import re
import subprocess
import sys
import tarfile


class InstallError(Exception):
    pass


def docker(*args):
    try:
        result = subprocess.run(['docker', '--host', 'unix:///var/run/docker.sock', *args], capture_output=True, timeout=600)
    except (OSError, subprocess.TimeoutExpired):
        raise InstallError('Docker command unavailable or timed out') from None
    if result.returncode:
        raise InstallError('Docker command failed; check Docker access and prerequisites')
    return result.stdout.decode().strip()


def verify(bundle):
    manifest = bundle / 'release.json'
    archive = bundle / 'images.tar'
    if manifest.is_symlink() or archive.is_symlink() or not manifest.is_file() or not archive.is_file():
        raise InstallError('release files must be regular files')
    if manifest.stat().st_size > 16384:
        raise InstallError('release manifest is too large')
    data = json.loads(manifest.read_text())
    if not isinstance(data, dict):
        raise InstallError('invalid release manifest')
    if data.get('schema_version') != 1 or data.get('os') != 'linux' or data.get('arch') not in ('arm64', 'amd64'):
        raise InstallError('unsupported release manifest')
    if not re.fullmatch(r'v[0-9]+\.[0-9]+\.[0-9]+(?:-[0-9A-Za-z]+(?:[.-][0-9A-Za-z]+)*)?', data.get('version', '')):
        raise InstallError('invalid release version')
    if not re.fullmatch(r'[0-9a-f]{40}', data.get('revision', '')):
        raise InstallError('invalid source revision')
    if data.get('binary_version') != data['version'] + '+git.' + data['revision'][:12]:
        raise InstallError('release binary identity does not match')
    images = data.get('images')
    if not isinstance(images, dict) or set(images) != {'controller', 'l2tp', 'xauth', 'application'}:
        raise InstallError('release image set is incomplete')
    for image in images.values():
        if not isinstance(image, dict) or not re.fullmatch(r'sha256:[0-9a-f]{64}', image.get('id', '')):
            raise InstallError('invalid immutable image identity')
    digest = hashlib.sha256()
    with archive.open('rb') as stream:
        for block in iter(lambda: stream.read(1024 * 1024), b''):
            digest.update(block)
    if digest.hexdigest() != data.get('archive_sha256'):
        raise InstallError('image archive checksum mismatch; nothing loaded')
    # Content-only archives must not retag an unrelated deployment's images.
    with tarfile.open(archive, 'r:') as tar:
        member = tar.getmember('manifest.json')
        if member.size > 65536 or not member.isfile():
            raise InstallError('invalid Docker archive manifest')
        entries = json.load(tar.extractfile(member))
        if not isinstance(entries, list) or len(entries) != len(images):
            raise InstallError('archive image set does not match release')
        expected = {image['id'] for image in images.values()}
        found = set()
        for entry in entries:
            if not isinstance(entry, dict) or entry.get('RepoTags'):
                raise InstallError('tagged image archive refused')
            config = tar.getmember(entry['Config'])
            if not config.isfile() or config.size > 1024 * 1024:
                raise InstallError('invalid Docker image configuration')
            raw = tar.extractfile(config).read()
            identity = 'sha256:' + hashlib.sha256(raw).hexdigest()
            if identity not in expected or identity in found:
                raise InstallError('archive image identity does not match release')
            found.add(identity)
            configuration = json.loads(raw)
            if configuration.get('architecture') != data['arch'] or configuration.get('os') != 'linux':
                raise InstallError('archive image architecture does not match release')
        if found != expected:
            raise InstallError('archive image set does not match release')
    return data


def install(bundle):
    data = verify(bundle)
    info = json.loads(docker('info', '--format', '{{json .}}'))
    arch = {'aarch64': 'arm64', 'x86_64': 'amd64', 'arm64': 'arm64', 'amd64': 'amd64'}.get(info.get('Architecture'))
    if info.get('OSType') != 'linux' or arch != data['arch']:
        raise InstallError('bundle architecture does not match the Linux Docker engine')
    docker('compose', 'version')
    # No source build, registry authentication, profile changes or container stop.
    docker('load', '-i', str(bundle / 'images.tar'))
    for role, image in data['images'].items():
        actual = json.loads(docker('image', 'inspect', image['id']))[0]
        if actual.get('Id') != image['id'] or actual.get('Architecture') != data['arch'] or actual.get('Os') != 'linux':
            raise InstallError('loaded image does not match release manifest')
        if role == 'controller':
            env = actual.get('Config', {}).get('Env') or []
            for name, backend in [('L2TP', 'l2tp'), ('XAUTH', 'xauth'), ('APPLICATION', 'application')]:
                if 'VPNMODO_' + name + '_IMAGE=' + data['images'][backend]['id'] not in env:
                    raise InstallError('controller backend image identity does not match release')
    actual = docker('run', '--rm', '--network', 'none', data['images']['controller']['id'], 'version')
    if actual != data['binary_version']:
        raise InstallError('controller version does not match release manifest')
    print(json.dumps({'ok': True, 'version': actual, 'controller_image': data['images']['controller']['id'], 'activated': False}))


def main():
    if len(sys.argv) != 1:
        raise InstallError('run install-vm.py without arguments in its extracted bundle')
    install(Path(__file__).resolve().parent)


if __name__ == '__main__':
    try:
        main()
    except (InstallError, OSError, ValueError, KeyError, TypeError, tarfile.TarError):
        print('VM image installation failed; verify bundle integrity, architecture and Docker prerequisites', file=sys.stderr)
        sys.exit(1)
