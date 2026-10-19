"""Exercise real Ansible role control flow against a synthetic, controller-only target.

The fixture action plugin replaces OS operations; assert/include/set_fact/when/loop
and check-mode handling run in Ansible itself. This does not establish live VM
package, systemd or kernel acceptance.
"""
import importlib.util
import json
import os
from pathlib import Path
import shutil
import subprocess
import sys
import tempfile
import unittest

import yaml

ROOT = Path(__file__).resolve().parents[1]
MOCK_ACTION = r'''
import json
from pathlib import Path
from ansible.plugins.action import ActionBase

class ActionModule(ActionBase):
    def run(self, tmp=None, task_vars=None):
        args = dict(self._task.args)
        kind = args.pop('_fixture_action')
        path = Path(task_vars['fixture_state'])
        state = json.loads(path.read_text())
        check = self._task.check_mode
        if kind != 'setup':
            state['trace'].append({'kind': kind, 'args': args, 'check': check,
                                   'user': self._task.become_user})
        result = {'changed': False}
        if kind == 'setup':
            result['ansible_facts'] = task_vars['fixture_facts']
        elif kind == 'getent':
            if args['key'] != 'operator':
                result.update(failed=True, msg='synthetic account does not exist')
        elif kind == 'apt':
            names = args['name']
            names = names if isinstance(names, list) else [names]
            missing = [name for name in names if name not in state['packages']]
            result['changed'] = bool(missing)
            if not check:
                state['packages'].extend(missing)
                if any(name.startswith('linux-modules') for name in names):
                    state['modules'] = True
                if 'docker.io' in names or 'docker-ce' in names:
                    state['service_exists'] = True
        elif kind == 'package_facts':
            result['ansible_facts'] = {'packages': {name: [] for name in state['packages']}}
        elif kind == 'service_facts':
            result['ansible_facts'] = {'services': {'docker.service': {'state': 'running'}}
                                      if state['service_exists'] else {}}
        elif kind == 'systemd_service':
            result['changed'] = not state['service_ready']
            if not check:
                state['service_ready'] = True
        elif kind == 'user':
            result['changed'] = 'docker' not in state['groups']
            if not check and result['changed']:
                state['groups'].append('docker')
        elif kind == 'copy':
            result['changed'] = state['files'].get(args['dest']) != args['content']
            if not check:
                state['files'][args['dest']] = args['content']
        elif kind == 'stat':
            result['stat'] = {'exists': state['ppp'], 'ischr': state['ppp']}
        elif kind == 'command':
            argv = args['argv']
            actor = None
            if argv[0] == 'runuser':
                actor = argv[2]
                argv = argv[4:]
            uses_sudo = argv[0] == 'sudo'
            result.update(rc=0, stdout='', stderr='')
            if argv[0] == 'sudo':
                if not state['sudo']:
                    result.update(rc=1, failed=True, msg='synthetic sudo refused')
                argv = argv[4:]  # sudo -n -k --
            if argv[0] == 'docker':
                if actor and not uses_sudo and 'docker' not in state['groups']:
                    result.update(rc=1, failed=True, msg='synthetic Docker group denied')
                if 'info' in argv:
                    result['stdout'] = json.dumps({'OSType': 'linux', 'SecurityOptions':
                                                   ['name=rootless'] if state['rootless'] else []})
                elif 'compose' in argv:
                    result['stdout'] = state['compose']
                else:
                    result['stdout'] = 'synthetic version'
                if not any(name in state['packages'] for name in ['docker.io', 'docker-ce']):
                    result.update(rc=127, failed=True)
            elif argv[0] == 'modprobe':
                result.update(rc=0 if state['modules'] else 1,
                              failed=not state['modules'])
            elif argv[0] == 'apt-cache':
                result.update(rc=0 if state['module_packages'] else 100,
                              failed=not state['module_packages'])
            elif argv[0] == 'id':
                result['stdout'] = ' '.join(state['groups'])
        else:
            result.update(failed=True, msg='unknown synthetic action ' + kind)
        path.write_text(json.dumps(state))
        return result
'''


