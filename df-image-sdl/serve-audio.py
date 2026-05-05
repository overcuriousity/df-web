#!/usr/bin/env python3
"""Minimal HTTP server that streams DF audio as WebM/Opus via ffmpeg+PulseAudio."""
import os, socket, subprocess, threading

PORT = 6081
PULSE_SERVER = os.environ.get("PULSE_SERVER", "unix:/tmp/pulse/native")
PULSE_SOURCE = "null_out.monitor"

HEADERS = (
    b"HTTP/1.1 200 OK\r\n"
    b"Content-Type: audio/webm; codecs=opus\r\n"
    b"Cache-Control: no-cache\r\n"
    b"\r\n"
)

def handle(conn):
    env = {**os.environ, "PULSE_SERVER": PULSE_SERVER}
    # Drain the HTTP request headers before sending the response.
    buf = b""
    while b"\r\n\r\n" not in buf:
        chunk = conn.recv(4096)
        if not chunk:
            conn.close()
            return
        buf += chunk
    conn.sendall(HEADERS)
    proc = subprocess.Popen(
        ["ffmpeg", "-f", "pulse", "-i", PULSE_SOURCE,
         "-c:a", "libopus", "-b:a", "64k", "-f", "webm", "pipe:1"],
        stdout=subprocess.PIPE, stderr=subprocess.DEVNULL,
        stdin=subprocess.DEVNULL, env=env,
    )
    try:
        while chunk := proc.stdout.read(4096):
            conn.sendall(chunk)
    except Exception:
        pass
    finally:
        proc.kill()
        conn.close()

srv = socket.socket(socket.AF_INET, socket.SOCK_STREAM)
srv.setsockopt(socket.SOL_SOCKET, socket.SO_REUSEADDR, 1)
srv.bind(("0.0.0.0", PORT))
srv.listen(5)
while True:
    conn, _ = srv.accept()
    threading.Thread(target=handle, args=(conn,), daemon=True).start()
