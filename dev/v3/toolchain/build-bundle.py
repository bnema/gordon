#!/usr/bin/env python3
"""Build the Ubuntu amd64 development toolchain once; never install it here."""

import argparse
import gzip
import hashlib
import json
import os
from pathlib import Path
import shutil
import subprocess
import tarfile
import tempfile
import urllib.request

PODMAN = "8303f2e25b675ea7f82099d615c60969aec15870"
PASST = "f8df3f1b228fe19a74a269334fdfe6cc7d0605ce"
EPOCH = 1788354743
PREFIX = "/opt/gordon-v3-toolchain"
TAGS = "apparmor seccomp systemd containers_image_openpgp exclude_graphdriver_btrfs"
HELPERS = {
    "netavark": "39fb540daf7578a793510b27b592b10f17b5d9aa3b07bc5c3f40881f12d590bd",
    "aardvark-dns": "a7bc5252ee0e083f3f46d5bb9dc5f0abdbaa31a70a5a19012412dc8055ac2976",
}


def output(*args):
    return subprocess.check_output(args, text=True).strip()


def digest(path):
    with path.open("rb") as source:
        return hashlib.file_digest(source, "sha256").hexdigest()


def check_source(path, commit):
    if output("git", "-C", str(path), "rev-parse", "HEAD") != commit:
        raise RuntimeError(f"unexpected source commit: {path.name}")
    if output("git", "-C", str(path), "status", "--porcelain", "--untracked-files=no"):
        raise RuntimeError(f"modified tracked source: {path.name}")


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--podman-source", required=True, type=Path)
    parser.add_argument("--passt-source", required=True, type=Path)
    parser.add_argument("--output", required=True, type=Path)
    args = parser.parse_args()
    if os.geteuid() == 0:
        raise RuntimeError("build as the unprivileged development account")
    if output("uname", "-m") != "x86_64":
        raise RuntimeError("only Ubuntu amd64 is validated")
    release = Path("/etc/os-release").read_text()
    if 'ID=ubuntu\n' not in release or 'VERSION_ID="26.04"' not in release:
        raise RuntimeError("Ubuntu 26.04 is required")
    if output("go", "version") != "go version go1.27.1 linux/amd64":
        raise RuntimeError("Go 1.27.1 linux/amd64 is required")
    if args.output.exists():
        raise RuntimeError("output already exists; refuse replacement")
    podman = args.podman_source.resolve()
    passt = args.passt_source.resolve()
    check_source(podman, PODMAN)
    check_source(passt, PASST)
    env = dict(os.environ, SOURCE_DATE_EPOCH=str(EPOCH), LC_ALL="C", TZ="UTC")
    subprocess.run([
        "make", "-B", "bin/podman", "bin/quadlet", f"GO={shutil.which('go')}",
        f"PREFIX={PREFIX}", f"BUILDTAGS={TAGS}", "BUILDFLAGS=-mod=vendor -trimpath",
    ], cwd=podman, env=env, check=True)
    subprocess.run(["make", "-B", "passt", "pasta", "pesto"], cwd=passt, env=env, check=True)
    args.output.parent.mkdir(parents=True, exist_ok=True)
    with tempfile.TemporaryDirectory(prefix="toolchain-stage-", dir=args.output.parent) as tmp:
        stage = Path(tmp)
        for directory in ("bin", "libexec/podman", "share/containers", "licenses"):
            (stage / directory).mkdir(parents=True)
        for source, target in (
            (podman / "bin/podman", "bin/podman"),
            (podman / "bin/quadlet", "libexec/podman/quadlet"),
            (passt / "passt", "bin/passt"),
            (passt / "pasta", "bin/pasta"),
            (passt / "pesto", "bin/pesto"),
        ):
            shutil.copyfile(source, stage / target)
            (stage / target).chmod(0o755)
        for name, sha in HELPERS.items():
            url = f"https://github.com/containers/{name}/releases/download/v2.1.0/{name}.gz"
            with urllib.request.urlopen(url, timeout=60) as response:
                compressed = response.read(64 * 1024 * 1024 + 1)
            if hashlib.sha256(compressed).hexdigest() != sha:
                raise RuntimeError(f"upstream archive checksum mismatch: {name}")
            target = stage / "libexec/podman" / name
            target.write_bytes(gzip.decompress(compressed))
            target.chmod(0o755)
        shutil.copyfile(podman / "LICENSE", stage / "licenses/podman-LICENSE")
        shutil.copytree(passt / "LICENSES", stage / "licenses/passt")
        shutil.copyfile(
            podman / "vendor/go.podman.io/common/pkg/seccomp/seccomp.json",
            stage / "share/containers/seccomp.json",
        )
        manifest = {
            "format": 1, "platform": "ubuntu-26.04-amd64", "prefix": PREFIX,
            "podman_commit": PODMAN, "passt_commit": PASST,
            "go": output("go", "version"), "build_tags": TAGS,
            "source_date_epoch": EPOCH, "helper_archives": HELPERS,
            "build_packages": output("dpkg-query", "-W", "-f=${binary:Package}=${Version}\n"),
            "files": {str(p.relative_to(stage)): digest(p)
                      for p in sorted(stage.rglob("*")) if p.is_file()},
        }
        (stage / "manifest.json").write_text(json.dumps(manifest, indent=2, sort_keys=True) + "\n")
        # Exclusive creation avoids replacing another build's cached artifact.
        with args.output.open("xb") as raw:
            with gzip.GzipFile(filename="", mode="wb", fileobj=raw, mtime=EPOCH) as zipped:
                with tarfile.open(fileobj=zipped, mode="w") as archive:
                    for path in sorted(stage.rglob("*")):
                        info = archive.gettarinfo(str(path), arcname=str(path.relative_to(stage)))
                        info.uid = info.gid = 0
                        info.uname = info.gname = ""
                        info.mtime = EPOCH
                        if path.is_file():
                            with path.open("rb") as source:
                                archive.addfile(info, source)
                        else:
                            archive.addfile(info)
    print(f"sha256:{digest(args.output)}  {args.output.name}")


if __name__ == "__main__":
    main()
