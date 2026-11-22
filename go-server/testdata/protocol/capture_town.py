"""Record both original opening and town sync routes from a disposable C# Host."""
import argparse
from contextlib import closing
import gzip
import json
import os
import shutil
import socket
import sqlite3
import struct
import subprocess
import sys
import tempfile
import time
import uuid
from pathlib import Path

HERE = Path(__file__).resolve().parent
sys.path.insert(0, str(HERE.parents[2] / "baseline"))
from probe import frame, identity, request


def saved_player(root):
    database = root / "runtime/data/next-local.db"
    with closing(sqlite3.connect(database.resolve().as_uri() + "?mode=ro", uri=True)) as db:
        db.row_factory = sqlite3.Row
        rows = db.execute("SELECT * FROM local_entry_characters ORDER BY id").fetchall()
        return [
            {k: v for k, v in dict(row).items() if k in {
                "id", "name", "job", "level", "created", "revision", "town",
                "area", "x", "y", "direction", "state", "stats_json", "gameplay_json",
                "wire_tail", "gold", "coin", "bag_expansion",
            }}
            for row in rows
        ]


def exchange(stream, op, sequence, body):
    sent = request(op, sequence, body)
    barrier = request(1715, sequence + 1, bytes(13) + struct.pack("<I", sequence + 1))
    stream.sendall(sent + barrier)
    replies = []
    for _ in range(160):
        try:
            wire = frame(stream)
        except EOFError:
            return {"request": sent.hex(), "responses": replies, "closed": True}
        received_seq = struct.unpack_from("<H", wire, 11)[0]
        if identity(wire) == (1, 1715) and received_seq == sequence + 1:
            return {"request": sent.hex(), "responses": replies}
        # The stock host can emit a delayed notification after the preceding
        # ping has already completed. Preserve its original sequence and order.
        replies.append(wire.hex())
    raise RuntimeError("response train exceeded capture limit")


def combat_route(stream, root, dungeon, player):
    events = []
    owner = player["id"]
    actor = 402
    sequence = 50
    def apply(name, op, body):
        nonlocal sequence
        item = exchange(stream, op, sequence, body)
        sequence += 2
        item.update(action=name, state=saved_player(root))
        events.append(item)
        if item.get("closed"):
            raise RuntimeError(f"closed during {name}")
        return item
    def report(target):
        return struct.pack("<IH", target, owner) + bytes(68)
    for index, room in enumerate(dungeon["Rooms"]):
        if index:
            apply(f"move{index}", 45, bytes([room["X"], room["Y"]]) + bytes(98))
            apply(f"load{index}", 37, bytes(8))
        for m in room["Monsters"]:
            if m["BlocksRoom"]:
                apply(f"death{actor}", 39, report(actor))
                if actor == 402:
                    apply(f"repeatDeath{actor}", 39, report(actor))
            actor += 1
        actor += len(room["Companions"])
    with closing(sqlite3.connect((root/"runtime/data/next-local.db").resolve().as_uri()+"?mode=ro",uri=True)) as db:
        db.row_factory = sqlite3.Row
        receipts = [dict(r) for r in db.execute("SELECT * FROM local_character_receipts ORDER BY rowid")]
    return events, receipts


def job_combat(catalog, job):
    rules = catalog["OpeningCombat"]
    return {"ExperienceHash": rules["ExperienceHash"], "Levels": rules["Levels"],
            "Jobs": {str(job): rules["Jobs"][str(job)]}}


