"""Check four purchased contracts at selection, at the next input after expiry, and on reconnect."""
import argparse
from contextlib import closing
from pathlib import Path
import sqlite3
import struct
import sys
import tempfile
import time
from urllib.parse import urlsplit

from pg_fixture import json_query
from test_gateway import go_host,project
sys.path.insert(0, str(project / "internal/mailwire/testdata"))
from live import old_host
from capture_town import exchange
from probe import identity

types = (22,27,97,84)
columns = ("conqueror_expires_at","skill_master_expires_at","crystal_expires_at","growth_expires_at")


def status(packets):
    result = {}
    for b in packets:
        if identity(b) == (0,66):
            assert len(b) == 27 and b[16:18] == bytes([2,0]) and b[18] not in result
            result[b[18]] = struct.unpack_from("<Q",b,19)[0]
    return result


def test(connect,deadline):
    def run(c,op,seq,body):
        answer=exchange(c,op,seq,body)
        assert not answer.get("closed"),op
        return [bytes.fromhex(x) for x in answer["responses"]]
    def select(c):
        run(c,8,2,bytes.fromhex("ffff02"))
        responses=run(c,4,4,bytes(2))
        b=next(b[16:] for b in responses if identity(b)==(1,4))
        count=b[17]
        rows={b[18+9*i]:struct.unpack_from("<Q",b,19+9*i)[0] for i in range(count)}
        return rows,status(responses)
    with connect() as c:
        low=int(time.time())
        ack,initial=select(c)
        high=int(time.time())
        assert tuple(ack)==types[:3] and tuple(initial)==types, (ack,initial)
        assert all(max(1,deadline-high)<=n<=deadline-low for n in [*ack.values(),*initial.values()])
        begin=run(c,120,6,b"")
        assert sum(identity(b)==(0,889) for b in begin)==1
        run(c,120,8,b"")
        time.sleep(max(0,deadline+.15-time.time()))
        ping=bytes(13)+struct.pack("<I",123)
        ending=run(c,1715,10,ping)
        ended=status(ending)
        assert list(ended)==list(types) and all(seconds==0 for seconds in ended.values()),ended
        assert all(struct.unpack_from("<H",b,11)[0]==10 for b in ending if identity(b)==(0,66))
        assert not any(identity(b)==(0,889) for b in ending)
        assert not status(run(c,1715,12,ping))
    with connect() as c:
        ack,expired=select(c)
        assert not ack and list(expired)==list(types) and not any(expired.values())
    return {"C4":list(types[:3]),"N66OnSelect":list(types),"N66OnExpiry":list(types),"repeatN66":0,"expiredReconnectC4Rows":0}


def main():
    p=argparse.ArgumentParser(description=__doc__)
    p.add_argument("--libpq",type=Path)
    p.add_argument("--dsn-file",type=Path)
    p.add_argument("--legacy-dotnet",type=Path)
    p.add_argument("--bin",type=Path,default=project/"bin")
    args=p.parse_args()
    cache=project/".work/mailbox-verify"
    cache.mkdir(parents=True,exist_ok=True)
    with tempfile.TemporaryDirectory(prefix="contract-",dir=cache) as raw:
        root=Path(raw)
        if args.legacy_dotnet:
            with old_host(root,"weapon",args.legacy_dotnet) as connect:
                deadline=int(time.time())+4
                with closing(sqlite3.connect(root/"runtime/data/next-local.db")) as db:
                    db.executemany("INSERT INTO local_account_premium(account,premium_type,expires_at) VALUES ('local',?,?) "
                                   "ON CONFLICT(account,premium_type) DO UPDATE SET expires_at=excluded.expires_at",[(k,deadline) for k in types])
                    db.commit()
                print("C#",test(connect,deadline))
        else:
            if not args.libpq or not args.dsn_file:
                p.error("provide --libpq and --dsn-file for the isolated premium_expiry fixture")
            parsed=urlsplit(args.dsn_file.read_text().strip())
            if parsed.hostname!="127.0.0.1" or parsed.path!="/s9a1_premium_expiry_fixture":
                p.error("requires the isolated s9a1_premium_expiry_fixture PostgreSQL database")
            account="premium-expiry-v35"
            def update(values):
                writes=",".join(f"{column}={int(n)}" for column,n in zip(columns,values))
                sql=f"WITH p AS (UPDATE s9a1.accounts SET {writes} WHERE account='{account}' RETURNING account) SELECT to_json(count(*)) FROM p"
                assert json_query(args.libpq,args.dsn_file,sql)==1
            with go_host(root,args.bin.resolve(),args.dsn_file,account) as connect:
                saved=json_query(args.libpq,args.dsn_file,"SELECT json_build_array("+",".join(columns)+f") FROM s9a1.accounts WHERE account='{account}'")
                deadline=int(time.time())+4
                try:
                    update([deadline]*4)
                    print("Go",test(connect,deadline))
                finally:
                    update(saved)


if __name__=="__main__":
    main()
