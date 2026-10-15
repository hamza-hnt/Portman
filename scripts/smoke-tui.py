#!/usr/bin/env python3
"""Exercise the real TUI through a PTY; only terminate fixtures we create."""
import fcntl
import json
import os
import pathlib
import pty
import select
import signal
import socket
import struct
import subprocess
import sys
import tempfile
import termios
import time


def fixture(ignore_term):
    if ignore_term:
        signal.signal(signal.SIGTERM, signal.SIG_IGN)
    tcp = socket.socket()
    tcp.bind(("127.0.0.1", 0))
    tcp.listen()
    udp = socket.socket(socket.AF_INET, socket.SOCK_DGRAM)
    udp.bind(("127.0.0.1", 0))
    print(json.dumps([tcp.getsockname()[1], udp.getsockname()[1]]), flush=True)
    while True:
        signal.pause()


def exercise(binary, force):
    with tempfile.TemporaryDirectory(prefix="portman-pty-") as root:
        name = "portman-pty-fixture-" + str(os.getpid())
        pathlib.Path(root, "package.json").write_text(json.dumps({"name": name}))
        child = subprocess.Popen(
            [sys.executable, str(pathlib.Path(__file__).resolve()), "--fixture", str(int(force))],
            cwd=root, stdout=subprocess.PIPE, text=True,
        )
        app = None
        master, slave = pty.openpty()
        try:
            assert select.select([child.stdout], [], [], 5)[0], "fixture startup timed out"
            ports = json.loads(child.stdout.readline())
            fcntl.ioctl(slave, termios.TIOCSWINSZ, struct.pack("HHHH", 30, 110, 0, 0))
            app = subprocess.Popen(
                [binary, "--interval", "500ms"], stdin=slave, stdout=slave, stderr=slave,
                env={**os.environ, "TERM": "xterm-256color", "NO_COLOR": "1"},
                start_new_session=True,
            )

            def read(seconds=0.5):
                chunks = []
                until = time.monotonic() + seconds
                while time.monotonic() < until:
                    if select.select([master], [], [], max(0, min(0.05, until-time.monotonic())))[0]:
                        try:
                            chunks.append(os.read(master, 65536))
                        except OSError:
                            break
                return b"".join(chunks).decode(errors="replace")

            def send(keys, seconds=0.5):
                os.write(master, keys.encode())
                return read(seconds)

            output = read(1.5)
            assert "PORTMAN" in output and "PROJECT / SERVICE" in output, output
            send("/")
            send(name + "\r", 1)
            send("g")
            # Click the process checkbox (SGR mouse coordinates are 1-based).
            output = send("\x1b[<0;5;10M\x1b[<0;5;10m")
            assert "1 process selected" in output, output
            output = send("d")
            assert "Stop selected processes?" in output and str(child.pid) in output, output
            send("\r")  # Enter must default to Cancel.
            assert child.poll() is None, "default confirmation unexpectedly killed fixture"
            send("d")
            output = send("y", 3)
            if force:
                assert child.poll() is None, "fixture should ignore SIGTERM"
                assert "Processes are still running" in output and "SIGKILL" in output, output
                send("\r")  # Escalation also defaults to Cancel.
                assert child.poll() is None
                send("d")
                output = send("y", 3)
                assert "Processes are still running" in output
                send("y", 1)
            child.wait(timeout=4)
            assert child.returncode == (-signal.SIGKILL if force else -signal.SIGTERM), child.returncode
            output = send("i")
            assert "LAST TERMINATION" in output and "exited" in output, output
            send("q")
            # Resize exercises the application's real SIGWINCH path.
            fcntl.ioctl(slave, termios.TIOCSWINSZ, struct.pack("HHHH", 15, 40, 0, 0))
            app.send_signal(signal.SIGWINCH)
            output = read()
            assert "resize terminal" in output, output
            send("q")
            app.wait(timeout=3)
            assert app.returncode == 0, app.returncode
            print(f"PASS: {'explicit SIGKILL' if force else 'SIGTERM'} · TCP/UDP {ports} · search/mouse-select/cancel/inspect/resize/quit")
        finally:
            for proc in (app, child):
                if proc is not None:
                    if proc.poll() is None:
                        proc.kill()
                    proc.wait(timeout=5)
            os.close(master)
            os.close(slave)
            child.stdout.close()


if __name__ == "__main__":
    if len(sys.argv) > 1 and sys.argv[1] == "--fixture":
        fixture(sys.argv[2] == "1")
    else:
        binary = str(pathlib.Path(sys.argv[1] if len(sys.argv)>1 else "bin/portman").resolve())
        exercise(binary, False)
        exercise(binary, True)
