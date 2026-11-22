"""Disposable loopback Go gateway with a pinned operator credential and a caller-owned PostgreSQL fixture."""
import base64
from contextlib import contextmanager
import json
import os
from pathlib import Path
import shutil
import socket
import ssl
import struct
import subprocess
import sys
import time

project = Path(__file__).resolve().parents[2]
workspace = project.parent
sys.path.insert(0, str(workspace / "baseline"))
from probe import frame, identity


def recv_login(client):
    client.settimeout(15)
    client.setsockopt(socket.IPPROTO_TCP, socket.TCP_NODELAY, 1)
    assert [identity(frame(client)), identity(frame(client))] == [(0, 1), (1, 1)]


@contextmanager
def go_host(root, binaries, dsn_file, account):
    binary = binaries / "serve100.exe"
    admin = binaries / "multiplayer-admin.exe"
    with socket.socket() as reserve:
        reserve.bind(("127.0.0.1", 0))
        port = reserve.getsockname()[1]
    operator = root / "operator"
    profile = root / "player.json"
    endpoint = f"127.0.0.1:{port}"
    subprocess.run([admin, "init", "-dir", operator, "-gateway", endpoint], check=True, stdout=subprocess.DEVNULL)
    subprocess.run([admin, "issue", "-dir", operator, "-account", account, "-out", profile], check=True, stdout=subprocess.DEVNULL)
    (root / "config").mkdir()
    shutil.copyfile(workspace / "s9a1/Server/config/server.json", root / "config/server.json")
    dsn = dsn_file.read_text().strip()
    env = dict(os.environ, S9A1_GATEWAY_CONFIG=str(operator / "gateway.json"), S9A1_POSTGRES_DSN=dsn,
               S9A1_CREATION_CATALOG=str(project / ".work/catalog/characters.json"), S9A1_LOG_LEVEL="debug",
               SERVE100_NEXT_SERVER__CHANNELPORT="0", SERVE100_NEXT_SERVER__GAMEPORT="0",
               SERVE100_NEXT_GAMEPLAY__CHANNELSCRIPT=str(workspace / "s9a1/Server/Data/channel_info.etc"),
               SERVE100_NEXT_RESOURCES__PVF=str(workspace / "客户端/Script.pvf"),
               SERVE100_NEXT_RESOURCES__CLIENTPROFILE=str(workspace / "s9a1/Server/Data/Pvf/Client100Profile.json"))
    player = json.loads(profile.read_text())
    context = ssl.create_default_context(cadata=player["serverCertificate"])
    context.minimum_version = ssl.TLSVersion.TLSv1_3
    context.set_alpn_protocols(["s9a1-gateway/1"])

    @contextmanager
    def connect(channel=19, consume_greeting=True):
        with context.wrap_socket(socket.create_connection(("127.0.0.1", port), 5), server_hostname="127.0.0.1") as client:
            name = player["account"].encode()
            token = base64.urlsafe_b64decode(player["token"] + "=")
            client.sendall(struct.pack(">BHB", 2, channel, len(name)) + name + token)
            assert client.recv(1) == b"\0"
            if consume_greeting:
                recv_login(client)
            yield client

    with (root / "host.log").open("wb") as log:
        host = subprocess.Popen([binary, "--root", root], env=env, stdout=log, stderr=log, creationflags=subprocess.CREATE_NO_WINDOW)
        try:
            wait_ready(host, root / "runtime/go-ready.json")
            yield connect
        finally:
            if host.poll() is None:
                stop = subprocess.run([binary, "--root", root, "--request-stop"], env=env,
                                      stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)
                if stop.returncode:
                    host.terminate()
                try:
                    host.wait(6)
                except subprocess.TimeoutExpired:
                    host.kill()
                    host.wait(3)


def wait_ready(host, ready, timeout=20):
    due = time.monotonic() + timeout
    while time.monotonic() < due and host.poll() is None:
        if ready.is_file():
            try:
                return json.loads(ready.read_text(encoding="utf-8-sig"))
            except (ValueError, OSError):
                pass
        time.sleep(.08)
    raise RuntimeError("isolated host did not become ready")
