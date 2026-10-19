"""Bootstrap decisions use fake Docker metadata; no live resources are changed."""
import importlib.util
import json
import os
from pathlib import Path
import tempfile
import unittest
from unittest.mock import patch

SOURCE = Path(__file__).resolve().parents[1] / "internal/host/bootstrap.py"
SPEC = importlib.util.spec_from_file_location("controller_bootstrap", SOURCE)
bootstrap = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(bootstrap)


class ControllerBootstrapTests(unittest.TestCase):
    def setUp(self):
        self.directory = tempfile.TemporaryDirectory()
        self.addCleanup(self.directory.cleanup)
        self.home = Path(self.directory.name).resolve()
        self.root = self.home / ".local/share/vpnmodo"
        self.socket = self.home / "docker.sock"
        self.socket.touch()
        self.calls = []
        self.existing = None
        self.image = {"Id": "sha256:" + "a" * 64}
        self.params = {"image": "vpnmodo-controller:test", "replace": False}
        self.stack = []
        for target, value in (("Path.home", lambda: self.home), ("SOCKET", str(self.socket)), ("docker", self.docker), ("inspect", self.inspect)):
            if target == "Path.home":
                context = patch.object(bootstrap.Path, "home", value)
            else:
                context = patch.object(bootstrap, target, value)
            context.start()
            self.addCleanup(context.stop)

    def docker(self, *args, **kwargs):
        self.calls.append(args)
        return b'"linux"' if args[0] == "info" else b""

    def inspect(self, kind, name):
        return self.existing if kind == "container" else self.image

    def owned(self, running=True):
        return {
            "Image": self.image["Id"],
            "Config": {"Labels": {bootstrap.MANAGER: "vpnmodo", bootstrap.COMPONENT: "controller", "com.docker.compose.project": bootstrap.NAME}},
            "Mounts": [{"Type": "bind", "Source": str(self.root), "Destination": str(self.root)}],
            "State": {"Running": running},
        }

    def test_creation_is_private_and_separate_from_vpn_projects(self):
        self.assertEqual(bootstrap.setup(self.params), {"ok": True, "action": "created"})
        manifest = self.root / "controller.compose.json"
        self.assertEqual(manifest.stat().st_mode & 0o777, 0o600)
        service = json.loads(manifest.read_text())["services"]["controller"]
        self.assertEqual(service["network_mode"], "none")
        self.assertTrue(service["read_only"])
        self.assertEqual(service["cap_drop"], ["ALL"])
        self.assertNotIn("ports", service)
        self.assertEqual(service["user"], f"{os.getuid()}:{os.getgid()}")
        self.assertTrue(any(call[:3] == ("compose", "--project-name", "vpnmodo-controller") for call in self.calls))
        self.assertFalse(any("down" in call for call in self.calls))

    def test_stopped_controller_is_repaired_without_api(self):
        self.existing = self.owned(running=False)
        self.assertEqual(bootstrap.setup(self.params)["action"], "started")
        self.assertIn(("start", bootstrap.NAME), self.calls)

    def test_running_controller_is_reused(self):
        self.existing = self.owned()
        self.assertEqual(bootstrap.setup(self.params)["action"], "reused")
        self.assertFalse(any(call[0] == "start" or "up" in call for call in self.calls))

    def test_foreign_controller_cannot_be_replaced(self):
        self.existing = self.owned()
        self.existing["Config"]["Labels"] = {}
        self.params["replace"] = True
        with self.assertRaisesRegex(bootstrap.SetupError, "ownership"):
            bootstrap.setup(self.params)
        self.assertFalse(any("up" in call or "start" in call for call in self.calls))

    def test_image_change_requires_explicit_replacement(self):
        self.existing = self.owned()
        self.existing["Image"] = "sha256:" + "b" * 64
        with self.assertRaisesRegex(bootstrap.SetupError, "replace_required"):
            bootstrap.setup(self.params)
        self.params["replace"] = True
        self.assertEqual(bootstrap.setup(self.params)["action"], "replaced")
        self.assertTrue(any("--force-recreate" in call for call in self.calls))

    def test_symlink_storage_is_rejected(self):
        self.root.parent.mkdir(parents=True)
        self.root.symlink_to(self.home, target_is_directory=True)
        with self.assertRaisesRegex(bootstrap.SetupError, "storage"):
            bootstrap.setup(self.params)

    def test_destination_local_backend_ids_are_written_to_controller(self):
        self.params['backend_images'] = {role:self.image['Id'] for role in ('l2tp','xauth','application')}
        self.assertTrue(bootstrap.setup(self.params)['ok'])
        service = json.loads((self.root/'controller.compose.json').read_text())['services']['controller']
        for role in ('L2TP','XAUTH','APPLICATION'):
            self.assertEqual(service['environment']['VPNMODO_'+role+'_IMAGE'], self.image['Id'])

    def test_new_backend_defaults_require_explicit_replacement(self):
        self.existing = self.owned()
        self.params['backend_images'] = {role:self.image['Id'] for role in ('l2tp','xauth','application')}
        with self.assertRaisesRegex(bootstrap.SetupError,'replace_required'):
            bootstrap.setup(self.params)
