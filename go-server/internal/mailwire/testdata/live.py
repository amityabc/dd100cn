"""Compare original v35 and PostgreSQL town assets on isolated loopback hosts."""
import argparse
import base64
from contextlib import closing, contextmanager
import gzip
import json
import os
from pathlib import Path
import shutil
import socket
import sqlite3
import struct
import subprocess
import sys
import tempfile
import uuid

project = Path(__file__).resolve().parents[3]
workspace = project.parent
sys.path.insert(0, str(project / "testdata/protocol"))
sys.path.insert(0, str(project / "tools/multiplayer"))
from test_gateway import go_host, recv_login, wait_ready
from capture_town import exchange
from probe import frame, identity

fixtures = {
    "mail": ("mailbox-v35", "mailbox-dsn.txt", "mailbox-verify/v35-mail.sqlite3"),
    "weapon": ("town-weapon-v35", "town-weapon-dsn.txt", "storage-verify/v35-town-equipped.sqlite3"),
    "stack": ("town-stack-v35", "town-stack-dsn.txt", "storage-verify/v35-town-stacks.sqlite3"),
}

def prepare_mail(fixture):
    source = workspace / "baseline/.work/fixtures/v35-character.sqlite3"
    output = fixture / "v35-mail.sqlite3"
    if output.exists():
        return output
    mailbox = json.loads((project / "internal/mailwire/testdata/mail.json").read_text())["Mailbox"]
    with tempfile.TemporaryDirectory(prefix="prepare-",dir=fixture) as temp:
        dest = Path(temp) / "v35-mail.sqlite3"
        with closing(sqlite3.connect(source.resolve().as_uri()+"?mode=ro",uri=True)) as original, closing(sqlite3.connect(dest)) as db:
            original.backup(db)
            assert db.execute("SELECT version FROM next_schema_modules WHERE module='local-entry'").fetchone()==(35,)
            edit = db.execute("UPDATE local_entry_characters SET gameplay_json=json_set(gameplay_json,'$.Mailbox',json(?)) WHERE id=1 AND storage_slot=0",
                              (json.dumps(mailbox,ensure_ascii=False,separators=(",",":")),))
            assert edit.rowcount == 1
            db.commit()
            assert db.execute("PRAGMA integrity_check").fetchone()==("ok",)
            db.execute("PRAGMA wal_checkpoint(TRUNCATE)")
        dest.rename(output)
    return output


@contextmanager
def old_host(root, kind, sdk, source=None):
    baseline = workspace / "baseline/.work"
    (root / "config").mkdir()
    shutil.copyfile(baseline / "run/config/server.json", root / "config/server.json")
    (root / "runtime/data").mkdir(parents=True)
    shutil.copyfile(source or project / ".work" / fixtures[kind][2], root / "runtime/data/next-local.db")
    cache = baseline / "run/runtime/cache"
    if cache.is_dir():
        shutil.copytree(cache, root / "runtime/cache", copy_function=os.link, ignore=shutil.ignore_patterns("*.lock"))
    host = baseline / "source/Server/artifacts/bin/Serve100.Host/release/Serve100.Host.dll"
    run_id = uuid.uuid4().hex
    env = dict(os.environ, DOTNET_ROOT=str(sdk.parent), DOTNET_CLI_TELEMETRY_OPTOUT="1", TEMP=str(root), TMP=str(root))
    with (root / "host.log").open("wb") as log:
        proc = subprocess.Popen([sdk, host, "--root", root, "--run-id", run_id], cwd=root, env=env,
                                stdout=log, stderr=log, creationflags=subprocess.CREATE_NO_WINDOW)
        try:
            ready = wait_ready(proc, root / "runtime/next-ready.json", 180)
            assert ready["RunId"] == run_id and ready["ProcessId"] == proc.pid

            @contextmanager
            def connect():
                with socket.create_connection(("127.0.0.1", ready["GamePort"]), 5) as client:
                    recv_login(client)
                    yield client
            yield connect
        finally:
            if proc.poll() is None:
                (root / "runtime/stop.request").write_text("stop\n")
                try:
                    proc.wait(25)
                except subprocess.TimeoutExpired:
                    proc.terminate()
                    proc.wait(12)


def snapshot(replies):
    regular = next(b[16:] for b in replies if identity(b) == (0, 13) and b[16] == 0)
    worn = next(b[16:] for b in replies if identity(b) == (0, 14) and b[16] == 3)
    count = struct.unpack_from("<H", regular, 3)[0]
    assert len(regular) == 5 + count * 119
    items = {}
    for i in range(count):
        slot, template, value = struct.unpack_from("<HII", regular, 5 + i * 119)
        items[slot] = (template, value)
    assert struct.unpack_from("<H", worn, 3)[0] == 12
    return items, struct.unpack_from("<I", worn, 5)[0]


def bags(replies):
    items, worn = snapshot(replies)
    assert worn == 27302
    return {slot: value for slot, (template, value) in items.items() if template == 1085}


