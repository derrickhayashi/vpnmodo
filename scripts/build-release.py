#!/usr/bin/env python3
"""Build private release artifacts from committed source; never publish them."""
import argparse
import gzip
import hashlib
import json
import os
from pathlib import Path
import re
import shutil
import subprocess
import tarfile
import tempfile

VERSION = re.compile(r"v[0-9]+\.[0-9]+\.[0-9]+(?:-[0-9A-Za-z]+(?:[.-][0-9A-Za-z]+)*)?")
REVISION = re.compile(r"[0-9a-f]{40}")
IMAGE_ID = re.compile(r"sha256:[0-9a-f]{64}")


def run(args, **kwargs):
    return subprocess.check_output(args, **kwargs).decode().strip()


def digest(path):
    result = hashlib.sha256()
    with path.open('rb') as stream:
        for block in iter(lambda: stream.read(1024 * 1024), b''):
            result.update(block)
    return result.hexdigest()


def write_json(path, value):
    path.write_text(json.dumps(value, indent=2, sort_keys=True) + '\n')


def archive(source, destination, epoch):
    # Stable archive names, timestamps, modes, users and gzip header.
    with destination.open('wb') as raw, gzip.GzipFile(filename='', mode='wb', fileobj=raw, mtime=0) as compressed:
        with tarfile.open(fileobj=compressed, mode='w|') as tar:
            for path in sorted(source.rglob('*')):
                if path.is_symlink():
                    raise RuntimeError('release source contains a symlink; review before packaging')
                if not path.is_file():
                    continue
                info = tar.gettarinfo(str(path), str(path.relative_to(source)))
                info.uid = info.gid = 0
                info.uname = info.gname = ''
                info.mtime = epoch
                info.mode = 0o755 if path.stat().st_mode & 0o111 else 0o644
                with path.open('rb') as stream:
                    tar.addfile(info, stream)


def source_snapshot(root, temporary, version):
    source = temporary / 'source'
    source.mkdir()
    if (root / '.git').exists():
        if run(['git', 'status', '--porcelain', '--untracked-files=all'], cwd=root):
            raise RuntimeError('commit the intended source first; dirty checkouts are refused')
        revision = run(['git', 'rev-parse', 'HEAD'], cwd=root)
        epoch = int(run(['git', 'show', '-s', '--format=%ct', 'HEAD'], cwd=root))
        subprocess.run(['git', 'archive', '--format=tar', '-o', str(temporary / 'source.tar'), 'HEAD'], cwd=root, check=True)
        with tarfile.open(temporary / 'source.tar') as tar:
            # Git-tracked source only; no ignored files or consumer directories.
            for member in tar.getmembers():
                if not member.isfile() and not member.isdir():
                    raise RuntimeError('unsupported source archive member')
                if member.name.startswith('/') or '..' in Path(member.name).parts:
                    raise RuntimeError('unsafe source archive member')
            for member in tar.getmembers():
                destination = source / member.name
                if member.isdir():
                    destination.mkdir(parents=True, exist_ok=True)
                else:
                    destination.parent.mkdir(parents=True, exist_ok=True)
                    with tar.extractfile(member) as stream, destination.open('wb') as output:
                        shutil.copyfileobj(stream, output)
                    destination.chmod(member.mode & 0o777)
        metadata = {'schema_version': 1, 'version': version, 'revision': revision, 'source_epoch': epoch}
        write_json(source / 'release-source.json', metadata)
    else:
        metadata = json.loads((root / 'release-source.json').read_text())
        if metadata.get('schema_version') != 1 or metadata.get('version') != version:
            raise RuntimeError('source bundle release identity does not match')
        if not REVISION.fullmatch(metadata.get('revision', '')) or type(metadata.get('source_epoch')) is not int:
            raise RuntimeError('invalid source bundle identity')
        # A source bundle must have been independently checksum-verified first.
        shutil.copytree(root, source, dirs_exist_ok=True, symlinks=True)
    return source, metadata


def cli(source, output, metadata, temporary):
    version = metadata['version']
    identity = version + '+git.' + metadata['revision'][:12]
    env = dict(os.environ, CGO_ENABLED='0', GOOS='darwin')
    for arch in ('arm64', 'amd64'):
        stage = temporary / ('cli-' + arch)
        stage.mkdir()
        env['GOARCH'] = arch
        subprocess.run(['go', 'build', '-trimpath', '-buildvcs=false', '-ldflags', '-s -w -X main.version=' + identity,
                        '-o', str(stage / 'vpnmodo'), './cmd/vpnmodo'], cwd=source, env=env, check=True)
        manifest = dict(metadata, os='darwin', arch=arch, binary_version=identity, binary_sha256=digest(stage / 'vpnmodo'))
        write_json(stage / 'release.json', manifest)
        template = (source / 'scripts/release/install-cli.sh.in').read_text()
        template = template.replace('@ARCH@', arch).replace('@SHA256@', manifest['binary_sha256'])
        (stage / 'install.sh').write_text(template)
        (stage / 'README.txt').write_text('See docs/releases.md and docs/onboarding.md in the source bundle.\nVerify the archive using the independently obtained SHA256SUMS-cli file before extracting.\nRun: sh install.sh --prefix "$HOME/.local"\n')
        archive(stage, output / ('vpnmodo-' + version + '-darwin-' + arch + '.tar.gz'), metadata['source_epoch'])
    archive(source, output / ('vpnmodo-' + version + '-source.tar.gz'), metadata['source_epoch'])
    return 'cli'


