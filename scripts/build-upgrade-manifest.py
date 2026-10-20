#!/usr/bin/env python3
"""Create the macOS arm64 upgrade descriptor from built release artifacts.
Publish the output and raw CLI binary as assets of the same GitHub release.
"""
import argparse
import hashlib
import json
from pathlib import Path
import re
import tarfile


def build(cli_dir, runtime_file):
    runtime = json.loads(runtime_file.read_text())
    version = runtime['version']
    if not re.fullmatch(r'v\d+\.\d+\.\d+(?:-[0-9A-Za-z]+(?:[.-][0-9A-Za-z]+)*)?', version):
        raise ValueError('invalid version')
    name = 'vpnmodo-darwin-arm64'
    binary = cli_dir / name
    with tarfile.open(cli_dir / f'vpnmodo-{version}-darwin-arm64.tar.gz') as archive:
        member = archive.getmember('release.json')
        if not member.isfile() or member.size > 16384:
            raise ValueError('invalid CLI metadata')
        cli = json.load(archive.extractfile(member))
    digest = hashlib.sha256(binary.read_bytes()).hexdigest()
    if cli['binary_sha256'] != digest or cli['binary_version'] != runtime['binary_version'] or cli['version'] != version or cli['os'] != 'darwin' or cli['arch'] != 'arm64':
        raise ValueError('CLI and runtime release do not match')
    if runtime['schema_version'] != 1 or runtime['os'] != 'linux' or runtime['arch'] != 'arm64' or set(runtime['images']) != {'controller', 'l2tp', 'xauth', 'application'}:
        raise ValueError('incompatible runtime release')
    for role, image in runtime['images'].items():
        if not re.fullmatch(r'ghcr\.io/derrickhayashi/vpn-sidecars-next/' + role + r'@sha256:[0-9a-f]{64}', image):
            raise ValueError('runtime images must be immutable published digests')
    return dict(schema_version=1, version=version, cli_version=cli['binary_version'], api_major=1, state_schema=1,
                cli=dict(asset=name, sha256=digest, size=binary.stat().st_size), runtime=runtime)


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--cli-dir', required=True, type=Path)
    parser.add_argument('--runtime', required=True, type=Path)
    args = parser.parse_args()
    output = args.cli_dir / 'upgrade-darwin-arm64.json'
    output.write_text(json.dumps(build(args.cli_dir, args.runtime), indent=2) + '\n')
    print(output)


if __name__ == '__main__':
    main()
