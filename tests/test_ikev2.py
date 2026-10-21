"""Policy/configuration tests. Real XFRM/IKE traffic is tested separately on Linux."""
import importlib.machinery
import importlib.util
import json
import os
from pathlib import Path
import sys
import tempfile
import types
import unittest
from unittest.mock import patch

ROOT = Path(__file__).resolve().parents[1]
loader = importlib.machinery.SourceFileLoader('ikev2_runtime', str(ROOT / 'containers/ipsec-ikev2/scripts/vpn-managed-ikev2'))
spec = importlib.util.spec_from_loader(loader.name, loader)
runtime = importlib.util.module_from_spec(spec)
loader.exec_module(runtime)


class RuntimeTests(unittest.TestCase):
    def test_config_has_exact_ordered_policies_trust_and_separate_identities(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            for name, value in {'username': 'synthetic-user', 'password': 'synthetic-password', 'ca_cert': (ROOT / 'internal/profile/testdata/ikev2-ca.txt').read_text()}.items():
                (root / name).write_text(value)
            env = {'VPN_SERVER': '203.0.113.1', 'VPN_SERVER_IDENTITY': 'vpn.example.test', 'VPN_CLIENT_IDENTITY': 'client@example.test', 'VPN_EAP_IDENTITY': 'synthetic-user', 'VPN_IKE_PROPOSALS': 'aes128-sha256-modp2048,aes256-sha256-modp2048', 'VPN_ESP_PROPOSALS': 'aes128gcm16,aes256-sha256', **{'VPN_' + key.upper() + '_FILE': str(root / key) for key in ('username', 'password', 'ca_cert')}}
            with patch.dict(os.environ, env, clear=True), patch.object(runtime, 'CONF', root / 'swanctl'):
                config = runtime.configuration()
                self.assertIn('proposals = ' + env['VPN_IKE_PROPOSALS'] + '\n', config)
                self.assertIn('esp_proposals = ' + env['VPN_ESP_PROPOSALS'] + '\n', config)
                self.assertNotIn('default', config)
                for expected in ('version = 2', 'auth = eap-mschapv2', 'auth = pubkey', 'id = "vpn.example.test"', 'id = "client@example.test"', 'eap_id = "synthetic-user"', 'local_ts = dynamic', 'remote_ts = 0.0.0.0/0', 'vips = 0.0.0.0', 'dpd_action = clear', 'keyingtries = 1'):
                    self.assertIn(expected, config)
                self.assertNotIn('synthetic-password', config)
                self.assertEqual(len(list((root / 'swanctl/x509ca').glob('*'))), 1)
                for bad in ('default', 'aes256-sha256-modp2048!', 'aes256-sha256-modp2048,default', 'aes256-sha1-modp1024'):
                    os.environ['VPN_IKE_PROPOSALS'] = bad
                    with self.assertRaises(ValueError):
                        runtime.configuration()

    def test_diagnostic_classifier_never_retains_peer_or_transcript(self):
        cases = {
            b'authentication of synthetic-peer with RSA successful': None,
            b'authentication of synthetic-peer (myself) with EAP': None,
            b'authentication of synthetic-peer failed': 'authentication_failed',
            b'received AUTHENTICATION_FAILED notify error': 'authentication_failed',
            b'EAP method EAP_MSCHAPV2 failed': 'authentication_failed',
            b'received EAP_FAILURE, EAP authentication failed': 'authentication_failed',
            b'constraint check failed: identity synthetic-peer required': 'server_identity_mismatch',
            b'no matching peer config found': 'authentication_failed',
            b'no trusted RSA public key found for synthetic-peer': 'certificate_untrusted',
            b'certificate has expired': 'certificate_expired',
            b'certificate synthetic-subject has expired': 'certificate_expired',
            b'received NO_PROPOSAL_CHOSEN notify error': 'proposal_mismatch',
            b'raw unrecognized synthetic text': None,
        }
        for line, expected in cases.items():
            self.assertEqual(runtime.classify(line), expected)

    def test_authentication_requires_established_ike_and_installed_tunnel_child_and_vip(self):
        child = {'state': b'INSTALLED', 'reqid': b'42', 'mode': b'TUNNEL'}
        sa = {'state': b'ESTABLISHED', 'version': b'2', 'local-vips': [b'198.51.100.10'], 'child-sas': {'vpn-1': child}}
        class Socket:
            def __init__(self, *_): pass
            def __enter__(self): return self
            def __exit__(self, *_): pass
            def settimeout(self, *_): pass
            def connect(self, *_): pass
        class Session:
            def __init__(self, *_): pass
            def list_sas(self, *_): return iter([{'vpn': sa}])
        with patch.dict(sys.modules, {'vici': types.SimpleNamespace(Session=Session)}), patch.object(runtime.socket, 'socket', Socket):
            self.assertTrue(runtime.authenticated())
            for target, key, bad in ((sa, 'state', b'CONNECTING'), (sa, 'version', b'1'), (sa, 'local-vips', []), (child, 'state', b'REKEYING'), (child, 'reqid', b'7'), (child, 'mode', b'TRANSPORT')):
                good = target[key]; target[key] = bad
                self.assertFalse(runtime.authenticated())
                target[key] = good

    def test_enforcement_and_live_kernel_policy_are_mandatory(self):
        state = 'src 192.0.2.3 dst 203.0.113.1 proto esp spi 0x00000001 reqid 42 mode tunnel output-mark 0x76706e6d'
        policy = 'src 198.51.100.10/32 dst 0.0.0.0/0 dir out tmpl src 192.0.2.3 dst 203.0.113.1 proto esp reqid 42 mode tunnel'
        def command(*args):
            return (state if args[-1] == 'state' else policy).encode()
        with patch.dict(os.environ, {'VPN_SERVER': '203.0.113.1'}), patch.object(runtime, 'authenticated', return_value=True), patch.object(runtime, 'command', side_effect=command) as cmd:
            self.assertTrue(runtime.protected())
            self.assertEqual(cmd.call_args_list[0].args, ('/usr/local/bin/vpn-enforce', 'check'))
            state = state.replace('0x76706e6d', '0x0')
            self.assertFalse(runtime.protected())
            state = state.replace('0x0', '0x76706e6d'); policy = ''
            self.assertFalse(runtime.protected())


if __name__ == '__main__':
    unittest.main()
