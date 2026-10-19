"""Run the real archive tasks in Ansible against fresh and active deployments."""
import getpass
import importlib.util
import os
from pathlib import Path
import subprocess
import sys
import tarfile
import tempfile
import unittest

import yaml

ROOT = Path(__file__).resolve().parents[1]


@unittest.skipUnless(importlib.util.find_spec('ansible'), 'requires ansible-core')
class SourceCheckModeTests(unittest.TestCase):
    def test_archive_checks_and_active_session_guard(self):
        tasks = yaml.safe_load((ROOT / 'roles/vpn_sidecar/tasks/main.yaml').read_text())
        selected = [task for task in tasks if task['name'] in {
            'Inspect the pinned source directory before archive checks',
            'Preview pinned source changes before touching an active XAuth session',
            'Require explicit XAuth disconnect before changing active runtime inputs',
            'Extract pinned package source on the VPN host',
        }]
        self.assertEqual(len(selected), 4)
        cases = [
            ('ipsec-xauth', None, False, False, True),
            ('l2tp-ipsec', None, False, False, True),
            ('ipsec-xauth', None, True, False, False),
            ('ipsec-xauth', 'pinned source\n', True, False, True),
            ('ipsec-xauth', 'pinned source\n', True, True, False),
        ]
        for backend, existing, active, credentials_changed, success in cases:
            with self.subTest(backend=backend, existing=existing, active=active,
                              credentials_changed=credentials_changed):
                with tempfile.TemporaryDirectory() as directory:
                    base = Path(directory)
                    source = base / 'deployed'
                    content = base / 'marker'
                    content.write_text('pinned source\n')
                    archive = base / 'source.tar'
                    with tarfile.open(archive, 'w') as bundle:
                        bundle.add(content, arcname='marker')
                    if existing is not None:
                        source.mkdir()
                        (source / 'marker').write_text(existing)
                    values = {
                        'vpn_sidecar_backend': backend,
                        'vpn_sidecar_source': str(source),
                        'vpn_sidecar_archive': str(archive),
                        'ansible_user': getpass.getuser(),
                        'vpn_sidecar_xauth_running': {'stdout': 'container-id' if active else ''},
                    }
                    for field in ('xauth_credentials', 'compose', 'env', 'profile'):
                        values['vpn_sidecar_' + field + '_preview'] = {'changed': False}
                    values['vpn_sidecar_xauth_credentials_preview']['changed'] = credentials_changed
                    playbook = base / 'check.yaml'
                    playbook.write_text(yaml.safe_dump([{
                        'hosts': 'localhost', 'gather_facts': False,
                        'vars': values, 'tasks': selected,
                    }]))
                    config = base / 'ansible.cfg'
                    config.write_text('[defaults]\n')
                    env = dict(os.environ, ANSIBLE_CONFIG=str(config),
                               ANSIBLE_LOCAL_TEMP=str(base / 'tmp'), ANSIBLE_NOCOLOR='1')
                    result = subprocess.run(
                        [sys.executable, '-m', 'ansible.cli.playbook', '-i', 'localhost,',
                         '-c', 'local', '--check', str(playbook)],
                        env=env, capture_output=True, text=True,
                    )
                    output = result.stdout + result.stderr
                    self.assertEqual(result.returncode == 0, success, output)
                    if existing is None:
                        self.assertFalse(source.exists(), output)
                    else:
                        self.assertEqual((source / 'marker').read_text(), existing)
                    if not success:
                        self.assertIn('Disconnect this profile explicitly', output)


if __name__ == '__main__':
    unittest.main()