def test(root, connect, kind):
    result = {}

    def action(client, label, op, seq, body):
        item = exchange(client, op, seq, body)
        result[label] = item
        if item.get("closed"):
            raise RuntimeError(f"{label} closed\n" + (root / "host.log").read_text(errors="replace")[-2400:])
        return [bytes.fromhex(x) for x in item["responses"]]

    def enter(client, prefix):
        action(client, prefix + "list", 8, 2, b"\xff\xff\x02")
        selected = action(client, prefix + "select", 4, 4, bytes(2))
        select = next(b[16:] for b in selected if identity(b) == (1, 4))
        if kind == "mail" and select[-10:-8] != bytes([1, 31]):
            action(client, prefix + "skipTutorial", 143, 6, bytes.fromhex("001f00000001"))
        loaded = action(client, prefix + "loadTown", 120, 8, b"")
        action(client, prefix + "readyTown", 120, 10, b"")
        if kind == "mail":
            expected = json.loads((project / "internal/mailwire/testdata/mail.json").read_text())["Packets"]
            mail = [(identity(b)[1], b[16:]) for b in loaded if identity(b)[1] in (97, 99)]
            assert mail == [(p["Opcode"], base64.b64decode(p["Body"])) for p in expected], "mail differs from original C#"
            assert struct.unpack("<H", mail[1][1])[0] == 22
        elif kind == "stack":
            assert bags(loaded) == {65: 8, 66: 4}
        else:
            bag, worn = snapshot(loaded)
            assert worn == 27302 and (9 not in bag or bag[9][0] != 27302)

    with connect() as c:
        enter(c, "")

        def move(label, seq, source, source_slot, count, dest, dest_slot, last_template=0, last_count=0):
            template = 27302 if kind == "weapon" else 1085
            body = struct.pack("<BHIIBHIIIBB", source, source_slot, template, count, dest, dest_slot, last_template, last_count, 0xffffffff, 0, 0)
            replies = [b for b in action(c, label, 19, seq, body) if struct.unpack_from("<H", b, 11)[0] == seq]
            ids = [identity(b) for b in replies]
            expected = [(1, 19), (0, 13), (0, 14), (0, 2)]
            if kind == "weapon":
                expected.append((0, 2))
            assert ids == expected, (label, ids)
            assert replies[0][16:] == struct.pack("<BBHIBH", 1, source, source_slot, count, dest, dest_slot)
            return replies

        if kind == "weapon":
            take = move("takeOff", 12, 3, 12, 1, 0, 9)
            bag, worn = snapshot(take)
            assert bag[9][0] == 27302 and worn == 0xffffffff
            equip = move("equip", 14, 0, 9, 1, 3, 12)
            bag, worn = snapshot(equip)
            assert worn == 27302 and 9 not in bag
            assert move("equipReplay", 14, 0, 9, 1, 3, 12) == equip
        if kind == "stack":
            split = move("split", 12, 0, 65, 3, 0, 67)
            assert bags(split) == {65: 5, 66: 4, 67: 3}
            assert move("splitReplay", 12, 0, 65, 3, 0, 67) == split
            merge = move("merge", 14, 0, 67, 3, 0, 66, 1085, 4)
            assert bags(merge) == {65: 5, 66: 7}
            assert move("mergeReplay", 14, 0, 67, 3, 0, 66, 1085, 4) == merge
            restore = move("restore", 16, 0, 66, 3, 0, 65, 1085, 5)
            assert bags(restore) == {65: 8, 66: 4}
    with connect() as c:
        enter(c, "reconnect-")
    return result


def move_actions(results):
    return {name: {"request": entry["request"], "responses": [raw for raw in entry["responses"]
            if bytes.fromhex(raw)[11:13] == bytes.fromhex(entry["request"])[11:13]]}
            for name, entry in results.items() if identity(bytes.fromhex(entry["request"])) == (1, 19)}


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("kind", choices=fixtures)
    parser.add_argument("--bin", type=Path, default=project/"bin")
    parser.add_argument("--prepare",action="store_true",help="prepare the v35 mailbox fixture from the original C5 backup and C# mail sample")
    parser.add_argument("--legacy-dotnet", type=Path, help="record reference bodies in testdata/inventory using this .NET 10 SDK")
    args = parser.parse_args()
    kind = args.kind
    fixture = project / ".work/mailbox-verify"
    fixture.mkdir(exist_ok=True, parents=True)
    if args.prepare:
        if kind != "mail":
            parser.error("--prepare applies to the mailbox fixture")
        print(prepare_mail(fixture))
        return
    with tempfile.TemporaryDirectory(prefix=kind + "-", dir=fixture) as directory:
        root = Path(directory)
        host = old_host(root, kind, args.legacy_dotnet) if args.legacy_dotnet else go_host(root, args.bin.resolve(), workspace / ".work/postgres-storage" / fixtures[kind][1], fixtures[kind][0])
        try:
            with host as connect:
                results = test(root, connect, kind)
        except Exception:
            if (root / "host.log").exists():
                shutil.copyfile(root / "host.log", fixture / (kind + "-last-error.log"))
            raise
    path = project / f"testdata/inventory/legacy-{kind}-moves.json.gz"
    if kind != "mail":
        observed = move_actions(results)
        if args.legacy_dotnet:
            path.write_bytes(gzip.compress(json.dumps(observed, separators=(",", ":")).encode(), mtime=0))
        else:
            expected = json.loads(gzip.decompress(path.read_bytes()))
            for name, event in observed.items():
                assert event == expected[name], f"{name} differs from the original C# frames"
    if not args.legacy_dotnet:
        (fixture / (kind + "-tcp.json.gz")).write_bytes(gzip.compress(json.dumps(results, separators=(",", ":")).encode(), mtime=0))
    print(kind, "C#" if args.legacy_dotnet else "Go", {k: len(v["responses"]) for k,v in results.items()})


if __name__ == "__main__":
    main()
