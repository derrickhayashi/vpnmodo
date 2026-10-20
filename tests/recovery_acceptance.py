"""Controller recovery policy over real SSH; VPNs are test-binary state only."""
import json
import os
import time

from operations_acceptance import last_json


def wait_connection(cli, alias, predicate):
    deadline = time.monotonic() + 30
    while time.monotonic() < deadline:
        c = last_json(cli("connection", "status", alias, "--json"))
        if predicate(c):
            return c
        time.sleep(0.1)
    raise RuntimeError("synthetic recovery policy did not settle")


def change(path, **fields):
    # Test-only adapter input. No production flags or RPC exposes these controls.
    data = json.loads(path.read_text())
    data.update(fields)
    temporary = path.with_suffix(".control")
    fd = os.open(temporary, os.O_WRONLY | os.O_CREAT | os.O_TRUNC, 0o600)
    with os.fdopen(fd, "w") as stream:
        json.dump(data, stream)
    os.replace(temporary, path)


def create(cli, directory, name):
    source = directory / "recovery-profile.json"
    source.write_text(json.dumps({"schema_version": 1, "name": name, "backend": {"type": "l2tp-ipsec", "config": {"server": "vpn.example.com"}}, "health": {"host": "192.0.2.10", "port": 22}}))
    credentials = {"username": "synthetic-user", "password": "synthetic-password", "psk": "synthetic-shared"}
    definition = json.loads(source.read_text())
    definition["credentials"] = credentials
    source.write_text(json.dumps(definition))
    source.chmod(0o600)
    last_json(cli("profile", "create", name, "--file", str(source), "--json"))
    last_json(cli("connection", "ensure", name, "--wait", "--json"))


def begin(cli, directory, state_root):
    name = "recovery-l2tp"
    create(cli, directory, name)
    actual = state_root / "synthetic-runtime" / (name + ".json")
    change(actual, health="service_unavailable")
    wait_connection(cli, name, lambda c: c.get("recovery", {}).get("health") == "service_unavailable")
    assert json.loads(actual.read_text())["connects"] == 1
    change(actual, health="namespace_detached", forwarding=False)
    wait_connection(cli, name, lambda c: c["observed"] == "connected" and json.loads(actual.read_text())["repairs"] == 2)
    assert json.loads(actual.read_text())["connects"] == 1
    change(actual, health="tunnel_down", established=False, fail_recovery=True)
    c = wait_connection(cli, name, lambda c: c.get("recovery", {}).get("attempts") == 3 and "active_operation" not in c)
    c = wait_connection(cli, name, lambda c: not c["recovery_enabled"] and "active_operation" not in c)
    failed = json.loads(actual.read_text())
    assert failed["connects"] == 4 and not failed["forwarding"] and not failed["established"]
    time.sleep(0.5)
    assert json.loads(actual.read_text())["connects"] == 4
    change(actual, fail_recovery=False)
    last_json(cli("connection", "ensure", name, "--wait", "--json"))
    last_json(cli("connection", "disconnect", name, "--wait", "--json"))
    stopped = json.loads(actual.read_text())["connects"]
    change(actual, health="tunnel_down")
    time.sleep(2.5)
    assert json.loads(actual.read_text())["connects"] == stopped
    c = last_json(cli("connection", "status", name, "--json"))
    assert c["desired"] == "disconnected" and not c["recovery_enabled"]
    # Leave a different profile between retry attempts, then recreate the service.
    name = "cooldown-l2tp"
    create(cli, directory, name)
    actual = state_root / "synthetic-runtime" / (name + ".json")
    change(actual, health="tunnel_down", established=False, fail_recovery=True)
    c = wait_connection(cli, name, lambda c: c.get("recovery", {}).get("attempts") == 1 and "active_operation" not in c)
    print("PASS: service failures preserve sessions, namespace repair avoids login, retries stop at budget, and deliberate stops persist", flush=True)
    return c["recovery"]


def after_restart(cli, state_root, before):
    c = wait_connection(cli, "cooldown-l2tp", lambda c: c["observed"] == "disconnected")
    assert c["recovery"]["attempts"] == before["attempts"]
    assert c["recovery"]["not_before"] == before["not_before"]
    actual = state_root / "synthetic-runtime" / "cooldown-l2tp.json"
    assert json.loads(actual.read_text())["connects"] == 2
    assert not c["recovery_enabled"]
    for name in ("recovery-l2tp", "cooldown-l2tp"):
        last_json(cli("connection", "disconnect", name, "--wait", "--json"))
        last_json(cli("profile", "delete", name, "--revision", "1", "--json"))
    print("PASS: controller recreation retains retry budget/cooldown and never logs into an absent tunnel", flush=True)
