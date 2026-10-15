#!/usr/bin/env python3
"""Real Docker + PTY test. Only stop/remove containers created by this invocation.

Requires Docker and python:3.13.7-alpine (or PORTMAN_DOCKER_TEST_IMAGE).
Runs explicitly, never as a side effect of the unit test suite.
"""
import fcntl
import json
import os
import pathlib
import pty
import re
import select
import socket
import struct
import subprocess
import sys
import termios
import time
import uuid


def main():
    binary = str(pathlib.Path(sys.argv[1] if len(sys.argv) > 1 else "bin/portman").resolve())
    cli = os.environ.get("PORTMAN_DOCKER_CLI", "docker")
    endpoint = os.environ.get("DOCKER_HOST") if not os.environ.get("DOCKER_CONTEXT") else None
    if not endpoint:
        endpoint = json.loads(subprocess.check_output(
            [cli, "context", "inspect", "--format", "{{json .Endpoints.docker.Host}}"], text=True))
    assert endpoint.startswith("unix:///"), "test requires a local Unix-socket Docker context"
    env = {k: v for k, v in os.environ.items() if k not in ("DOCKER_CONTEXT", "DOCKER_HOST")}
    env["DOCKER_HOST"] = endpoint

    def docker(*args):
        return subprocess.check_output([cli, "--host", endpoint, *args], env=env, text=True).strip()

    project = "portman-test-" + uuid.uuid4().hex[:10]
    created = []
    fixture = """
import signal, socket, sys
signal.signal(signal.SIGTERM, signal.SIG_IGN if sys.argv[1] == 'ignore' else lambda *_: sys.exit(0))
sockets = []
for port, kind in [(8080, socket.SOCK_STREAM), (9090, socket.SOCK_STREAM), (5353, socket.SOCK_DGRAM)]:
    s = socket.socket(socket.AF_INET, kind)
    s.bind(('0.0.0.0', port))
    if kind == socket.SOCK_STREAM: s.listen()
    sockets.append(s)
while True: signal.pause()
"""

    def running(cid):
        return docker("inspect", "--format", "{{.State.Running}}", cid) == "true"

    def exercise(cid, name, force, mappings):
        master, slave = pty.openpty()
        app = None
        try:
            fcntl.ioctl(slave, termios.TIOCSWINSZ, struct.pack("HHHH", 34, 124, 0, 0))
            app = subprocess.Popen([binary, "--interval", "500ms"], stdin=slave, stdout=slave,
                                   stderr=slave, env={**env, "TERM": "xterm-256color", "NO_COLOR": "1"},
                                   start_new_session=True)

            def read(seconds=0.5):
                chunks = []
                until = time.monotonic() + seconds
                while time.monotonic() < until:
                    if select.select([master], [], [], min(0.05, max(0, until-time.monotonic())))[0]:
                        try:
                            chunks.append(os.read(master, 65536))
                        except OSError:
                            break
                return b"".join(chunks).decode(errors="replace")

            def send(keys, seconds=0.5):
                os.write(master, keys.encode())
                return read(seconds)

            assert "PORTMAN" in read(1.5)
            send("/")
            output = send(name + "\r", 1)
            assert "1 container" in output, output
            send("g")
            # Docker -> Compose -> container. Checkbox and port toggles share
            # the same hit-testing path as a user's terminal mouse.
            output = send("\x1b[<0;5;11M\x1b[<0;5;11m")
            assert "1 container selected" in output, output
            output = send("\r")
            for mapping in mappings:
                expected = f"127.0.0.1:{mapping['host_port']} → {mapping['container_port']}/{mapping['protocol']}"
                assert expected in output, (expected, output)
            output = send("d")
            assert "Stop selected containers?" in output and name in output, output
            send("\r")
            assert running(cid), "Enter should cancel by default"
            send("d")
            output = send("y", 3)
            if force:
                assert running(cid), "SIGTERM must not auto-escalate"
                assert "Targets are still running" in output and "SIGKILL" in output, output
                send("\r")
                assert running(cid), "force confirmation should default to Cancel"
                send("d")
                output = send("y", 3)
                assert "Targets are still running" in output, output
                send("y", 2)
            assert not running(cid), "selected container did not stop"
            send("q")
            app.wait(timeout=5)
            assert app.returncode == 0
            print("PASS: Docker", "explicit SIGKILL" if force else "SIGTERM",
                  "· Compose/search/click/expand TCP+UDP mappings/cancel/stop")
        finally:
            if app is not None:
                if app.poll() is None:
                    app.kill()
                app.wait(timeout=5)
            os.close(master)
            os.close(slave)

    try:
        for service, ignore in (("api", False), ("db", True), ("other", False)):
            name = project + "-" + service
            compose = project if service != "other" else project + "-other"
            cid = docker("run", "--detach", "--name", name,
                         "--label", "com.docker.compose.project=" + compose,
                         "--label", "com.docker.compose.service=" + service,
                         "--publish", "127.0.0.1::8080/tcp", "--publish", "127.0.0.1::9090/tcp",
                         "--publish", "127.0.0.1::5353/udp",
                         os.environ.get("PORTMAN_DOCKER_TEST_IMAGE", "python:3.13.7-alpine"),
                         "python", "-c", fixture, "ignore" if ignore else "term")
            assert re.fullmatch("[a-f0-9]{64}", cid), cid
            created.append((cid, name))
        snapshot = json.loads(subprocess.check_output([binary, "--json"], env=env, text=True))
        containers = {p["container"]["id"]: p for p in snapshot["processes"] if p.get("container")}
        for cid, _ in created:
            assert cid in containers, "fixture missing from discovery"
            mappings = containers[cid]["container"]["ports"]
            assert len(mappings) == 3 and {p["protocol"] for p in mappings} == {"tcp", "udp"}
            for mapping in mappings:
                if mapping["protocol"] == "tcp":
                    with socket.create_connection(("127.0.0.1", mapping["host_port"]), timeout=5):
                        pass
        for index, force in ((0, False), (1, True)):
            cid, name = created[index]
            exercise(cid, name, force, containers[cid]["container"]["ports"])
            for peer, _ in created[index+1:]:
                assert running(peer), "stopping a container affected a sibling or another Compose app"
        print("PASS: unrelated Compose app stayed running; all fixture containers will be removed")
    finally:
        for cid, _ in created:
            subprocess.run([cli, "--host", endpoint, "rm", "--force", cid], env=env,
                           stdout=subprocess.DEVNULL, check=True)


if __name__ == "__main__":
    main()
