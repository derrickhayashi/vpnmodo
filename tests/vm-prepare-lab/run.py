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
            listed = call(["ps", "-aq", "--no-trunc"], check=False)
            if listed.returncode:
                raise RuntimeError("Docker container listing failed")
            ids = listed.stdout.split()
        if not ids:
            return {}
        result = call(["inspect", "--format", "{{.Id}} {{.State.Status}} {{.State.StartedAt}}", *ids], check=False)
        if result.returncode:
            # Docker still returns surviving requested IDs when others vanished.
            # Missing baseline resources are a preservation failure below, not a
            # reason to discard survivors or skip the remaining diagnostics.
            messages = [line.lower() for line in result.stderr.splitlines() if line.strip()]
            if not messages or not all("no such object" in line or "no such container" in line for line in messages):
                raise RuntimeError("Docker state snapshot failed for a reason other than missing containers")
        rows = [line.split() for line in result.stdout.splitlines() if line.strip()]
        if any(len(row) != 3 or row[0] not in ids for row in rows):
            raise RuntimeError("Docker state snapshot returned an unexpected identity/state format")
        return {row[0]: row[1:] for row in rows}

    before = snapshot()
    before_modules = sorted(line.split()[0] for line in Path("/proc/modules").read_text().splitlines())
    (evidence / "before.json").write_text(json.dumps(before, indent=2) + "\n")
    (evidence / "before_modules.json").write_text(json.dumps(before_modules, indent=2) + "\n")
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
            try:
                current = call(["inspect", "--format", '{{index .Config.Labels "io.vpnmodo.vm-prepare-lab"}}', cid], check=False)
            except (OSError, subprocess.TimeoutExpired):
                cleanup_errors.append({"id": cid, "reason": "ownership inspection failed"})
                continue
            if current.returncode != 0 or current.stdout.strip() != identity:
                cleanup_errors.append({"id": cid, "reason": "ownership could not be verified"})
                continue
            try:
                copied = evidence / ("ansible-" + str(number))
                call(["cp", cid + ":/tmp/vpnmodo-vm-prepare-evidence", str(copied)], check=False)
                if options.sudo and copied.exists() and copied.stat().st_uid != os.getuid():
                    # sudo docker cp creates root-owned files. Change ownership
                    # only in this exact copied synthetic directory, never follow
                    # symlinks, and leave all unrelated runtime evidence alone.
                    ownership = subprocess.run(["sudo", "-n", "--", "chown", "--no-dereference", "--recursive",
                                                str(os.getuid()) + ":" + str(os.getgid()), "--", str(copied)],
                                               capture_output=True, text=True, timeout=60)
                    if ownership.returncode:
                        print("Copied synthetic evidence requires sudo access:", copied, flush=True)
            except (OSError, subprocess.TimeoutExpired):
                print("Synthetic evidence copy incomplete for owned lab container", flush=True)
            # Failed evidence collection must never bypass exact owned cleanup.
            try:
                removed = call(["rm", "-f", cid], check=False)
                if removed.returncode:
                    cleanup_errors.append({"id": cid, "reason": "container removal failed"})
            except (OSError, subprocess.TimeoutExpired):
                cleanup_errors.append({"id": cid, "reason": "container removal command failed"})

        preservation_errors = []
        details = {"cleanup_errors": cleanup_errors}
        if cleanup_errors:
            preservation_errors.append("exact owned-container cleanup could not be verified")
        # This tag is unique to this run; never remove unrelated image tags.
        try:
            removed_image = call(["image", "rm", identity], check=False)
            if removed_image.returncode and call(["image", "inspect", "--format", "{{.Id}}", identity], check=False).returncode == 0:
                preservation_errors.append("unique lab image tag remains")
        except (OSError, subprocess.TimeoutExpired):
            preservation_errors.append("unique lab image cleanup could not be verified")
        try:
            after = snapshot(list(before))
            (evidence / "after.json").write_text(json.dumps(after, indent=2) + "\n")
            details["missing_baseline_ids"] = sorted(set(before) - set(after))
            details["changed_baseline_ids"] = sorted(cid for cid in set(before) & set(after) if before[cid] != after[cid])
            if after != before:
                preservation_errors.append("existing VM containers changed (missing=" + str(len(details["missing_baseline_ids"]))
                                           + ", state/start=" + str(len(details["changed_baseline_ids"])) + ")")
        except (OSError, subprocess.TimeoutExpired, RuntimeError):
            preservation_errors.append("baseline container preservation snapshot unavailable")
        try:
            remaining = snapshot()
            (evidence / "remaining.json").write_text(json.dumps(remaining, indent=2) + "\n")
            details["remaining_owned_ids"] = sorted(set(remaining) & set(owned))
            details["added_ids"] = sorted(set(remaining) - set(before))
            details["removed_ids"] = sorted(set(before) - set(remaining))
            if details["remaining_owned_ids"]:
                preservation_errors.append("owned lab containers remain after cleanup")
            if set(remaining) != set(before):
                preservation_errors.append("VM container membership changed (added=" + str(len(details["added_ids"]))
                                           + ", removed=" + str(len(details["removed_ids"])) + ")")
        except (OSError, subprocess.TimeoutExpired, RuntimeError):
            preservation_errors.append("post-cleanup container membership unavailable")
        try:
            after_modules = sorted(line.split()[0] for line in Path("/proc/modules").read_text().splitlines())
            (evidence / "after_modules.json").write_text(json.dumps(after_modules, indent=2) + "\n")
            details["added_modules"] = sorted(set(after_modules) - set(before_modules))
            details["removed_modules"] = sorted(set(before_modules) - set(after_modules))
            if after_modules != before_modules:
                preservation_errors.append("shared VM kernel module set changed")
        except OSError:
            preservation_errors.append("post-run kernel module snapshot unavailable")
        details["errors"] = preservation_errors
        (evidence / "preservation.json").write_text(json.dumps(details, indent=2) + "\n")
        if preservation_errors:
            raise RuntimeError("Lab preservation failed: " + "; ".join(preservation_errors)
                               + ". Private diagnostics: " + str(evidence))
    print("PASS: exact lab cleanup and existing VM container identities/start states preserved", flush=True)


if __name__ == "__main__":
    main()
