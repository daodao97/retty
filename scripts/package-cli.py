#!/usr/bin/env python3
"""Build a GUI-free CLI and bundle its pinned, hash-verified VT library."""
import argparse
import hashlib
import json
import os
from pathlib import Path
import shutil
import subprocess
import tarfile
import tempfile
import urllib.request

ROOT = Path(__file__).resolve().parent.parent
TARGETS = ("linux/amd64", "linux/arm64", "darwin/amd64", "darwin/arm64")
# This APNs client uses only the standard library. Other MyGo packages remain
# forbidden, including any UI dependency introduced transitively by this one.
HEADLESS_MYGO_PACKAGES = {"github.com/egoist/mygo/push/apns"}


def validate_dependencies(dependencies):
    forbidden = [name for name in dependencies.splitlines()
                 if (name == "github.com/egoist/mygo" or name.startswith("github.com/egoist/mygo/"))
                 and name not in HEADLESS_MYGO_PACKAGES or name == "retty/internal/terminal"]
    if forbidden:
        raise RuntimeError("CLI unexpectedly depends on the graphics/UI runtime: " + ", ".join(forbidden))


def native_library(spec, cache):
    dest = cache / spec["sha256"] / spec["name"]
    if dest.is_file() and hashlib.sha256(dest.read_bytes()).hexdigest() == spec["sha256"]:
        return dest
    dest.parent.mkdir(parents=True, exist_ok=True)
    request = urllib.request.Request(spec["url"], headers={"User-Agent": "Retty CLI build"})
    with urllib.request.urlopen(request, timeout=120) as response:
        data = response.read(64 * 1024 * 1024 + 1)
    if len(data) > 64 * 1024 * 1024 or hashlib.sha256(data).hexdigest() != spec["sha256"]:
        raise RuntimeError(f"native library checksum mismatch: {spec['name']}")
    with tempfile.NamedTemporaryFile(dir=dest.parent, delete=False) as temp:
        temp.write(data)
        pending = Path(temp.name)
    try:
        pending.chmod(0o755)
        pending.replace(dest)
    finally:
        pending.unlink(missing_ok=True)
    return dest


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--platform", choices=(*TARGETS, "all"), default=None)
    parser.add_argument("--output", type=Path, default=ROOT / "build" / "cli")
    args = parser.parse_args()
    env = dict(os.environ, GOWORK="off", CGO_ENABLED="0")
    target = args.platform or "/".join(subprocess.check_output(["go", "env", "GOOS", "GOARCH"], env=env, text=True).split())
    if target != "all" and target not in TARGETS:
        parser.error(f"unsupported platform {target}")
    version = json.loads((ROOT / "mygo.json").read_text())["version"]
    commit = subprocess.check_output(["git", "rev-parse", "--short=12", "HEAD"], cwd=ROOT, text=True).strip()
    if subprocess.check_output(["git", "status", "--porcelain"], cwd=ROOT, text=True).strip():
        commit += "-dirty"
    manifest = json.loads((ROOT / "internal/terminal/internal/library/desktop/mygo-plugin.json").read_text())
    files = next(lib["files"] for lib in manifest["libraries"] if lib["name"] == "libghostty-vt")
    output = args.output.resolve()
    output.mkdir(parents=True, exist_ok=True)
    for platform in TARGETS if target == "all" else (target,):
        goos, goarch = platform.split("/")
        platform_env = dict(env, GOOS=goos, GOARCH=goarch)
        dependencies = subprocess.check_output(["go", "list", "-tags", "retty_cli", "-deps", "./cmd/retty"], cwd=ROOT, env=platform_env, text=True)
        validate_dependencies(dependencies)
        name = f"retty-{goos}-{goarch}"
        package = output / name
        package.mkdir(exist_ok=True)
        subprocess.run(["go", "build", "-tags", "retty_cli", "-trimpath", "-ldflags", f"-s -w -X main.version={version} -X main.commit={commit}", "-o", str(package / "retty"), "./cmd/retty"], cwd=ROOT, env=platform_env, check=True)
        spec = files[f"{goos}-{goarch}"]
        library = native_library(spec, ROOT / ".mygo" / "cli-natives")
        shutil.copy2(library, package / spec["name"])
        shutil.copy2(ROOT / "docs" / "cli.md", package / "README.md")
        archive = output / f"{name}.tar.gz"
        with tarfile.open(archive, "w:gz") as tar:
            for entry in ("retty", spec["name"], "README.md"):
                tar.add(package / entry, arcname=f"{name}/{entry}")
        checksum = hashlib.sha256(archive.read_bytes()).hexdigest()
        archive.with_suffix(archive.suffix + ".sha256").write_text(f"{checksum}  {archive.name}\n")
        print(f"Built {archive}", flush=True)


if __name__ == "__main__":
    main()
