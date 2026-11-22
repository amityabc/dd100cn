"""Compare native C324 frames on the isolated original Host and the Go gateway."""
import argparse
import gzip
import json
import struct
import tempfile
from pathlib import Path

from live import exchange, fixtures, go_host, old_host, project, workspace

HERE = Path(__file__).resolve().parent
GOLDEN = HERE / "recipient-legacy.json.gz"
SENDER, RECEIVER, MIXED, FOREIGN = "基线角色", "大厅新角", "MailCase甲", "异户收件"


def query(name, mode=0):
    raw = name.encode("gbk") if isinstance(name, str) else name
    return struct.pack("<I", len(raw)) + raw + bytes([mode])


def roster(client):
    result = exchange(client, 8, 4, bytes.fromhex("ffff02"))
    return next(bytes.fromhex(b)[16:] for b in result["responses"] if bytes.fromhex(b)[:3] == bytes([0, 2, 0]))


def create_if_missing(client, name):
    raw = name.encode("gb18030")
    if raw in roster(client):
        return
    event = exchange(client, 5, 6, b"\0" + struct.pack("<I", len(raw)) + raw)
    assert any(bytes.fromhex(b)[:3] == bytes([1, 5, 0]) and bytes.fromhex(b)[16] == 1 for b in event["responses"])


def run(connect):
    records = []
    with connect() as client:
        def check(label, body):
            event = exchange(client, 324, 20 + 2 * len(records), body)
            replies = [b for b in event["responses"] if bytes.fromhex(b)[:3] == bytes([1, 68, 1])]
            assert not event.get("closed") and len(replies) == 1, label
            records.append(dict(name=label, request=event["request"], response=replies[0]))
        check("authenticated", query(RECEIVER))
        create_if_missing(client, MIXED)
        check("roster", query(RECEIVER))
        exchange(client, 4, 8, bytes(2))
        check("selected", query(RECEIVER))
        exchange(client, 120, 10, b"")
        check("townLoading", query(RECEIVER))
        exchange(client, 120, 12, b"")
        for label, body in [
            ("sameAccount", query(RECEIVER)), ("asciiCase", query("mAILcASE甲")),
            ("self", query(SENDER)), ("unknown", query("Nobody")),
            ("foreign", query(FOREIGN)), ("mode", query(RECEIVER, 1)),
            ("empty", query(b"")), ("whitespace", query("　")),
            ("GBK80", query(b"\x80")), ("GBKFF", query(b"\xff")),
            ("privatePair", query(bytes.fromhex("a2e3"))), ("nul", query(b"A\0B")),
            ("badLead", query(b"\x81")), ("fourByte", query(bytes.fromhex("90308130"))),
            ("oversize", query(b"x" * 20)), ("finalizedPadding", query(RECEIVER) + bytes(3)),
        ]:
            check(label, body)
        exchange(client, 7, 80, b"")
        check("returned", query(RECEIVER))
    return records


def main():
    p = argparse.ArgumentParser(description=__doc__)
    p.add_argument("--legacy-dotnet", type=Path)
    p.add_argument("--bin", type=Path, default=project/"bin")
    args = p.parse_args()
    work = project/".work/mailbox-verify"
    work.mkdir(exist_ok=True, parents=True)
    fixtures["recipient"] = ("recipient-v35", "recipient-dsn.txt", "duel-capture/v35-duel.sqlite3")
    dsn = workspace/".work/postgres-storage/recipient-dsn.txt"
    with tempfile.TemporaryDirectory(prefix="recipient-", dir=work) as temp:
        root = Path(temp)
        if args.legacy_dotnet:
            with old_host(root, "recipient", args.legacy_dotnet.resolve()) as connect:
                data = run(connect)
                stamp = json.loads((root/"runtime/next-ready.json").read_text(encoding="utf-8-sig"))
            golden = dict(origin="Original C# Host and matching PVF, scripted TCP; one local account",
                          hostSha256=stamp["HostSha256"], runId=stamp["RunId"], records=data)
            GOLDEN.write_bytes(gzip.compress(json.dumps(golden,separators=(",",":")).encode(),mtime=0))
        else:
            # The original has one account. Under PostgreSQL the same name
            # belongs to a real second account, created through its own lease.
            foreign = root/"foreign"
            foreign.mkdir()
            with go_host(foreign,args.bin.resolve(),dsn,"recipient-other") as connect:
                with connect() as client:
                    create_if_missing(client, FOREIGN)
            with go_host(root,args.bin.resolve(),dsn,"recipient-v35") as connect:
                data=run(connect)
            original=json.loads(gzip.decompress(GOLDEN.read_bytes()))
            assert data == original["records"], [r["name"] for r,x in zip(data, original["records"]) if r != x]
        print("C324", len(data), "original" if args.legacy_dotnet else "Go", "native requests and full reply frames")


if __name__ == "__main__":
    main()
