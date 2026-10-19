#!/usr/bin/env python3
"""Native macOS archive installation/upgrade refusal checks in a temporary prefix."""
import argparse
import hashlib
import json
from pathlib import Path
import platform
import shutil
import subprocess
import tarfile
import tempfile


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--archive', required=True, type=Path)
    args = parser.parse_args()
    if platform.system() != 'Darwin':
        parser.error('run on native macOS')
    with tempfile.TemporaryDirectory(prefix='vpnmodo-install-') as directory:
        root = Path(directory).resolve()
        package = root / 'package'
        package.mkdir()
        with tarfile.open(args.archive) as tar:
            if {m.name for m in tar.getmembers()} != {'vpnmodo', 'install.sh', 'release.json', 'README.txt'}:
                raise RuntimeError('unexpected CLI archive members')
            for member in tar.getmembers():
                if not member.isfile():
                    raise RuntimeError('nonregular CLI archive member')
                with tar.extractfile(member) as source, (package / member.name).open('wb') as dest:
                    shutil.copyfileobj(source, dest)
        metadata = json.loads((package / 'release.json').read_text())
        arch = {'x86_64': 'amd64', 'arm64': 'arm64'}[platform.machine()]
        assert metadata['os'] == 'darwin' and metadata['arch'] == arch
        prefix = root / 'installation with spaces'
        def install(*options):
            return subprocess.run(['sh', str(package / 'install.sh'), '--prefix', str(prefix), *options], capture_output=True)
        assert install().returncode == 0
        binary = prefix / 'bin/vpnmodo'
        before = hashlib.sha256(binary.read_bytes()).hexdigest()
        assert subprocess.check_output([str(binary), 'version']).decode().strip() == metadata['binary_version']
        subprocess.run([str(binary), '--help'], check=True, stdout=subprocess.DEVNULL)
        assert install().returncode != 0, 'existing binary overwritten without explicit replacement'
        assert install('--replace').returncode == 0
        (package / 'vpnmodo').write_bytes(b'corrupted synthetic binary')
        assert install('--replace').returncode != 0, 'corrupt package was installed'
        assert hashlib.sha256(binary.read_bytes()).hexdigest() == before
        (package / 'vpnmodo').unlink()
        (package / 'vpnmodo').symlink_to(binary)
        assert install('--replace').returncode != 0, 'symlink package was installed'
        assert hashlib.sha256(binary.read_bytes()).hexdigest() == before
        assert not (prefix / 'bin/.vpnmodo-install.lock').exists()
        print('PASS: native macOS archive install, version/help, explicit replacement, corruption/symlink refusal and owned cleanup')


if __name__ == '__main__':
    main()
