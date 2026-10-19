"""Exercise the VM role's package and kernel policy branches without mutating a host."""
from pathlib import Path
import re
import unittest

from jinja2.nativetypes import NativeEnvironment
import yaml

ROOT = Path(__file__).resolve().parents[1]


class VmPrerequisiteTests(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        cls.tasks = yaml.safe_load((ROOT / 'roles/vpn_sidecar/tasks/prerequisites.yaml').read_text())
        cls.env = NativeEnvironment(keep_trailing_newline=True)
        cls.env.filters['bool'] = bool
        cls.env.filters['type_debug'] = lambda value: type(value).__name__
        cls.env.tests['match'] = lambda value, pattern: re.match(pattern, value) is not None

    def task(self, name):
        return next(task for task in self.tasks if task['name'] == name)

    def render(self, expression, **overrides):
        values = {
            'ansible_facts': {'distribution': 'Ubuntu', 'kernel': '6.8.0-100-generic', 'packages': {}},
            'vpn_sidecar_backend': 'l2tp-ipsec',
            'vpn_sidecar_l2tp_acceleration': False,
            'vpn_sidecar_kernel_blocklist': [],
            'vpn_sidecar_kernel_allowlist': [],
            'vpn_sidecar_kernel_warning_releases': ['6.8.0-146-generic'],
            'vpn_sidecar_install_docker': True,
            'vpn_sidecar_install_kernel_modules': True,
            'vpn_sidecar_docker_ce': False,
            'vpn_sidecar_docker_missing': True,
            'vpn_sidecar_docker_capabilities': {'results': [{'rc': 2}, {'rc': 2}, {'rc': 2}]},
            'vpn_sidecar_module_probe': {'results': [{'rc': 1, 'item': 'l2tp_ppp'}]},
            'vpn_sidecar_module_reprobe': {'results': [{'rc': 1, 'item': 'l2tp_ppp'}]},
            'vpn_sidecar_kernel_packages': {'results': [{'rc': 0}, {'rc': 0}]},
        }
        values.update(overrides)
        template = expression if '{{' in expression else '{{ ' + expression + ' }}'
        return self.env.from_string(template).render(**values)

    def enabled(self, name, **values):
        when = self.task(name).get('when', [])
        return all(self.render(condition, **values) for condition in ([when] if isinstance(when, str) else when))

    def test_kernel_policy_blocks_before_package_mutations(self):
        policy = self.tasks[0]['ansible.builtin.assert']['that']
        kernel = {'distribution': 'Ubuntu', 'kernel': '6.8.0-146-generic'}
        self.assertTrue(all(self.render(value, ansible_facts=kernel) for value in policy))
        self.assertFalse(all(self.render(value, ansible_facts=kernel,
                                        vpn_sidecar_kernel_blocklist=[kernel['kernel']]) for value in policy))
        self.assertFalse(all(self.render(value, ansible_facts=kernel,
                                        vpn_sidecar_kernel_allowlist=['6.8.0-100-generic']) for value in policy))
        self.assertFalse(all(self.render(value, vpn_sidecar_kernel_allowlist='6.8.0-100-generic') for value in policy))
        self.assertFalse(all(self.render(value, vpn_sidecar_kernel_blocklist=[123]) for value in policy))
        warning = 'Report a running kernel with a recorded stability incident'
        self.assertTrue(self.enabled(warning, ansible_facts=kernel))
        self.assertFalse(self.enabled(warning))

    def test_docker_packages_follow_installed_family(self):
        packages = self.task('Install the matching Docker engine and CLI plugins when incomplete')['ansible.builtin.apt']['name']
        self.assertEqual(self.render(packages), ['docker.io', 'docker-compose-v2', 'docker-buildx'])
        self.assertEqual(self.render(packages, vpn_sidecar_docker_ce=True),
                         ['docker-ce', 'docker-ce-cli', 'containerd.io', 'docker-compose-plugin', 'docker-buildx-plugin'])
        name = 'Install the matching Docker engine and CLI plugins when incomplete'
        self.assertFalse(self.enabled(name, vpn_sidecar_install_docker=False))
        self.assertFalse(self.enabled(name, vpn_sidecar_docker_missing=False))

    def test_custom_or_conflicting_docker_is_never_replaced(self):
        checks = self.task('Refuse to mix Docker CE and Ubuntu engine packages')['ansible.builtin.assert']['that']
        capabilities = {'results': [{'rc': 0}, {'rc': 2}, {'rc': 2}]}
        self.assertFalse(all(self.render(value, vpn_sidecar_docker_capabilities=capabilities) for value in checks))
        self.assertTrue(all(self.render(value, vpn_sidecar_docker_capabilities=capabilities,
                                       vpn_sidecar_install_docker=False) for value in checks))
        self.assertFalse(all(self.render(value, vpn_sidecar_docker_ce=True,
                                        ansible_facts={'packages': {'docker.io': []}}) for value in checks))
        self.assertFalse(all(self.render(value, ansible_facts={'packages': {'podman-docker': []}})
                             for value in checks))

    def test_ppp_requirements_are_l2tp_only(self):
        modules = self.task('Select the modules required by this VPN type')['ansible.builtin.set_fact']['vpn_sidecar_required_modules']
        self.assertEqual(self.render(modules, vpn_sidecar_backend='ipsec-xauth'), ['xfrm_user', 'esp4'])
        self.assertEqual(self.render(modules), ['xfrm_user', 'esp4', 'ppp_generic', 'ppp_async'])
        self.assertIn('l2tp_ppp', self.render(modules, vpn_sidecar_l2tp_acceleration=True))
        self.assertEqual(self.render(modules, vpn_sidecar_backend='ipsec-xauth',
                                     vpn_sidecar_l2tp_acceleration=True), ['xfrm_user', 'esp4'])
        self.assertFalse(self.enabled('Inspect the PPP device for L2TP',
                                      vpn_sidecar_backend='ipsec-xauth', ansible_check_mode=False))

    def test_multiple_backend_instances_preserve_their_own_boot_modules(self):
        task = self.task('Persist required kernel modules')['ansible.builtin.copy']
        modules = self.task('Select the modules required by this VPN type')['ansible.builtin.set_fact']['vpn_sidecar_required_modules']
        l2tp_modules = self.render(modules, vpn_sidecar_backend='l2tp-ipsec')
        xauth_modules = self.render(modules, vpn_sidecar_backend='ipsec-xauth')
        files = {}
        for instance, required in [('example-l2tp', l2tp_modules), ('example-xauth', xauth_modules)]:
            path = self.render(task['dest'], vpn_sidecar_instance=instance)
            files[path] = self.render(task['content'], vpn_sidecar_required_modules=required)
        self.assertEqual(len(files), 2)
        self.assertIn('ppp_generic\n', files['/etc/modules-load.d/vpn-sidecar-example-l2tp.conf'])
        self.assertEqual(files['/etc/modules-load.d/vpn-sidecar-example-xauth.conf'], 'xfrm_user\nesp4\n')

    def test_extra_modules_require_missing_modules_and_available_exact_package(self):
        name = 'Install available extra modules only when still required'
        self.assertTrue(self.enabled(name))
        self.assertFalse(self.enabled(name, vpn_sidecar_module_reprobe={'results': [{'rc': 0}]}))
        self.assertFalse(self.enabled(name, vpn_sidecar_kernel_packages={'results': [{'rc': 0}, {'rc': 100}]}))
        self.assertFalse(self.enabled(name, vpn_sidecar_install_kernel_modules=False))
        package = self.task(name)['ansible.builtin.apt']['name']
        self.assertEqual(self.render(package), 'linux-modules-extra-6.8.0-100-generic')

    def test_check_mode_requires_present_modules_or_available_matching_packages(self):
        condition = self.task('Explain deferred kernel verification in check mode')['ansible.builtin.assert']['that'][0]
        self.assertTrue(self.render(condition))
        self.assertTrue(self.render(condition, vpn_sidecar_module_reprobe={'results': [{'rc': 0}]}))
        self.assertFalse(self.render(condition, vpn_sidecar_kernel_packages={'results': [{'rc': 100}, {'rc': 100}]}))
        self.assertFalse(self.render(condition, vpn_sidecar_install_kernel_modules=False))

    def test_acceleration_requires_a_real_boolean(self):
        policy = self.tasks[0]['ansible.builtin.assert']['that']
        self.assertFalse(all(self.render(value, vpn_sidecar_l2tp_acceleration='false') for value in policy))

    def test_failed_module_loads_prevent_container_deployment(self):
        condition = self.task('Require all VPN kernel modules before creating containers')['ansible.builtin.assert']['that'][0]
        self.assertTrue(self.render(condition, vpn_sidecar_module_load={'results': [{'rc': 0}]}))
        self.assertFalse(self.render(condition, vpn_sidecar_module_load={'results': [{'rc': 0}, {'rc': 1}]}))


if __name__ == '__main__':
    unittest.main()
