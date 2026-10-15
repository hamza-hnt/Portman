#!/usr/bin/env python3
"""Install real packages in isolated prefixes; never touch the user's install."""
import hashlib
import json
import os
import pathlib
import platform
import shutil
import socket
import subprocess
import sys
import tempfile

ROOT = pathlib.Path(__file__).resolve().parents[1]
SYSTEM = {"Darwin": "darwin", "Linux": "linux"}[platform.system()]
ARCH = {"arm64": "arm64", "aarch64": "arm64", "x86_64": "amd64"}[platform.machine()]
ASSET = f"portman_{SYSTEM}_{ARCH}.tar.gz"
VERSION = (ROOT / "VERSION").read_text().strip()


def run(*args, env=None, succeeds=True):
    result = subprocess.run(["sh", str(ROOT / "install.sh"), *map(str, args)],
                            cwd=ROOT, env=env, text=True, capture_output=True, timeout=180)
    if (result.returncode == 0) != succeeds:
        raise AssertionError(result.stdout + result.stderr)
    return result.stdout + result.stderr


def verify(binary):
    assert subprocess.check_output([binary, "--version"], text=True).strip() == f"portman {VERSION}"
    # Verify the installed application's real backend, not just its version.
    with socket.socket() as listener:
        listener.bind(("127.0.0.1", 0))
        listener.listen()
        snapshot = json.loads(subprocess.check_output([binary, "--json"], text=True, timeout=15))
        process = next(p for p in snapshot["processes"] if p["pid"] == os.getpid())
        assert any(s["port"] == listener.getsockname()[1] for s in process["sockets"]), process


def main():
    with tempfile.TemporaryDirectory(prefix="portman-install-test-") as temp:
        temp = pathlib.Path(temp)
        package = temp / ASSET
        shutil.copy2(ROOT / "dist" / ASSET, package)
        checksum = pathlib.Path(str(package) + ".sha256")
        shutil.copy2(ROOT / "dist" / (ASSET + ".sha256"), checksum)
        prefix = temp / "prefix with spaces"
        binary = prefix / "bin" / "portman"
        # Fail loudly if a binary install attempts to run Go.
        shim = temp / "shim"
        shim.mkdir()
        marker = temp / "go-was-used"
        (shim / "go").write_text('#!/bin/sh\nprintf used > "$PORTMAN_GO_MARKER"\nexit 93\n')
        (shim / "go").chmod(0o755)
        env = {**os.environ, "PATH": str(shim) + os.pathsep + os.environ["PATH"],
               "PORTMAN_GO_MARKER": str(marker)}

        run("--archive", package, "--prefix", prefix, env=env)
        verify(binary)
        run("--archive", package, "--prefix", prefix, env=env)

        # Exercise release URL selection/download failure without relying on an
        # already-published repository or the network in installation tests.
        (shim / "curl").write_text(f"#!{sys.executable}\n" + '''
import os, pathlib, shutil, sys
args = sys.argv[1:]
url = next(a for a in args if a.startswith("https://"))
base = "https://github.com/portman/fixture/releases/"
routes = (base + "latest/download/", base + "download/v0.2.0/")
if not any(url.startswith(route) for route in routes):
    sys.exit(22)
name = url.rsplit("/", 1)[1]
source = pathlib.Path(os.environ["PORTMAN_PACKAGE_DIR"]) / name
if not source.is_file():
    sys.exit(22)
shutil.copyfile(source, args[args.index("-o") + 1])
''')
        (shim / "curl").chmod(0o755)
        env["PORTMAN_PACKAGE_DIR"] = str(temp)
        run("--repo", "portman/fixture", "--prefix", prefix, env=env)
        run("--repo", "portman/fixture", "--version", "v0.2.0", "--prefix", prefix, env=env)
        verify(binary)
        assert not marker.exists(), "binary installation requires Go"
        installed_hash = hashlib.sha256(binary.read_bytes()).hexdigest()

        run("--repo", "portman/missing", "--prefix", prefix, env=env, succeeds=False)
        assert hashlib.sha256(binary.read_bytes()).hexdigest() == installed_hash

        wrong_package = temp / "portman_wrong_arch.tar.gz"
        shutil.copy2(package, wrong_package)
        shutil.copy2(checksum, pathlib.Path(str(wrong_package) + ".sha256"))
        assert "expected " in run("--archive", wrong_package, "--prefix", prefix, env=env, succeeds=False)

        with package.open("ab") as f:
            f.write(b"corrupt")
        assert "SHA-256 mismatch" in run("--archive", package, "--prefix", prefix, env=env, succeeds=False)
        assert hashlib.sha256(binary.read_bytes()).hexdigest() == installed_hash
        checksum.unlink()
        run("--archive", package, "--prefix", prefix, env=env, succeeds=False)
        run("--prefix", succeeds=False)
        run("--repo", "../bad/path", env=env, succeeds=False)
        run("--source", "--archive", package, env=env, succeeds=False)
        assert not marker.exists()

        source_prefix = temp / "source install"
        run("--source", "--prefix", source_prefix)
        verify(source_prefix / "bin" / "portman")
        print(f"PASS: {SYSTEM}/{ARCH} binary + source install, TCP discovery, release routing, reinstall, checksum/architecture rejection, spaces in paths; binary install uses no Go")


if __name__ == "__main__":
    main()
