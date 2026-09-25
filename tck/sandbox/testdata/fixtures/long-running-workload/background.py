#!/usr/local/bin/python3
"""A live challenge response distinguishes a surviving process from stale files."""

import secrets
import socket
import subprocess
import sys
import time

ADDRESS = "/tmp/kit-tck-background.sock"


def serve():
    identity = secrets.token_hex(16)
    with socket.socket(socket.AF_UNIX) as server:
        server.bind(ADDRESS)
        server.listen()
        while True:
            client, _ = server.accept()
            with client, client.makefile("rwb") as stream:
                challenge = stream.readline(128).strip().decode("ascii")
                stream.write(f"{identity}:{challenge}\n".encode("ascii"))
                stream.flush()


def probe():
    challenge = secrets.token_hex(16)
    with socket.socket(socket.AF_UNIX) as client:
        client.settimeout(2)
        client.connect(ADDRESS)
        with client.makefile("rwb") as stream:
            stream.write(f"{challenge}\n".encode("ascii"))
            stream.flush()
            identity, answer = stream.readline(128).strip().decode("ascii").split(":")
    if not identity or answer != challenge:
        raise RuntimeError("background process did not answer the fresh challenge")
    return identity


if sys.argv[1] == "serve":
    serve()
elif sys.argv[1] == "start":
    # Detach the process and all inherited pipes so returning from exec
    # really closes that client session; the service holds no client open.
    subprocess.Popen(
        [sys.executable, __file__, "serve"],
        stdin=subprocess.DEVNULL,
        stdout=subprocess.DEVNULL,
        stderr=subprocess.DEVNULL,
        start_new_session=True,
    )
    deadline = time.monotonic() + 5
    while True:
        try:
            print(probe())
            break
        except (OSError, ValueError):
            if time.monotonic() >= deadline:
                raise
            time.sleep(0.05)
elif sys.argv[1] == "probe":
    print(probe())
else:
    sys.exit("expected start, probe, or serve")