def record(ready, root, opening, combat=None):
    with socket.create_connection(("127.0.0.1", ready["GamePort"]), timeout=20) as stream:
        stream.setsockopt(socket.IPPROTO_TCP, socket.TCP_NODELAY, 1)
        for _ in range(5):
            if identity(frame(stream)) == (1, 1):
                break
        else:
            raise RuntimeError("no automatic login")
        actions = [("list", 8, bytes.fromhex("ffff02"))]
        if opening:
            name = "基线角色".encode("gb18030")
            actions.append(("create", 5, b"\x00" + struct.pack("<I", len(name)) + name))
        actions.append(("select", 4, bytes(2)))
        if opening:
            actions += [("loadOpening", 120, b""), ("bootstrapOpening", 143, bytes(6)),
                        ("sceneControl", 191, bytes.fromhex("0100")),
                        ("loaded", 37, bytes(8)), ("loadedAgain", 37, bytes(16)),
                        ("sceneFlip", 191, bytes.fromhex("0003")),
                        ("unsupportedSceneControl", 191, bytes.fromhex("0001")),
                        ("blockedMove", 45, bytes([1]) + bytes(63))]
        else:
            actions += [("completeTutorialC143", 143, bytes.fromhex("001f00000001")),
                        ("loadTown", 120, b""), ("readyTown", 120, b"")]
        snapshots = []
        for i, (name, op, body) in enumerate(actions, 1):
            entry = exchange(stream, op, i * 2, body)
            entry["action"] = name
            entry["state"] = saved_player(root)
            snapshots.append(entry)
            if entry.get("closed"):
                return snapshots
        if combat is not None:
            path, receipts = combat_route(stream, root, combat, snapshots[-1]["state"][0])
            return {"entry": snapshots, "path": path, "receipts": receipts}
        if not opening:
            row = snapshots[-1]["state"][0]
            movement = struct.pack("<HHBH", row["x"] + 1, row["y"], 5, 1)
            entry = exchange(stream, 35, 16, movement)
            entry.update(action="move", state=saved_player(root))
            snapshots.append(entry)
        return snapshots


