"""Synthetic-only SSH lifecycle acceptance, called from the isolated VM fixture."""
import concurrent.futures
import json
import select
import subprocess
import time

CODE = "9876543210"  # Synthetic test input only, never a real OTP.


def last_json(result):
    assert CODE.encode() not in result.stdout + result.stderr
    records = [json.loads(line) for line in result.stdout.splitlines() if line]
    return records[-1] if records else None


def wait_status(cli, operation_id, state):
    deadline = time.monotonic() + 15
    while time.monotonic() < deadline:
        snapshot = last_json(cli("operation", "status", operation_id, "--host", "example", "--json"))
        if snapshot["state"] == state:
            return snapshot
        time.sleep(0.05)
    raise RuntimeError("synthetic operation did not reach expected state")


def rpc(bridge, method, params):
    process = subprocess.Popen(bridge, stdin=subprocess.PIPE, stdout=subprocess.PIPE, stderr=subprocess.DEVNULL)
    try:
        def call(number, name, arguments):
            process.stdin.write(json.dumps({"jsonrpc": "2.0", "id": number, "method": name, "params": arguments}).encode() + b"\n")
            process.stdin.flush()
            if not select.select([process.stdout], [], [], 10)[0]:
                raise RuntimeError("synthetic RPC timed out")
            result = process.stdout.readline()
            assert CODE.encode() not in result
            return json.loads(result)
        assert "result" in call(1, "system.hello", {"api": {"major": 1, "minor": 0}})
        result = call(2, method, params)
        process.stdin.close()
        assert process.wait(timeout=10) == 0
        return result
    finally:
        if process.poll() is None:
            process.kill()
            process.wait()
        process.stdout.close()


def begin(cli, bridge, cli_command, directory, state_root):
    first = last_json(cli("connection", "ensure", "example-profile", "--wait", "--json"))
    assert first["state"] == "succeeded"
    last_json(cli("connection", "ensure", "example-profile", "--wait", "--json"))
    actual = state_root / "synthetic-runtime" / "example-l2tp.json"
    assert json.loads(actual.read_text())["connects"] == 1
    snapshot = last_json(cli("profile", "inspect", "example-profile", "--json"))
    profile_file = directory / "operation-profile.json"
    profile_file.write_text(json.dumps(snapshot["profile"]))
    refused = cli("profile", "update", "example-profile", "--file", str(profile_file), "--revision", "3", required=False)
    assert refused.returncode != 0 and b"disconnect" in refused.stderr
    credentials = {"username": "synthetic-user", "password": "synthetic-password", "psk": "synthetic-shared"}

    def create(alias, name):
        definition = {"schema_version": 1, "name": name, "backend": {"type": "ipsec-xauth", "config": {"server": "192.0.2.1"}}, "health": {"host": "192.0.2.10", "port": 22}}
        profile_file.write_text(json.dumps(definition))
        last_json(cli("profile", "create", alias, "--host", "example", "--file", str(profile_file), "--credentials-stdin", "--json", data=json.dumps(credentials).encode()))

    create("mfa-profile", "interactive-mfa")
    pending = last_json(cli("connection", "ensure", "mfa-profile", "--json"))
    pending = wait_status(cli, pending["id"], "awaiting_mfa")
    # The initiating SSH command has exited. Concurrent new SSH callers join it.
    with concurrent.futures.ThreadPoolExecutor(max_workers=4) as pool:
        joined = list(pool.map(lambda _: last_json(cli("connection", "ensure", "mfa-profile", "--json"))["id"], range(8)))
    assert set(joined) == {pending["id"]}
    watched = subprocess.Popen([*cli_command, "operation", "watch", pending["id"], "--host", "example", "--json"], stdout=subprocess.PIPE, stderr=subprocess.PIPE)
    try:
        if not select.select([watched.stdout], [], [], 10)[0]:
            raise RuntimeError("operation watch did not subscribe")
        initial = watched.stdout.readline()
        assert json.loads(initial)["state"] == "awaiting_mfa"
        params = {"operation_id": pending["id"], "challenge_id": pending["challenge"]["id"], "code": CODE}
        assert "result" in rpc(bridge, "operation.submit_mfa", params)
        assert "error" in rpc(bridge, "operation.submit_mfa", params)
        output, errors = watched.communicate(timeout=15)
        assert watched.returncode == 0 and CODE.encode() not in initial + output + errors
        assert json.loads(output.splitlines()[-1])["state"] == "succeeded"
    finally:
        if watched.poll() is None:
            watched.kill()
            watched.wait()
    last_json(cli("connection", "disconnect", "mfa-profile", "--wait", "--json"))
    cancelled = last_json(cli("connection", "ensure", "mfa-profile", "--json"))
    wait_status(cli, cancelled["id"], "awaiting_mfa")
    assert last_json(cli("operation", "cancel", cancelled["id"], "--host", "example", "--json"))["state"] == "cancelled"
    last_json(cli("connection", "disconnect", "mfa-profile", "--wait", "--json"))
    create("pending-profile", "pending-mfa")
    interrupted = last_json(cli("connection", "ensure", "pending-profile", "--json"))
    wait_status(cli, interrupted["id"], "awaiting_mfa")
    print("PASS: SSH-independent operations, concurrent join/session reuse, subscriptions, cancellation and single-use MFA", flush=True)
    return interrupted["id"]


def after_restart(cli, state_root, interrupted_id):
    interrupted = wait_status(cli, interrupted_id, "failed")
    assert interrupted["failure"]["code"] == -32008 and "challenge" not in interrupted
    pending = json.loads((state_root / "synthetic-runtime" / "pending-mfa.json").read_text())
    healthy = json.loads((state_root / "synthetic-runtime" / "example-l2tp.json").read_text())
    assert pending["connects"] == 1 and healthy["connects"] == 1 and healthy["established"]
    last_json(cli("connection", "disconnect", "example-profile", "--wait", "--json"))
    last_json(cli("connection", "disconnect", "pending-profile", "--wait", "--json"))
    connection = last_json(cli("connection", "status", "example-profile", "--json"))
    assert connection["desired"] == "disconnected" and connection["observed"] == "disconnected" and not connection["recovery_enabled"]
    journal = state_root / "operations.db"
    assert journal.stat().st_mode & 0o777 == 0o600
    data = journal.read_bytes()
    for value in (CODE, "synthetic-user", "synthetic-password", "synthetic-shared"):
        assert value.encode() not in data
    last_json(cli("profile", "delete", "mfa-profile", "--revision", "1", "--json"))
    last_json(cli("profile", "delete", "pending-profile", "--revision", "1", "--json"))
    print("PASS: restart reconciles intent/established sessions, interrupts MFA without retry, and journals no secrets", flush=True)