def docker(*args):
    # Explicitly target the local rootful VM engine, never a workstation context.
    return run(['docker', '--host', 'unix:///var/run/docker.sock', *args])


def vm(source, output, metadata, temporary):
    info = json.loads(docker('info', '--format', '{{json .}}'))
    arch = {'aarch64': 'arm64', 'x86_64': 'amd64', 'arm64': 'arm64', 'amd64': 'amd64'}.get(info['Architecture'])
    if info.get('OSType') != 'linux' or not arch:
        raise RuntimeError('build VM images on a native Linux arm64/amd64 Docker engine')
    docker('compose', 'version')
    version, revision = metadata['version'], metadata['revision']
    identity = version + '+git.' + revision[:12]
    images = {}
    for role, path, name in [('l2tp', 'containers/l2tp-ipsec', 'vpnmodo-l2tp'), ('xauth', 'containers/ipsec-xauth', 'vpnmodo-xauth')]:
        tag = name + ':' + version
        docker('build', '--build-arg', 'VERSION=' + identity, '--build-arg', 'REVISION=' + revision, '-t', tag, str(source / path))
        images[role] = {'tag': tag, 'id': docker('image', 'inspect', '--format', '{{.Id}}', tag)}
    docker('pull', 'alpine:3.22')
    images['application'] = {'tag': 'alpine:3.22', 'id': docker('image', 'inspect', '--format', '{{.Id}}', 'alpine:3.22')}
    controller = 'vpnmodo-controller:' + version
    docker('build', '-f', str(source / 'containers/controller/Dockerfile'), '-t', controller,
           '--build-arg', 'VERSION=' + identity, '--build-arg', 'REVISION=' + revision,
           '--build-arg', 'L2TP_IMAGE=' + images['l2tp']['id'], '--build-arg', 'XAUTH_IMAGE=' + images['xauth']['id'],
           '--build-arg', 'APPLICATION_IMAGE=' + images['application']['id'], str(source))
    images['controller'] = {'tag': controller, 'id': docker('image', 'inspect', '--format', '{{.Id}}', controller)}
    for image in images.values():
        if not IMAGE_ID.fullmatch(image['id']):
            raise RuntimeError('invalid built image identity')
    actual = docker('run', '--rm', '--network', 'none', images['controller']['id'], 'version')
    if actual != identity:
        raise RuntimeError('built controller version does not match release')
    stage = temporary / 'vm'
    stage.mkdir()
    docker('save', '-o', str(stage / 'images.tar'), *[image['id'] for image in images.values()])
    manifest = dict(metadata, os='linux', arch=arch, binary_version=identity, images=images, archive_sha256=digest(stage / 'images.tar'))
    write_json(stage / 'release.json', manifest)
    shutil.copyfile(source / 'scripts/release/install-vm.py', stage / 'install-vm.py')
    (stage / 'README.txt').write_text('See docs/releases.md and docs/onboarding.md in the source bundle.\nVerify the archive using the independently obtained SHA256SUMS-linux-' + arch + ' file before extracting.\nRun: python3 install-vm.py\nThis loads images only; host setup explicitly activates the controller.\n')
    archive(stage, output / ('vpnmodo-' + version + '-linux-' + arch + '-images.tar.gz'), metadata['source_epoch'])
    return 'linux-' + arch


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('kind', choices=('cli', 'vm'))
    parser.add_argument('--version', required=True)
    parser.add_argument('--output', type=Path, required=True, help='new directory outside the source checkout')
    args = parser.parse_args()
    if not VERSION.fullmatch(args.version):
        parser.error('use a version such as v0.1.0-rc.1')
    root = Path(__file__).resolve().parents[1]
    output = args.output.expanduser().resolve()
    if output.is_relative_to(root) or output.exists():
        parser.error('output must be a new directory outside the source checkout')
    try:
        with tempfile.TemporaryDirectory(prefix='vpnmodo-release-') as directory:
            source, metadata = source_snapshot(root, Path(directory), args.version)
            output.mkdir(parents=True)
            kind = globals()[args.kind](source, output, metadata, Path(directory))
            lines = [digest(path) + '  ' + path.name + '\n' for path in sorted(output.iterdir()) if path.is_file()]
            (output / ('SHA256SUMS-' + kind)).write_text(''.join(lines))
        print(str(output))
    except (OSError, ValueError, KeyError, RuntimeError, subprocess.CalledProcessError) as error:
        # Release builds contain committed synthetic source only. No deployments.
        parser.exit(1, 'release build failed: ' + str(error) + '\n')


if __name__ == '__main__':
    main()
