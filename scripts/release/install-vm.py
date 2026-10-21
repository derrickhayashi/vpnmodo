#!/usr/bin/env python3
"""Load verified content-addressed images and report destination-local IDs."""
import hashlib
import io
import json
from pathlib import Path
import re
import subprocess
import sys
import tarfile

ROLES = {'controller', 'l2tp', 'xauth', 'application'}
DIGEST = re.compile(r'sha256:[0-9a-f]{64}')
OCI = 'application/vnd.oci.image.'
# Keep Docker's template literal intact when Lima embeds this file as data.
DOCKER_JSON_FORMAT = '{' + '{json .}' + '}'


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


def blob_path(digest):
    if not isinstance(digest, str) or not DIGEST.fullmatch(digest):
        raise InstallError('invalid content digest')
    return 'blobs/sha256/' + digest[7:]


def raw_json(value):
    return json.dumps(value, sort_keys=True, separators=(',', ':')).encode()


def add_bytes(tar, name, raw):
    entry = tarfile.TarInfo(name)
    entry.size = len(raw)
    entry.mode = 0o644
    tar.addfile(entry, io.BytesIO(raw))


def read_small(tar, name, limit=1024 * 1024):
    try:
        member = tar.getmember(name)
    except KeyError:
        raise InstallError('missing image metadata') from None
    if not member.isfile() or member.size > limit:
        raise InstallError('invalid image metadata')
    return tar.extractfile(member).read()


def normalize_archive(source, destination, images):
    """Give each image an OCI descriptor and a collision-resistant private tag.

    Config IDs used by classic Docker are not portable containerd-store IDs.
    Both archive formats must name all images; untagged multi-image exports can
    otherwise expose only one OCI descriptor when loaded by containerd.
    """
    with tarfile.open(source) as src:
        entries = json.loads(read_small(src, 'manifest.json'))
        if not isinstance(entries, list) or len(entries) != len(images):
            raise InstallError('unexpected image export set')
        used, descriptors, extra = set(), [], {}
        hashes = {}
        def content(name):
            if name not in hashes:
                h = hashlib.sha256()
                member = src.getmember(name)
                if not member.isfile():
                    raise InstallError('nonregular image blob')
                with src.extractfile(member) as stream:
                    for chunk in iter(lambda: stream.read(1024 * 1024), b''):
                        h.update(chunk)
                digest = 'sha256:' + h.hexdigest()
                hashes[name] = (digest, member.size)
                extra[blob_path(digest)] = name
            return hashes[name]
        for entry in entries:
            config, size = content(entry['Config'])
            tags = {tag.removeprefix('docker.io/') for tag in (entry.get('RepoTags') or [])}
            candidates = [role for role, image in images.items() if image['tag'] in tags or image['id'] == config]
            if len(candidates) != 1 or candidates[0] in used:
                raise InstallError('image export identity is ambiguous')
            role = candidates[0]
            used.add(role)
            ref = 'vpnmodo-offline/' + role + ':sha256-' + config[7:]
            layers = []
            for name in entry['Layers']:
                digest, length = content(name)
                with src.extractfile(name) as layer:
                    magic = layer.read(4)
                compression = '+gzip' if magic.startswith(b'\x1f\x8b') else '+zstd' if magic == b'\x28\xb5\x2f\xfd' else ''
                layers.append({'mediaType': OCI + 'layer.v1.tar' + compression, 'digest': digest, 'size': length})
            manifest = {'schemaVersion': 2, 'mediaType': OCI + 'manifest.v1+json', 'config': {'mediaType': OCI + 'config.v1+json', 'digest': config, 'size': size}, 'layers': layers}
            raw = raw_json(manifest)
            manifest_digest = 'sha256:' + hashlib.sha256(raw).hexdigest()
            extra[blob_path(manifest_digest)] = raw
            descriptors.append({'mediaType': OCI + 'manifest.v1+json', 'digest': manifest_digest, 'size': len(raw), 'annotations': {'io.containerd.image.name': 'docker.io/' + ref, 'org.opencontainers.image.ref.name': ref}})
            images[role].update(config_digest=config, manifest_digest=manifest_digest, ref=ref)
            entry['RepoTags'] = [ref]
        if used != set(images):
            raise InstallError('image export set is incomplete')
        with tarfile.open(destination, 'w') as dst:
            copied = set()
            for member in src.getmembers():
                if member.name in {'manifest.json', 'index.json', 'oci-layout', 'repositories'}:
                    continue
                if member.name.startswith('/') or '..' in Path(member.name).parts or not member.isfile():
                    if member.isdir():
                        continue
                    raise InstallError('unsafe image archive member')
                if member.name in copied:
                    raise InstallError('duplicate image archive member')
                copied.add(member.name)
                dst.addfile(member, src.extractfile(member))
            for name, value in extra.items():
                if name in copied:
                    continue
                if isinstance(value, bytes):
                    add_bytes(dst, name, value)
                else:
                    member = src.getmember(value)
                    replacement = tarfile.TarInfo(name)
                    replacement.size = member.size
                    dst.addfile(replacement, src.extractfile(member))
            add_bytes(dst, 'manifest.json', raw_json(entries))
            add_bytes(dst, 'index.json', raw_json({'schemaVersion': 2, 'mediaType': OCI + 'index.v1+json', 'manifests': descriptors}))
            add_bytes(dst, 'oci-layout', b'{"imageLayoutVersion":"1.0.0"}')


