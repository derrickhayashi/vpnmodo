"""Exercise role backend rendering and XAuth provisioning safety without a VM."""
from pathlib import Path
import subprocess
import re
import tempfile
import unittest

import yaml
from jinja2.nativetypes import NativeEnvironment

ROOT = Path(__file__).resolve().parents[1]
ROLE = ROOT / 'roles/vpn_sidecar'


class RoleBackendTests(unittest.TestCase):
    def setUp(self):
        self.tasks = yaml.safe_load((ROLE / 'tasks/main.yaml').read_text())
        self.defaults = yaml.safe_load((ROLE / 'defaults/main.yaml').read_text())
        self.env = NativeEnvironment()
        self.env.tests.update(match=lambda value, pattern: re.match(pattern, str(value)) is not None,
                              search=lambda value, pattern: re.search(pattern, str(value)) is not None)
        self.env.filters.update(dirname=lambda value: str(Path(value).parent), bool=bool)
        self.values = dict(self.defaults, vpn_sidecar_package_version='a' * 40,
                           vpn_sidecar_runtime_dir='/srv/consumer/instances/example',
                           vpn_sidecar_credentials_dir='/srv/consumer/instances/example/secrets',
                           vpn_sidecar_project_name='vpn-example', vpn_sidecar_instance='example',
                           vpn_sidecar_server='203.0.113.10', vpn_sidecar_health_host='192.0.2.10',
                           vpn_sidecar_health_port=22, playbook_dir='/srv/playbooks',
                           vpn_sidecar_commit={'stdout': 'a' * 40}, ansible_check_mode=False,
                           vpn_sidecar_source_directory={'stat': {'isdir': True}})

    def task(self, name):
        for task in self.tasks:
            if task['name'] == name:
                return task
            for child in task.get('block', []):
                if child['name'] == name:
                    return child
        self.fail('Task not found: ' + name)

    def render(self, value):
        if isinstance(value, dict):
            return {key: self.render(item) for key, item in value.items()}
        if isinstance(value, list):
            return [self.render(item) for item in value]
        if isinstance(value, str):
            return self.env.from_string(value).render(**self.values)
        return value

    def configure(self, backend):
        self.values['vpn_sidecar_backend'] = backend
        for key in ('vpn_sidecar_ike_proposals', 'vpn_sidecar_esp_proposals'):
            self.values[key] = self.render(self.defaults[key])
        self.values.update(self.render(self.task('Define package paths and consumer configuration')['ansible.builtin.set_fact']))

    def conditions(self, task):
        conditions = task.get('when', [])
        if isinstance(conditions, str):
            conditions = [conditions]
        return all(self.env.compile_expression(item)(**self.values) for item in conditions)

    def test_backend_images_modes_and_proposals(self):
        for backend, mode, proposal in [('l2tp-ipsec', 'auto', 'aes256-sha256-modp2048'),
                                        ('ipsec-xauth', 'manual', 'aes128-sha1-modp1536')]:
            with self.subTest(backend=backend):
                self.configure(backend)
                settings = self.values['vpn_sidecar_environment']
                self.assertEqual(settings['VPN_WATCHDOG_MODE'], mode)
                self.assertTrue(settings['VPN_BACKEND_DIR'].endswith('/containers/' + backend))
                self.assertTrue(settings['VPN_IKE_PROPOSALS'].startswith(proposal))
                self.assertIn('l2tp-ipsec' if backend == 'l2tp-ipsec' else 'ipsec-xauth', settings['VPN_IMAGE'])

    def test_xauth_never_activates_vpn_or_watchdog(self):
        self.configure('ipsec-xauth')
        for name in ['Start the package and wait for VPN health',
                     'Start containerized recovery after VPN validation']:
            self.assertFalse(self.conditions(self.task(name)))
        self.configure('l2tp-ipsec')
        self.assertTrue(self.conditions(self.task('Start the package and wait for VPN health')))

    def test_active_xauth_changes_are_rejected_before_source_extract(self):
        self.configure('ipsec-xauth')
        names = [task['name'] for task in self.tasks]
        gate = 'Require explicit XAuth disconnect before changing active runtime inputs'
        self.assertLess(names.index(gate), names.index('Extract pinned package source on the VPN host'))
        for changed_field in ('xauth_credentials', 'compose', 'env', 'profile', 'xauth_source'):
            for field in ('xauth_credentials', 'compose', 'env', 'profile', 'xauth_source'):
                self.values['vpn_sidecar_' + field + '_preview'] = {'changed': field == changed_field}
            assertion = self.task(gate)['ansible.builtin.assert']['that'][0]
            self.assertFalse(self.env.compile_expression(assertion)(**self.values), changed_field)
        for field in ('xauth_credentials', 'compose', 'env', 'profile', 'xauth_source'):
            self.values['vpn_sidecar_' + field + '_preview'] = {'changed': False}
        self.assertTrue(self.env.compile_expression(assertion)(**self.values))

    def test_xauth_credentials_preserve_literal_characters_and_are_protected(self):
        self.configure('ipsec-xauth')
        self.values['vpn_sidecar_credentials'] = {
            'username': 'person@example.test', 'password': 'literal=$"\\word', 'psk': 'key=with spaces'}
        content = self.render((ROLE / 'templates/xauth-credentials.j2').read_text())
        parsed = dict(line.split('=', 1) for line in content.splitlines())
        self.assertEqual(parsed, {'USERNAME': 'person@example.test', 'PASSWORD': 'literal=$"\\word',
                                  'PRE_SHARED_KEY': 'key=with spaces'})
        for name in ('Preview XAuth credential changes without exposing values', 'Deploy protected XAuth credentials'):
            task = self.task(name)
            self.assertTrue(task['no_log'])
            self.assertFalse(task['diff'])
            self.assertEqual(task['ansible.builtin.template']['mode'], '0600')

    def test_generated_xauth_profile_passes_real_launcher_validation(self):
        self.configure('ipsec-xauth')
        content = self.render((ROLE / 'templates/profile.env.j2').read_text())
        with tempfile.TemporaryDirectory() as directory:
            profile = Path(directory) / 'example'
            profile.mkdir()
            (profile / 'profile.env').write_text(content)
            result = subprocess.run(['bash', str(ROOT / 'scripts/vpn'), '--profiles-dir', directory,
                                     '--local', 'example', 'validate'], capture_output=True, text=True)
            self.assertEqual(result.returncode, 0, result.stderr)
        self.assertNotIn('PASSWORD', content)
        self.assertNotIn('PRE_SHARED_KEY', content)

    def test_launcher_rejects_invalid_instance_names_and_role_matches(self):
        self.configure('ipsec-xauth')
        assertions = self.task('Validate deployment scope and settings')['ansible.builtin.assert']['that']
        instance_assertions = [item for item in assertions if item.startswith('vpn_sidecar_instance')]
        for name in ('example--bad', 'example-', '-example', 'Example', 'x' * 49):
            self.values['vpn_sidecar_instance'] = name
            self.assertFalse(all(self.env.compile_expression(item)(**self.values)
                                 for item in instance_assertions), name)

    def test_xauth_rejects_profile_values_the_launcher_cannot_use(self):
        self.configure('ipsec-xauth')
        assertions = self.task('Validate XAuth launcher layout and gateway')['ansible.builtin.assert']['that']
        for field, value in [('vpn_sidecar_health_host', 'bad host'),
                             ('vpn_sidecar_health_port', '22.0'),
                             ('vpn_sidecar_watchdog_interval', 100000),
                             ('vpn_sidecar_watchdog_cooldown', 1000000)]:
            original = self.values[field]
            self.values[field] = value
            self.assertFalse(all(self.env.compile_expression(item)(**self.values) for item in assertions), field)
            self.values[field] = original
        assertions = self.task('Validate settings for the Compose environment file')['ansible.builtin.assert']['that']
        for invalid in ('bad\nline', 'bad\rline', 'bad\0line'):
            self.values['item'] = invalid
            self.assertFalse(all(self.env.compile_expression(item)(**self.values) for item in assertions))

    def test_selected_backend_assets_and_build_context(self):
        self.configure('ipsec-xauth')
        assets = self.render(self.task('Validate pinned backend assets before changing the VPN host')['loop'])
        self.assertIn('containers/ipsec-xauth/connect.exp', assets)
        self.assertIn('scripts/vpn', assets)
        self.assertNotIn('containers/l2tp-ipsec/Dockerfile', assets)
        build = self.render(self.task('Build the package image when missing or source changed')['ansible.builtin.command']['argv'])
        self.assertTrue(build[-1].endswith('/containers/ipsec-xauth'))


if __name__ == '__main__':
    unittest.main()
