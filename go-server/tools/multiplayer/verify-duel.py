"""Compare both native duel lobbies with the original v35 trace, using a fresh imported account."""
import argparse
import gzip
import json
from pathlib import Path
import socket
import struct
import sys
import tempfile
import time

from test_gateway import go_host, project
from pg_fixture import json_query
sys.path.insert(0, str(project / "testdata/protocol"))
from capture_town import exchange
from probe import checksum, frame, identity


def fields(data):
    def number(i):
        v = shift = 0
        while i < len(data) and shift < 64:
            b = data[i]
            i += 1
            v |= (b & 127) << shift
            if b < 128:
                return v, i
            shift += 7
        raise ValueError("invalid N1 varint")
    i, result = 0, {}
    while i < len(data):
        key, i = number(i)
        value, i = number(i)
        if key % 8 == 2:
            value, i = data[i:i+value], i+value
        elif key % 8 != 0:
            raise ValueError("unexpected N1 protobuf type")
        assert key not in result
        result[key] = value
    assert i == len(data)
    return result


def greeting(sock, reference, lobby, recent):
    new = [frame(sock), frame(sock)]
    old = [bytes.fromhex(raw) for raw in reference]
    for wire in new:
        assert checksum(wire[11:]) == struct.unpack_from("<I", wire, 7)[0]
    assert [identity(w) for w in new] == [(0, 1), (1, 1)]
    assert old[0][:7] == new[0][:7] and old[0][11:20] == new[0][11:20]
    a, b = fields(old[0][20:]), fields(new[0][20:])
    challenge, timestamp = b[26], b[88]
    assert len(challenge) == 334 and challenge not in recent and len(set(challenge)) > 1
    recent.add(challenge)
    assert abs(time.time() - timestamp) < 20 and b[64] == lobby
    b[26], b[88] = a[26], a[88]
    assert a == b
    login = bytearray(old[1])
    struct.pack_into("<I", login, 16 + 13 + len(b"127.0.0.1"), {251: 10501, 252: 10252}[lobby])
    struct.pack_into("<I", login, 7, checksum(login[11:]))
    assert bytes(login) == new[1]


def state(libpq, dsn_file):
    sql = ("SELECT coalesce(json_agg(row_to_json(p) ORDER BY id),'[]') FROM "
           "(SELECT wire_id AS id,name,level,revision,town,area,x,y,direction,state,wire_tail,gameplay_json "
           "FROM s9a1.characters) p")
    return json_query(libpq,dsn_file,sql)


def main():
    p = argparse.ArgumentParser(description=__doc__)
    p.add_argument("--dsn-file", type=Path, required=True)
    p.add_argument("--account", default="duel-v35")
    p.add_argument("--bin", type=Path, default=project / "bin")
    p.add_argument("--libpq",type=Path,help="also compare database state after every session using PostgreSQL's libpq library")
    args = p.parse_args()
    trace = json.loads(gzip.decompress((project / "testdata/protocol/legacy-duel-lobby.json.gz").read_bytes()))
    captures = project / ".work/duel-capture"
    captures.mkdir(parents=True, exist_ok=True)
    signatures = set()
    with tempfile.TemporaryDirectory(dir=captures) as directory:
        root = Path(directory)
        with go_host(root, args.bin.resolve(), args.dsn_file, args.account) as connect:
            for lobby in (251, 252):
                for suffix in ("", "LowLevel", "Roads"):
                    key = str(lobby) + suffix
                    run = trace[key]
                    replies = 0
                    with connect(lobby, consume_greeting=False) as sock:
                        sock.settimeout(12)
                        sock.setsockopt(socket.IPPROTO_TCP, socket.TCP_NODELAY, 1)
                        greeting(sock, run["greeting"], lobby, signatures)
                        for step in run["steps"]:
                            raw = bytes.fromhex(step["request"])
                            got = exchange(sock, identity(raw)[1], struct.unpack_from("<H",raw,11)[0], raw[13:])
                            expected, actual = step["responses"], got["responses"]
                            if expected != actual or bool(step.get("closed")) != bool(got.get("closed")):
                                which = next((i for i,(a,b) in enumerate(zip(expected,actual)) if a != b),min(len(expected),len(actual)))
                                print(key, step["action"], "expected", [identity(bytes.fromhex(x)) for x in expected], "actual", [identity(bytes.fromhex(x)) for x in actual], file=sys.stderr)
                                if which < min(len(expected),len(actual)):
                                    a,b = bytes.fromhex(expected[which]),bytes.fromhex(actual[which])
                                    j = next((j for j,(u,v) in enumerate(zip(a[16:],b[16:])) if u != v),min(len(a),len(b))-16)
                                    print("frame",which, "body offset", j, "old/Go",a[16+j:24+j].hex(),b[16+j:24+j].hex(), file=sys.stderr)
                                print((root/"host.log").read_text(errors="replace")[-1200:], file=sys.stderr)
                                raise ValueError(f"{key} {step['action']} frame mismatch")
                            replies += len(actual)
                    if args.libpq:
                        expected = run["steps"][-1]["state"]
                        current = state(args.libpq, args.dsn_file)
                        assert len(current) == len(expected) == 2
                        for a,b in zip(expected,current):
                            for field, value in b.items():
                                old = json.loads(a[field]) if field == "gameplay_json" else a[field]
                                assert old == value,(key,field)
                    print(key, len(run["steps"]), "requests", replies, "original response frames",flush=True)


if __name__ == "__main__":
    main()