def verify(bundle):
    path, archive = bundle / 'release.json', bundle / 'images.tar'
    if path.is_symlink() or archive.is_symlink() or not path.is_file() or not archive.is_file() or path.stat().st_size > 16384:
        raise InstallError('invalid release files')
    data = json.loads(path.read_text())
    if not isinstance(data, dict) or data.get('schema_version') != 2 or data.get('os') != 'linux' or data.get('arch') not in ('arm64', 'amd64'):
        raise InstallError('unsupported release manifest')
    if not re.fullmatch(r'v[0-9]+\.[0-9]+\.[0-9]+(?:-[0-9A-Za-z]+(?:[.-][0-9A-Za-z]+)*)?', data.get('version', '')) or not re.fullmatch(r'[0-9a-f]{40}', data.get('revision', '')):
        raise InstallError('invalid release identity')
    if data.get('binary_version') != data['version'] + '+git.' + data['revision'][:12]:
        raise InstallError('release binary identity does not match')
    if not isinstance(data, dict):
        raise InstallError('invalid registry release manifest')
    images = data.get('images')
    if not isinstance(images, dict) or set(images) not in (ROLES, ROLES | {'ikev2'}):
        raise InstallError('release image set is incomplete')
    for role, image in images.items():
        for key in ('id', 'config_digest', 'manifest_digest'):
            if not DIGEST.fullmatch(image.get(key, '')):
                raise InstallError('invalid image identity')
        if image.get('ref') != 'vpnmodo-offline/' + role + ':sha256-' + image['config_digest'][7:]:
            raise InstallError('invalid content-addressed image reference')
    digest = hashlib.sha256()
    with archive.open('rb') as stream:
        for block in iter(lambda: stream.read(1024 * 1024), b''):
            digest.update(block)
    if digest.hexdigest() != data.get('archive_sha256'):
        raise InstallError('image archive checksum mismatch; nothing loaded')
    with tarfile.open(archive) as tar:
        entries = json.loads(read_small(tar, 'manifest.json'))
        if not isinstance(entries, list) or len(entries) != len(images):
            raise InstallError('archive image set does not match release')
        found = set()
        for entry in entries:
            tags = entry.get('RepoTags')
            candidates = [role for role, image in images.items() if tags == [image['ref']]]
            if len(candidates) != 1 or candidates[0] in found:
                raise InstallError('foreign or duplicate archive tag refused')
            role = candidates[0]
            found.add(role)
            raw = read_small(tar, entry['Config'])
            if 'sha256:' + hashlib.sha256(raw).hexdigest() != images[role]['config_digest']:
                raise InstallError('archive image identity does not match release')
            configuration = json.loads(raw)
            if configuration.get('architecture') != data['arch'] or configuration.get('os') != 'linux':
                raise InstallError('archive image architecture does not match release')
            manifest = read_small(tar, blob_path(images[role]['manifest_digest']))
            if 'sha256:' + hashlib.sha256(manifest).hexdigest() != images[role]['manifest_digest'] or json.loads(manifest)['config']['digest'] != images[role]['config_digest']:
                raise InstallError('OCI manifest identity mismatch')
        index = json.loads(read_small(tar, 'index.json'))
        actual = {(d['digest'], d.get('annotations', {}).get('io.containerd.image.name')) for d in index['manifests']}
        expected = {(i['manifest_digest'], 'docker.io/' + i['ref']) for i in images.values()}
        if found != set(images) or len(index['manifests']) != len(images) or actual != expected:
            raise InstallError('OCI image index does not match release')
    return data


