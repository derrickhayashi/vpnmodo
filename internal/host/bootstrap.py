"""Fixed SSH bootstrap; stdin is data. Never print command output or secrets."""
import json
import fcntl
import os
from pathlib import Path
import re
import subprocess
import sys
import tempfile

NAME = "vpnmodo-controller"
MANAGER = "io.vpnmodo.managed-by"
COMPONENT = "io.vpnmodo.component"
SOCKET = "/var/run/docker.sock"


class SetupError(Exception):
    pass


def docker(*args, code="prerequisites"):
    try:
        result = subprocess.run(["docker", "--host", "unix://" + SOCKET, *args], capture_output=True, timeout=180)
    except (OSError, subprocess.TimeoutExpired):
        raise SetupError(code) from None
    if result.returncode:
        raise SetupError(code)
    return result.stdout


def inspect(kind, name):
    try:
        result = subprocess.run(["docker", "--host", "unix://" + SOCKET, kind, "inspect", name], capture_output=True, timeout=30)
    except (OSError, subprocess.TimeoutExpired):
        raise SetupError("prerequisites") from None
    if result.returncode:
        expected = (b"no such container", b"no such image", b"no such object")
        if any(message in result.stderr.lower() for message in expected):
            return None
        raise SetupError("prerequisites")
    return json.loads(result.stdout)[0]


def setup(params):
    if not {"image", "replace"}.issubset(params) or set(params) - {"image", "replace", "backend_images"} or type(params["replace"]) is not bool:
        raise SetupError("input")
    image = params["image"]
    if not isinstance(image, str) or not re.fullmatch(r"[a-z0-9][a-z0-9._/:@-]{0,254}", image):
        raise SetupError("input")
    backends = params.get("backend_images")
    if backends is not None and (not isinstance(backends, dict) or set(backends) != {"l2tp", "xauth", "application"} or any(not isinstance(v, str) or not re.fullmatch(r"sha256:[0-9a-f]{64}", v) for v in backends.values())):
        raise SetupError("input")
    if json.loads(docker("info", "--format", "{{json .OSType}}")) != "linux":
        raise SetupError("prerequisites")
    docker("compose", "version")
    root = Path.home() / ".local" / "share" / "vpnmodo"
    for parent in (root, *root.parents):
        if parent.is_symlink():
            raise SetupError("storage")
    root.mkdir(parents=True, exist_ok=True, mode=0o700)
    if root.stat().st_uid != os.getuid() or root.stat().st_mode & 0o077:
        raise SetupError("storage")
    state = root / "controller-state"
    state.mkdir(exist_ok=True, mode=0o700)
    if state.is_symlink() or state.stat().st_uid != os.getuid() or state.stat().st_mode & 0o077:
        raise SetupError("storage")
    lock_path = root / ".setup.lock"
    descriptor = os.open(lock_path, os.O_CREAT | os.O_RDWR | os.O_NOFOLLOW, 0o600)
    with os.fdopen(descriptor, "a+b") as lock:
        fcntl.flock(lock, fcntl.LOCK_EX)
        return activate(params, root, state)


def activate(params, root, state):
    image = params["image"]
    existing = inspect("container", NAME)
    if existing:
        labels = existing["Config"].get("Labels") or {}
        if labels.get(MANAGER) != "vpnmodo" or labels.get(COMPONENT) != "controller" or labels.get("com.docker.compose.project") != NAME:
            raise SetupError("ownership")
    desired = inspect("image", image)
    if desired is None:
        docker("pull", image, code="image")
        desired = inspect("image", image)
    if desired is None:
        raise SetupError("image")
    overrides = {}
    for role, image_id in params.get("backend_images", {}).items():
        backend = inspect("image", image_id)
        if backend is None or backend.get("Id") != image_id:
            raise SetupError("image")
        overrides["VPNMODO_" + role.upper() + "_IMAGE"] = image_id
    socket = SOCKET
    if existing:
        mounts = existing.get("Mounts") or []
        if not any(m.get("Type") == "bind" and m.get("Source") == str(root) and m.get("Destination") == str(root) for m in mounts):
            raise SetupError("ownership")
        current_env = existing["Config"].get("Env") or []
        changed = existing["Image"] != desired["Id"] or any(k + "=" + v not in current_env for k, v in overrides.items())
        if changed and not params["replace"]:
            raise SetupError("replace_required")
        if not params["replace"]:
            if existing["State"]["Running"]:
                return {"ok": True, "action": "reused"}
            docker("start", NAME)
            return {"ok": True, "action": "started"}
    service = {
        "image": image,
        "container_name": NAME,
        "user": f"{os.getuid()}:{os.getgid()}",
        "group_add": [str(os.stat(socket).st_gid)],
        "labels": {MANAGER: "vpnmodo", COMPONENT: "controller"},
        "restart": "unless-stopped",
        "read_only": True,
        "network_mode": "none",
        "cap_drop": ["ALL"],
        "security_opt": ["no-new-privileges:true"],
        "tmpfs": [f"/run/vpnmodo:rw,noexec,nosuid,nodev,mode=0700,uid={os.getuid()},gid={os.getgid()}"],
        "environment": {"VPNMODO_STATE_DIR": str(state), **overrides},
        "volumes": [
            {"type": "bind", "source": socket, "target": socket, "bind": {"create_host_path": False}},
            {"type": "bind", "source": str(root), "target": str(root), "bind": {"create_host_path": False}},
        ],
    }
    manifest = root / "controller.compose.json"
    if manifest.is_symlink():
        raise SetupError("storage")
    descriptor, temporary = tempfile.mkstemp(prefix=".controller-", dir=root)
    try:
        with os.fdopen(descriptor, "w") as output:
            json.dump({"services": {"controller": service}}, output)
            output.flush()
            os.fsync(output.fileno())
        os.replace(temporary, manifest)
    finally:
        if os.path.exists(temporary):
            os.unlink(temporary)
    args = ["compose", "--project-name", NAME, "-f", str(manifest), "up", "-d", "--no-deps"]
    if existing:
        args.append("--force-recreate")
    docker(*args, "controller")
    return {"ok": True, "action": "replaced" if existing else "created"}


def main():
    try:
        payload = sys.stdin.buffer.read(4097)
        if len(payload) > 4096:
            raise SetupError("input")
        result = setup(json.loads(payload))
    except SetupError as error:
        result = {"ok": False, "error": str(error)}
    except Exception:
        result = {"ok": False, "error": "setup"}
    print(json.dumps(result))


if __name__ == "__main__":
    main()
