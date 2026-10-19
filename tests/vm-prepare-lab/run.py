#!/usr/bin/env python3
"""Run only on the authorized Linux lab VM; source is a committed Git archive."""
import argparse
import json
import os
from pathlib import Path
import platform
import subprocess
import tempfile
import time
import uuid


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--source", type=Path, required=True, help="absolute extracted git-archive directory")
    parser.add_argument("--sudo", action="store_true", help="use sudo -n for VM Docker access")
    parser.add_argument("--scenario", choices=("full", "ipsec-only", "both"), default="both")
    options = parser.parse_args()
    assert os.environ.get("VPNMODO_AUTHORIZED_VM_PREPARE_LAB") == "1", "explicit authorized-lab flag required"
    assert platform.system() == "Linux" and platform.machine() in ("aarch64", "arm64"), "Ubuntu ARM64 lab VM required"
    assert options.source.is_absolute(), "source must be an absolute private path"
    source = options.source.resolve(strict=True)
    assert not (source / ".git").exists(), "use git archive, never a live checkout"
    assert (source / "roles/vm_prepare/tasks/main.yaml").is_file(), "expected committed collection source"
    assert "," not in str(source), "Docker bind source must not contain commas"
    assert Path("/sys/fs/cgroup/cgroup.controllers").is_file(), "cgroup v2 required; no host mount workaround is permitted"
    modules = ["xfrm_user", "esp4"]
    if options.scenario != "ipsec-only":
        modules += ["ppp_generic", "ppp_async"]
        assert Path("/dev/ppp").is_char_device(), "PPP device must already be prepared on lab VM"
    for module in modules:
        assert Path("/sys/module", module).is_dir(), "required VM module must already be loaded: " + module
    assert Path("/lib/modules", platform.release()).is_dir(), "read-only matching module metadata required"

    evidence = Path(tempfile.mkdtemp(prefix="vpnmodo-vm-prepare-lab-"))
    evidence.chmod(0o700)
    docker = (["sudo", "-n", "--"] if options.sudo else []) + ["docker", "--host", "unix:///var/run/docker.sock"]
    identity = "vpnmodo-vm-prepare-" + uuid.uuid4().hex[:12]
    label = "io.vpnmodo.vm-prepare-lab=" + identity
    owned = []

    def call(args, check=True, timeout=60):
        return subprocess.run(docker + args, check=check, capture_output=True, text=True, timeout=timeout)

    def snapshot(ids=None):
        if ids is None:
            ids = call(["ps", "-aq", "--no-trunc"]).stdout.split()
        if not ids:
            return {}
        result = call(["inspect", "--format", "{{.Id}} {{.State.Status}} {{.State.StartedAt}}", *ids])
        return {line.split()[0]: line.split()[1:] for line in result.stdout.splitlines()}

    before = snapshot()
    before_modules = sorted(line.split()[0] for line in Path("/proc/modules").read_text().splitlines())
    (evidence / "before.json").write_text(json.dumps(before, indent=2) + "\n")
    print("Private lab evidence:", evidence, flush=True)
    try:
        fixture = source / "tests/vm-prepare-lab"
        built = call(["build", "--label", label, "-t", identity, str(fixture)], check=False, timeout=1800)
        (evidence / "build.log").write_text(built.stdout + built.stderr)
        assert built.returncode == 0, "fixture build failed; see private build.log"
        scenarios = ("full", "ipsec-only") if options.scenario == "both" else (options.scenario,)
        for scenario in scenarios:
            name = identity + "-" + scenario
            args = ["create", "--name", name, "--privileged", "--cgroupns=private",
                    "--label", label, "--log-driver", "none",
                    "--tmpfs", "/run", "--tmpfs", "/run/lock",
                    "--mount", "type=bind,src=" + str(source) + ",dst=/src,readonly",
                    "--mount", "type=bind,src=/lib/modules,dst=/lib/modules,readonly"]
            if scenario == "full":
                args += ["--device", "/dev/ppp:/dev/ppp"]
            # Never bind /sys/fs/cgroup, the VM Docker socket, or a host network.
            cid = call(args + [identity]).stdout.strip()
            assert len(cid) == 64 and all(c in "0123456789abcdef" for c in cid), "invalid created container ID"
            owned.append(cid)
            (evidence / "owned.json").write_text(json.dumps({"label": label, "ids": owned, "image": identity}) + "\n")
            inspection = json.loads(call(["inspect", cid]).stdout)[0]
            assert inspection["HostConfig"]["CgroupnsMode"] == "private"
            assert inspection["HostConfig"]["NetworkMode"] != "host"
            assert not any(m["Destination"] in ("/var/run/docker.sock", "/sys/fs/cgroup") for m in inspection["Mounts"])
            call(["start", cid])
            for attempt in range(60):
                ready = call(["exec", cid, "sh", "-c",
                    'test "$(cat /proc/1/comm)" = systemd && systemctl show --property=SystemState --value | grep -Eq "^(running|degraded)$"'], check=False)
                if ready.returncode == 0:
                    break
                if call(["inspect", "--format", "{{.State.Running}}", cid]).stdout.strip() != "true":
                    raise RuntimeError("systemd exited; private writable cgroup delegation is unavailable; do not mount VM cgroup root writable")
                time.sleep(1)
            else:
                raise RuntimeError("systemd did not become ready; no fake-service or writable-host-cgroup fallback is allowed")
            result = call(["exec", "--env", "VPNMODO_VM_PREPARE_LAB=1", cid,
                           "/usr/bin/setpriv", "--bounding-set=-sys_module", "--",
                           "python3", "/opt/lab/inside.py", "--scenario", scenario], check=False, timeout=1800)
            (evidence / (scenario + ".log")).write_text(result.stdout + result.stderr)
            print(result.stdout, end="", flush=True)
            if result.returncode:
                raise RuntimeError(scenario + " failed; private synthetic log: " + str(evidence / (scenario + ".log")))
    finally:
        cleanup_errors = []
        for number, cid in enumerate(owned):
            current = call(["inspect", "--format", '{{index .Config.Labels "io.vpnmodo.vm-prepare-lab"}}', cid], check=False)
            if current.returncode == 0 and current.stdout.strip() == identity:
                call(["cp", cid + ":/tmp/vpnmodo-vm-prepare-evidence", str(evidence / ("ansible-" + str(number)))], check=False)
                removed = call(["rm", "-f", cid], check=False)
                if removed.returncode:
                    cleanup_errors.append(cid)
            else:
                cleanup_errors.append(cid)
        # This tag is unique to this run; never remove unrelated image tags.
        call(["image", "rm", identity], check=False)
        after = snapshot(list(before))
        (evidence / "after.json").write_text(json.dumps(after, indent=2) + "\n")
        assert after == before, "existing VM container identities/start states changed; inspect private before/after evidence"
        assert not cleanup_errors, "exact lab cleanup failed; owned IDs saved privately"
        assert set(snapshot()) == set(before), "unexpected VM container additions or removals during lab run"
        after_modules = sorted(line.split()[0] for line in Path("/proc/modules").read_text().splitlines())
        assert after_modules == before_modules, "shared VM kernel module set changed during run; do not claim isolated kernel-state preservation"
    print("PASS: exact lab cleanup and existing VM container identities/start states preserved", flush=True)


if __name__ == "__main__":
    main()