def inspect_optional(ref):
    result = subprocess.run(['docker', '--host', 'unix:///var/run/docker.sock', 'image', 'inspect', ref], capture_output=True, timeout=30)
    if result.returncode == 0:
        return json.loads(result.stdout)[0]
    if b'no such image' in result.stderr.lower() or b'no such object' in result.stderr.lower():
        return None
    raise InstallError('cannot inspect existing release reference')


def install(bundle):
    data = verify(bundle)
    info = json.loads(docker('info', '--format', DOCKER_JSON_FORMAT))
    arch = {'aarch64': 'arm64', 'x86_64': 'amd64', 'arm64': 'arm64', 'amd64': 'amd64'}.get(info.get('Architecture'))
    if info.get('OSType') != 'linux' or arch != data['arch']:
        raise InstallError('bundle architecture does not match the Linux Docker engine')
    docker('compose', 'version')
    for image in data['images'].values():
        current = inspect_optional(image['ref'])
        if current and current.get('Id') not in (image['config_digest'], image['manifest_digest']):
            raise InstallError('release reference already points at different content; nothing loaded')
    docker('load', '-i', str(bundle / 'images.tar'))
    local = {}
    for role, image in data['images'].items():
        actual = json.loads(docker('image', 'inspect', image['ref']))[0]
        if actual.get('Id') not in (image['config_digest'], image['manifest_digest']) or actual.get('Architecture') != data['arch'] or actual.get('Os') != 'linux':
            raise InstallError('loaded image does not match release manifest')
        local[role] = actual['Id']
    actual = docker('run', '--rm', '--pull=never', '--network', 'none', local['controller'], 'version')
    if actual != data['binary_version']:
        raise InstallError('controller version does not match release manifest')
    print(json.dumps({'schema_version': 1, 'ok': True, 'version': actual, 'controller_image': local.pop('controller'), 'backend_images': local, 'activated': False}))


def install_registry(manifest):
    if manifest.stat().st_size > 4096:
        raise InstallError('registry release manifest is too large')
    data = json.loads(manifest.read_text())
    images = data.get('images')
    if data.get('schema_version') != 1 or data.get('os') != 'linux' or data.get('arch') not in ('arm64', 'amd64') or not isinstance(images, dict) or set(images) not in (ROLES, ROLES | {'ikev2'}):
        raise InstallError('invalid registry release manifest')
    if not isinstance(data.get('binary_version'), str) or not data['binary_version']:
        raise InstallError('missing registry release version')
    for role, ref in images.items():
        if not isinstance(ref, str) or not re.fullmatch(r'ghcr\.io/derrickhayashi/vpnmodo/' + (r'(?:vpntools|application)' if role == 'application' else role) + r'@sha256:[0-9a-f]{64}', ref):
            raise InstallError('registry images must use published GHCR digests')
    info = json.loads(docker('info', '--format', DOCKER_JSON_FORMAT))
    arch = {'aarch64': 'arm64', 'x86_64': 'amd64', 'arm64': 'arm64', 'amd64': 'amd64'}.get(info.get('Architecture'))
    if info.get('OSType') != 'linux' or arch != data['arch']:
        raise InstallError('registry release architecture does not match the Linux Docker engine')
    docker('compose', 'version')
    local = {}
    for role, ref in images.items():
        actual = inspect_optional(ref)
        if actual is None:
            docker('pull', ref)
            actual = json.loads(docker('image', 'inspect', ref))[0]
        if actual.get('Architecture') != data['arch'] or actual.get('Os') != 'linux' or not DIGEST.fullmatch(actual.get('Id', '')):
            raise InstallError('pulled image does not match release architecture')
        local[role] = actual['Id']
    actual = docker('run', '--rm', '--pull=never', '--network', 'none', local['controller'], 'version')
    if actual != data['binary_version']:
        raise InstallError('controller version does not match registry release')
    print(json.dumps({'schema_version': 1, 'ok': True, 'version': actual, 'controller_image': local.pop('controller'), 'backend_images': local, 'activated': False}))


def main():
    if len(sys.argv) == 3 and sys.argv[1] == '--registry':
        install_registry(Path(sys.argv[2]))
    elif len(sys.argv) == 1:
        install(Path(__file__).resolve().parent)
    else:
        raise InstallError('use install-vm.py [--registry REGISTRY_RELEASE.json]')


if __name__ == '__main__':
    try:
        main()
    except (InstallError, OSError, ValueError, KeyError, TypeError, tarfile.TarError):
        print('VM image installation failed; check release files, registry login, architecture and Docker prerequisites', file=sys.stderr)
        sys.exit(1)