def record_travel(ready, root):
    def street(town, area, x, y, previous_area, tail=bytes(6)):
        return struct.pack("<IBHHBIB", town, area, x, y, 5, 38, previous_area) + tail

    actions = [
        ("list", 8, bytes.fromhex("ffff02")), ("selectRoom", 4, bytes(2)),
        ("loadRoom", 120, b""), ("readyRoom", 120, b""),
        ("createSoloParty", 12, bytes.fromhex("000604000000000500000000000007")),
        ("repeatSoloParty", 12, bytes.fromhex("000604000000000500000000000007")),
        ("differentActiveParty", 12, bytes.fromhex("000603000000000500000000000007")),
        ("badLeave", 13, bytes.fromhex("01000000")),
        ("leaveSoloParty", 13, bytes(4)),
        ("leaveAgain", 13, bytes(4)),
        ("customSoloParty", 12, bytes.fromhex("010002000000d3ce02000000000000000100000001")),
        ("leaveCustomParty", 13, bytes(4)),
        ("roomExit", 1418, b""), ("walkAfterExit", 35, struct.pack("<HHBH", 500, 250, 7, 37)),
        ("roomExitReplay", 1418, b""),
        ("roadToRoom", 36, street(38, 1, 450, 234, 0)),
        ("roadReplay", 36, street(38, 1, 450, 234, 0)),
        ("roadBadOrigin", 36, street(38, 0, 433, 311, 2)),
        ("roomExitToSaved", 1418, b""),
        ("roadToGate", 36, street(38, 2, 84, 249, 0)),
        ("badSeriaWarp", 36, street(38, 1, 450, 234, 2, bytes([0,0,0,0,0,5]))),
        ("returnToList", 7, b""),
    ]
    with socket.create_connection(("127.0.0.1", ready["GamePort"]), timeout=20) as stream:
        stream.setsockopt(socket.IPPROTO_TCP, socket.TCP_NODELAY, 1)
        while identity(frame(stream)) != (1,1):
            pass
        results = []
        for seq, (name, op, body) in enumerate(actions, 1):
            step = exchange(stream, op, 2*seq, body)
            if step.get("closed"):
                raise ValueError(f"old host closed during {name}")
            state = saved_player(root)[0]
            step.update(action=name, state={k:state[k] for k in ("id","revision","town","area","x","y","direction","wire_tail")})
            results.append(step)
        return results


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--dotnet", required=True, type=Path)
    parser.add_argument("--baseline", required=True, type=Path, help="baseline work directory with release host and run/config")
    parser.add_argument("--output", type=Path, default=HERE / "legacy-town.json.gz")
    parser.add_argument("--combat-catalog",type=Path,help="record job zero's natural combat path from this exported PVF catalog")
    parser.add_argument("--travel", action="store_true",help="record a fresh login after completing the ordinary town replay")
    args = parser.parse_args()
    baseline = args.baseline.resolve()
    host = baseline / "source/Server/artifacts/bin/Serve100.Host/release/Serve100.Host.dll"
    work = (HERE.parents[1] / ".work" / "protocol-capture").resolve()
    work.mkdir(parents=True, exist_ok=True)
    with tempfile.TemporaryDirectory(prefix="town-", dir=work) as directory:
        root = Path(directory).resolve()
        if root.parent != work:
            raise ValueError("unexpected capture work path")
        (root / "config").mkdir()
        shutil.copyfile(baseline / "run/config/server.json", root / "config/server.json")
        run_id = uuid.uuid4().hex
        sdk = args.dotnet.resolve()
        environment = dict(os.environ, DOTNET_ROOT=str(sdk.parent),
                           DOTNET_CLI_TELEMETRY_OPTOUT="1", TEMP=str(root), TMP=str(root))
        with (root / "launcher.log").open("wb") as log:
            proc = subprocess.Popen([str(sdk), str(host), "--root", str(root), "--run-id", run_id],
                                    cwd=root, env=environment, stdout=log, stderr=subprocess.STDOUT,
                                    creationflags=getattr(subprocess, "CREATE_NO_WINDOW", 0))
            try:
                deadline = time.monotonic() + 180
                slot = root / "runtime/next-ready.json"
                while proc.poll() is None and time.monotonic() < deadline:
                    if slot.is_file():
                        ready = json.loads(slot.read_text(encoding="utf-8-sig"))
                        if ready["RunId"] == run_id and ready["ProcessId"] == proc.pid:
                            break
                    time.sleep(.12)
                else:
                    raise RuntimeError("isolated host failed to become ready")
                data = {
                    "origin": "Synthetic TCP operations on the original C# host with the matched PVF",
                    "hostSha256": ready["HostSha256"],
                    "runId": run_id,
                    "nativeTownProfile140": ready["NativeTownProfile140"],
                    "nativePartyNotices": ready["NativePartyNotices"],
                }
                if args.combat_catalog is not None:
                    catalog = json.loads(args.combat_catalog.read_text(encoding="utf-8-sig"))
                    definition = next(x for x in catalog["Openings"] if x["Job"] == 0)
                    data.update(definition=definition, combatRules=job_combat(catalog, 0),
                                combat=record(ready,root,True,definition["Dungeon"]))
                    print("combat", [(a["action"],len(a["responses"])) for a in data["combat"]["path"]],flush=True)
                elif args.travel:
                    record(ready,root,True)
                    record(ready,root,False)
                    data["travel"]=record_travel(ready,root)
                    print("travel",[(r["action"],len(r["responses"])) for r in data["travel"]])
                else:
                    data.update(opening=record(ready, root, True), town=record(ready, root, False))
                    for path in ("opening", "town"):
                        print(path, [(a["action"], len(a["responses"]), a.get("closed", False)) for a in data[path]])
                args.output.write_bytes(gzip.compress(json.dumps(data, separators=(",", ":")).encode(), mtime=0))
            finally:
                if proc.poll() is None:
                    (root / "runtime").mkdir(exist_ok=True)
                    (root / "runtime/stop.request").write_text("stop\n", encoding="utf-8")
                    try:
                        proc.wait(timeout=30)
                    except subprocess.TimeoutExpired:
                        proc.terminate()
                        proc.wait(timeout=20)


if __name__ == "__main__":
    main()
