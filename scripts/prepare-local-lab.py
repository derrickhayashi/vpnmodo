#!/usr/bin/env python3
"""Prepare committed source and Linux binaries outside the checkout."""
import argparse
import json
import os
from pathlib import Path
import shutil
import subprocess
import tempfile


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--arch", choices=("arm64", "amd64"), required=True)
    options = parser.parse_args()
    root = Path(__file__).resolve().parents[1]
    if subprocess.check_output(["git", "status", "--porcelain"], cwd=root).strip():
        parser.error("commit the intended source first; the lab uses only committed files")
    directory = Path(tempfile.mkdtemp(prefix="vpnmodo-local-lab-")).resolve()
    if directory.is_relative_to(root):
        directory.rmdir()
        parser.error("set TMPDIR outside the checkout")
    archive = directory / "source.tar"
    subprocess.run(["git", "archive", "--format=tar", "--output=" + str(archive), "HEAD"], cwd=root, check=True)
    source = directory / "work"
    source.mkdir()
    subprocess.run(["tar", "-xf", str(archive), "-C", str(source)], check=True)
    archive.unlink()
    artifacts = directory / "artifacts"
    production = artifacts / "production"
    operations = artifacts / "operations"
    production.mkdir(parents=True)
    operations.mkdir()
    env = dict(os.environ, CGO_ENABLED="0", GOOS="linux", GOARCH=options.arch)
    builds = [
        ["go", "build", "-trimpath", "-o", str(artifacts / "vpnmodo"), "./cmd/vpnmodo"],
        ["go", "build", "-trimpath", "-o", str(production / "vpnmodo-controller"), "./cmd/vpnmodo-controller"],
        ["go", "test", "-c", "-o", str(operations / "vpnmodo-controller"), "./internal/controller"],
    ]
    for args in builds:
        subprocess.run(args, cwd=source, env=env, check=True)
    # Reuse the production runtime stage with the cross-compiled binary. The
    # workstation needs Go; the VM needs only Docker, SSH and the kernel features.
    dockerfile = (source / "containers/controller/Dockerfile").read_text()
    marker = "FROM docker:28.5.1-cli"
    if dockerfile.count(marker) != 1 or "COPY --from=build /out/vpnmodo-controller" not in dockerfile:
        raise RuntimeError("review the changed production runtime stage before preparing this lab")
    runtime = marker + dockerfile.split(marker, 1)[1]
    runtime = runtime.replace("COPY --from=build /out/vpnmodo-controller", "COPY vpnmodo-controller")
    (production / "Dockerfile").write_text(runtime)
    fixture = source / "tests/local-lab"
    for name in ("Dockerfile", "start"):
        shutil.copyfile(fixture / name, directory / name)
    shutil.copyfile(fixture / "run", artifacts / "run")
    revision = subprocess.check_output(["git", "rev-parse", "HEAD"], cwd=root, text=True).strip()
    (directory / "source.json").write_text(json.dumps({"revision": revision, "arch": options.arch}) + "\n")
    print(directory)


if __name__ == "__main__":
    main()
