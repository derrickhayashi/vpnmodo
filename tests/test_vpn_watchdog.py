"""Exercise recovery decisions without changing a live VPN or contacting devices."""

import json
import os
from pathlib import Path
import subprocess
import time

WATCHDOG = Path(__file__).resolve().parents[1] / "watchdog/vpn-watchdog"


def run_watchdog(tmp_path, *, health="healthy", status="running", detached=False,
                 cooldown=False, application_status="running", mode="auto",
                 reachable=True, recovery_succeeds=True, checks=1, details=False, duplicate_vpn=False):
    """Fake only host/container tools; execute the actual watchdog shell logic."""
    tools = tmp_path / "bin"
    tools.mkdir()
    state_dir = tmp_path / "state"
    state_dir.mkdir()
    state_file = tmp_path / "containers.json"
    state_file.write_text(json.dumps({"health": health, "status": status,
                                    "application_status": application_status}))
    calls_file = tmp_path / "calls.jsonl"
    docker = tools / "docker"
    docker.write_text("""#!/usr/bin/env python3
import json, os, sys
from pathlib import Path
args = sys.argv[1:]
path = Path(os.environ['MOCK_CONTAINERS'])
state = json.loads(path.read_text())
with open(os.environ['MOCK_CALLS'], 'a') as calls:
    calls.write(json.dumps(args) + '\\n')
if args[0] == 'compose':
    op = args[3]
    if op == 'ps':
        print('vpn-old\\nvpn' if args[-1] == 'vpn' and os.environ['MOCK_DUPLICATE'] == '1' else args[-1])
    elif op == 'up' and args[-1] == 'vpn' and os.environ['MOCK_RECOVERY'] == '1':
        state['health'] = 'healthy'
        path.write_text(json.dumps(state))
elif args[0] == 'inspect':
    fmt, target = args[2:4]
    if 'Health' in fmt:
        print(state['health'])
    elif 'Status' in fmt:
        print(state['status'] if target == 'vpn' else state['application_status'])
elif args[0] == 'exec':
    if args[2] == 'readlink':
        print('net:2' if args[1] == 'application' and os.environ['MOCK_DETACHED'] == '1' else 'net:1')
    else:
        sys.exit(0 if os.environ['MOCK_REACHABLE'] == '1' else 1)
""")
    for name, content in {
        "flock": "exit 0\n",
        "sleep": "exit 0\n",
    }.items():
        (tools / name).write_text("#!/bin/sh\n" + content)
    for tool in tools.iterdir():
        tool.chmod(0o755)
    if cooldown:
        (state_dir / "last-recovery").write_text(str(int(time.time())))
    env = {**os.environ, "PATH": str(tools) + os.pathsep + os.environ["PATH"],
           "VPN_WATCHDOG_STATE_DIR": str(state_dir),
           "VPN_WATCHDOG_MODE": mode, "VPN_HEALTH_HOST": "192.0.2.10",
           "MOCK_REACHABLE": '1' if reachable else '0',
           "MOCK_DETACHED": '1' if detached else '0',
           "MOCK_RECOVERY": '1' if recovery_succeeds else '0',
           "MOCK_DUPLICATE": '1' if duplicate_vpn else '0',
           "VPN_PROJECT_DIR": str(tmp_path),
           "MOCK_CONTAINERS": str(state_file), "MOCK_CALLS": str(calls_file)}
    outputs = []
    results = []
    for _ in range(checks):
        result = subprocess.run(["sh", str(WATCHDOG)], env=env,
                                capture_output=True, text=True, timeout=15)
        outputs.append(result.stdout)
        results.append(result.returncode)
    calls = [json.loads(line) for line in calls_file.read_text().splitlines()]
    operations = [args[3:] for args in calls if args[0] == "compose"
                  and args[3] in {"stop", "up"}]
    if details:
        return operations, ''.join(outputs), results, state_dir
    assert all(code == 0 for code in results), outputs
    return operations


def test_healthy_shared_namespace_is_unchanged(tmp_path):
    assert run_watchdog(tmp_path) == []


def test_unhealthy_recovers_vpn_then_forwarding(tmp_path):
    assert run_watchdog(tmp_path, health="unhealthy") == [
        ["stop", "application"],
        ["up", "-d", "--no-deps", "--force-recreate", "vpn"],
        ["up", "-d", "--no-deps", "--force-recreate", "application"],
    ]


def test_cooldown_prevents_restart_loop(tmp_path):
    assert run_watchdog(tmp_path, health="unhealthy", cooldown=True) == []


def test_starting_vpn_keeps_startup_grace(tmp_path):
    assert run_watchdog(tmp_path, health="starting") == []


def test_stopped_vpn_is_not_revived(tmp_path):
    assert run_watchdog(tmp_path, status="exited") == []


def test_operator_stopped_forwarding_is_preserved(tmp_path):
    assert run_watchdog(tmp_path, application_status="exited", cooldown=True) == []


def test_detached_forwarding_only_recreates_application(tmp_path):
    assert run_watchdog(tmp_path, detached=True) == [
        ["up", "-d", "--no-deps", "--force-recreate", "application"],
    ]


def test_manual_mode_reports_failure_without_reconnection(tmp_path):
    operations, output, results, state = run_watchdog(
        tmp_path, mode="manual", health="unhealthy", checks=2, details=True)
    assert operations == []
    assert results == [0, 0]
    assert output.count('fresh MFA code') == 1
    assert (state / 'status').read_text().strip() == 'manual-required'


def test_manual_mode_probes_endpoint_even_with_healthy_ipsec_policy(tmp_path):
    operations, output, _, state = run_watchdog(
        tmp_path, mode="manual", reachable=False, details=True)
    assert operations == []
    assert 'manual reconnect' in output
    assert (state / 'status').read_text().strip() == 'manual-required'


def test_manual_healthy_mode_does_not_repair_containers(tmp_path):
    operations, output, _, state = run_watchdog(
        tmp_path, mode="manual", detached=True, details=True)
    assert operations == []
    assert 'private endpoint is reachable' in output
    assert (state / 'status').read_text().strip() == 'healthy'


def test_failed_recovery_leaves_forwarding_stopped(tmp_path):
    operations, output, results, state = run_watchdog(
        tmp_path, health="unhealthy", recovery_succeeds=False, details=True)
    assert results == [1]
    assert operations == [["stop", "application"],
                          ["up", "-d", "--no-deps", "--force-recreate", "vpn"]]
    assert (state / 'forwarding-pending').exists()
    assert 'forwarding stays stopped' in output


def test_replacement_waits_for_a_single_vpn_container(tmp_path):
    operations, output, results, _ = run_watchdog(
        tmp_path, duplicate_vpn=True, details=True)
    assert operations == []
    assert results == [0]
    assert 'replacement is in progress' in output