@unittest.skipUnless(importlib.util.find_spec('ansible'), 'requires ansible-core')
class VmPrepareTests(unittest.TestCase):
    def setUp(self):
        self.directory = tempfile.TemporaryDirectory()
        self.addCleanup(self.directory.cleanup)
        self.base = Path(self.directory.name)
        self.collection = self.base / 'collections/ansible_collections/vpn_sidecar/client'
        self.collection.mkdir(parents=True)
        shutil.copytree(ROOT / 'roles', self.collection / 'roles')
        plugins = self.collection / 'plugins/action'
        plugins.mkdir(parents=True)
        (plugins / 'fixture.py').write_text(MOCK_ACTION)
        self.state_path = self.base / 'state.json'
        self.values = {
            'ansible_facts': {'distribution': 'Ubuntu', 'distribution_version': '24.04',
                              'architecture': 'aarch64', 'kernel': '6.8.0-100-generic'},
            'vpnmodo_vm_management_user': 'operator',
            'ansible_user': 'operator',
            'fixture_state': str(self.state_path),
        }
        self.initial = {
            'trace': [], 'packages': ['bash', 'ca-certificates', 'tar', 'kmod', 'util-linux',
                                    'iproute2', 'sudo', 'docker.io',
                                    'docker-compose-v2', 'docker-buildx'],
            'service_exists': True, 'service_ready': True, 'modules': True,
            'module_packages': True, 'groups': ['operator', 'docker'], 'sudo': True,
            'rootless': False, 'compose': '2.40.0', 'ppp': True, 'files': {},
        }
        self.state_path.write_text(json.dumps(self.initial))
        config = self.base / 'ansible.cfg'
        config.write_text('[defaults]\ninterpreter_python=auto_silent\n')
        self.env = dict(os.environ, ANSIBLE_CONFIG=str(config),
                        ANSIBLE_COLLECTIONS_PATH=str(self.base / 'collections'),
                        ANSIBLE_LOCAL_TEMP=str(self.base / 'ansible-tmp'),
                        ANSIBLE_NOCOLOR='1')

    def mock_target_actions(self):
        kinds = {'apt', 'getent', 'command', 'user', 'copy', 'package_facts',
                 'service_facts', 'systemd_service', 'stat'}
        safe = {'ansible.builtin.' + kind for kind in ('assert', 'set_fact', 'debug')}
        files = ['vm_prepare/tasks/main.yaml', 'vpn_sidecar/tasks/prerequisites.yaml']
        for filename in files:
            path = self.collection / 'roles' / filename
            tasks = yaml.safe_load(path.read_text())
            for task in tasks:
                actions = [key for key in task if key.startswith('ansible.')]
                self.assertEqual(len(actions), 1, task)
                action = actions[0]
                if action == 'ansible.builtin.include_role':
                    self.assertEqual(task[action], {'name': 'vpn_sidecar.client.vpn_sidecar',
                                                    'tasks_from': 'prerequisites'})
                elif action not in safe:
                    kind = action.removeprefix('ansible.builtin.')
                    self.assertIn(kind, kinds, 'Refusing an unmocked target action: ' + action)
                    args = task.pop(action) or {}
                    task['vpn_sidecar.client.fixture'] = dict(args, _fixture_action=kind)
            path.write_text(yaml.safe_dump(tasks, sort_keys=False))

    def run_role(self, check=False, syntax=False, overrides=None):
        values = dict(self.values, **(overrides or {}))
        values['fixture_facts'] = values.pop('ansible_facts')
        playbook = self.base / 'prepare.yaml'
        playbook.write_text(yaml.safe_dump([{
            'hosts': 'localhost', 'gather_facts': False, 'vars': values,
            'pre_tasks': [{'name': 'Supply synthetic VM facts',
                           'vpn_sidecar.client.fixture': {'_fixture_action': 'setup'}}],
            'roles': ['vpn_sidecar.client.vm_prepare'],
        }], sort_keys=False))
        argv = [sys.executable, '-m', 'ansible.cli.playbook', '-i', 'localhost,',
                '-c', 'local', str(playbook)]
        if check:
            argv.append('--check')
        if syntax:
            argv.append('--syntax-check')
        result = subprocess.run(argv, env=self.env, capture_output=True, text=True)
        return result.returncode, result.stdout + result.stderr

    def state(self):
        return json.loads(self.state_path.read_text())

    def test_collection_fqcn_syntax_without_customer_settings(self):
        code, output = self.run_role(syntax=True)
        self.assertEqual(code, 0, output)

    def test_preflight_rejects_invalid_inputs_before_target_actions(self):
        self.mock_target_actions()
        invalid = [
            {'vpnmodo_vm_management_user': ''}, {'vpnmodo_vm_management_user': '-root'},
            {'vpnmodo_vm_management_user': 'operator;id'},
            {'vpnmodo_vm_docker_access': 'automatic'}, {'vpnmodo_vm_l2tp': 'false'},
            {'ansible_facts': dict(self.values['ansible_facts'], distribution='Debian')},
            {'ansible_facts': dict(self.values['ansible_facts'], distribution_version='22.04')},
            {'ansible_facts': dict(self.values['ansible_facts'], architecture='x86_64')},
        ]
        for overrides in invalid:
            with self.subTest(overrides=overrides):
                code, output = self.run_role(check=True, overrides=overrides)
                self.assertNotEqual(code, 0, output)
                self.assertIn('VM preparation requires Ubuntu 24.04 ARM64', output)
                self.assertEqual(self.state()['trace'], [], output)

    def test_fresh_apply_and_unchanged_rerun_prepare_only_host(self):
        self.mock_target_actions()
        fresh = dict(self.initial, packages=[], service_exists=False, service_ready=False,
                     modules=False, groups=['operator'])
        self.state_path.write_text(json.dumps(fresh))
        code, output = self.run_role()
        self.assertEqual(code, 0, output)
        state = self.state()
        self.assertIn('docker.io', state['packages'])
        self.assertEqual(state['groups'], ['operator', 'docker'])
        self.assertTrue(state['service_ready'])
        self.assertEqual(state['files'], {'/etc/modules-load.d/vpn-sidecar-vpnmodo-preparation.conf':
                                         'xfrm_user\nesp4\nppp_generic\nppp_async\n'})
        code, output = self.run_role()
        self.assertEqual(code, 0, output)
        self.assertRegex(output, r'changed=0\s')
        commands = [item['args']['argv'] for item in self.state()['trace'] if item['kind'] == 'command']
        self.assertFalse(any(any(value in command for value in ['up', 'run', 'exec', 'build', 'pull'])
                             for command in commands), commands)
        account = [item for item in state['trace'] if item['kind'] == 'user'][0]
        self.assertTrue(account['args']['append'])
        # Even when SSH and management users match, avoid Ansible's skipped
        # become and inherit fresh supplementary groups through root runuser.
        probes = [item for item in state['trace'] if item['kind'] == 'command'
                  and item['args']['argv'][0] == 'runuser']
        self.assertEqual(len(probes), 2)
        self.assertTrue(all(item['user'] == 'root' for item in probes))
        self.assertTrue(all(item['args']['argv'][:4] == ['runuser', '-u', 'operator', '--']
                            for item in probes))

    def test_fresh_check_mode_reports_plan_without_changing_target_state(self):
        self.mock_target_actions()
        fresh = dict(self.initial, packages=[], service_exists=False, service_ready=False,
                     modules=False, groups=['operator'])
        self.state_path.write_text(json.dumps(fresh))
        code, output = self.run_role(check=True)
        self.assertEqual(code, 0, output)
        state = self.state()
        for key, value in fresh.items():
            if key != 'trace':
                self.assertEqual(state[key], value, key)
        self.assertIn('Module load, XFRM and PPP device checks run on apply', output)
        commands = [item['args']['argv'] for item in state['trace'] if item['kind'] == 'command']
        self.assertFalse(any(command[0] == 'modprobe' and '--dry-run' not in command
                             for command in commands))
        self.assertFalse(any(item['kind'] in ['user', 'systemd_service', 'stat']
                             for item in state['trace']))

    def test_sudo_mode_uses_existing_authority_and_skips_group_grant(self):
        self.mock_target_actions()
        code, output = self.run_role(overrides={'vpnmodo_vm_docker_access': 'sudo',
                                               'vpnmodo_vm_l2tp': False})
        self.assertEqual(code, 0, output)
        trace = self.state()['trace']
        self.assertFalse(any(item['kind'] == 'user' for item in trace))
        commands = [item for item in trace if item['kind'] == 'command']
        delegated = [item for item in commands if item['args']['argv'][0] == 'runuser']
        self.assertEqual(len(delegated), 3)
        self.assertTrue(all(item['user'] == 'root' for item in delegated))
        self.assertTrue(all(item['args']['argv'][:8] ==
                            ['runuser', '-u', 'operator', '--', 'sudo', '-n', '-k', '--']
                            for item in delegated))
        self.assertEqual(self.state()['files'], {
            '/etc/modules-load.d/vpn-sidecar-vpnmodo-preparation.conf': 'xfrm_user\nesp4\n'})

    def test_required_capabilities_fail_closed(self):
        self.mock_target_actions()
        cases = [({'rootless': True}, {}, False, 'rootful Linux Docker'),
                 ({'compose': '1.29.2'}, {}, False, 'rootful Linux Docker'),
                 ({'ppp': False}, {}, False, '/dev/ppp is unavailable'),
                 ({'sudo': False}, {'vpnmodo_vm_docker_access': 'sudo'}, True,
                  'synthetic sudo refused'),
                 ({'modules': False, 'module_packages': False}, {}, True,
                  'no matching module package is available'),
                 ({'modules': False, 'module_packages': False}, {}, False,
                  'Required modules are unavailable for running kernel'),
                 ({'packages': self.initial['packages'] + ['podman-docker']}, {}, False,
                  'Docker is mixed or managed outside'),
                 ({}, {'vpnmodo_vm_management_user': 'missing'}, False,
                  'synthetic account does not exist')]
        for state_changes, overrides, check, expected in cases:
            with self.subTest(state=state_changes, check=check):
                self.state_path.write_text(json.dumps(dict(self.initial, **state_changes)))
                code, output = self.run_role(check=check, overrides=overrides)
                self.assertNotEqual(code, 0, output)
                self.assertIn(expected, output)


if __name__ == '__main__':
    unittest.main()
