#!/usr/bin/env python3
"""Real Docker transaction smoke, only inside a disposable Linux helper container.

Build a helper FROM the candidate controller image with Python 3 installed, then
run with VPNMODO_UPGRADE_SMOKE=1, a disposable --home directory bind-mounted at
the same path, a read-only checkout at /source, and a disposable Docker daemon's
socket. Refuses an existing controller. No VPN endpoints or credentials are used.
Image staging is injected with preloaded image IDs; registry verification has
separate unit tests. Activation, pause, state copy, health and rollback are real.
"""
import argparse
import json
import os
from pathlib import Path
import subprocess
import time
import uuid


def main():
    if os.environ.get('VPNMODO_UPGRADE_SMOKE') != '1' or not Path('/.dockerenv').exists():
        raise RuntimeError('requires an explicitly enabled disposable Linux helper container')
    parser = argparse.ArgumentParser()
    parser.add_argument('--image', required=True)
    parser.add_argument('--previous', required=True)
    parser.add_argument('--home', required=True, type=Path)
    args = parser.parse_args()
    namespace = {'__name__': 'bootstrap'}
    source = Path(__file__).resolve().parents[1]
    exec((source / 'internal/host/bootstrap.py').read_text(), namespace)
    docker = namespace['docker']
    if namespace['inspect']('container', 'vpnmodo-controller'):
        raise RuntimeError('refusing an existing controller')
    root = args.home / '.local/share/vpnmodo'
    if root.exists():
        raise RuntimeError('refusing existing private state')
    root.mkdir(parents=True, mode=0o700)
    state = root / 'controller-state'
    state.mkdir(mode=0o700)
    settings = state / 'synthetic-settings'
    settings.write_bytes(b'synthetic private settings')
    settings.chmod(0o600)
    image = namespace['inspect']('image', args.image)['Id']
    old = namespace['inspect']('image', args.previous)['Id']
    version = docker('run', '--rm', '--network', 'none', image, 'version').decode().strip()
    sentinel = 'upgrade-smoke-' + uuid.uuid4().hex[:12]
    original_home = Path.home
    try:
        Path.home = classmethod(lambda cls: args.home)
        namespace['activate']({'image': old, 'replace': False}, root, state)
        docker('run', '-d', '--name', sentinel, '--network', 'none', '--label', 'io.vpnmodo.managed-by=vpnmodo', '--label', 'io.vpnmodo.profile=synthetic', '--entrypoint', 'sleep', image, '3600')
        backend = namespace['inspect']('container', sentinel)['Id']
        started = namespace['inspect']('container', sentinel)['State']['StartedAt']
        exec((source / 'internal/host/upgrade.py').read_text(), namespace)
        def install(path):
            print(json.dumps({'controller_image': image, 'backend_images': dict(l2tp=image, xauth=image, application=image)}))
        namespace['I'] = {'install_registry': install}
        namespace['ready']()
        for commit in (False, True):
            token = uuid.uuid4().hex
            params = dict(action='prepare', token=token, runtime=dict(binary_version=version))
            assert namespace['upgrade'](params)['phase'] == 'prepared'
            assert namespace['inspect']('container', 'vpnmodo-controller')['Image'] == image
            assert settings.read_bytes() == b'synthetic private settings'
            action = 'commit' if commit else 'rollback'
            assert namespace['upgrade'](dict(action=action, token=token))['phase'] == ('committed' if commit else 'rolled_back')
            assert namespace['inspect']('container', 'vpnmodo-controller')['Image'] == (image if commit else old)
            actual = namespace['inspect']('container', sentinel)
            assert actual['Id'] == backend and actual['State']['Running'] and actual['State']['StartedAt'] == started
            assert settings.read_bytes() == b'synthetic private settings'
            assert not (root / 'upgrade-pending').exists()
        print('PASS: real controller replacement, rollback, private settings preservation, unchanged backend ID/start time')
    finally:
        Path.home = original_home
        for name in ('vpnmodo-controller', sentinel):
            subprocess.run(['docker', '--host', 'unix:///var/run/docker.sock', 'rm', '--force', name], stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)


if __name__ == '__main__':
    main()
