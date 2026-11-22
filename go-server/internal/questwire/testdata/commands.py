"""Record and compare ordinary town C31/C32, including repeated native callbacks and one transactional quest-item grant."""
import argparse
from contextlib import closing
from datetime import datetime, timedelta, timezone
import gzip
import json
from pathlib import Path
import sqlite3
import struct
import sys
import tempfile

here=Path(__file__).resolve().parent
project=here.parents[2]
sys.path.insert(0,str(project/"tools/multiplayer"))
sys.path.insert(0,str(project/"internal/mailwire/testdata"))
from live import old_host
from test_gateway import go_host
from pg_fixture import json_query
from capture_town import exchange
from probe import checksum, identity

work=project/".work/quest-commands"
fixture=work/"v35-town-quests.sqlite3"
reference=here/"legacy-commands.json.gz"


def prepare():
    if fixture.exists():
        return
    source=project/".work/storage-verify/v35-town-equipped.sqlite3"
    with tempfile.TemporaryDirectory(dir=work) as tmp:
        new=Path(tmp)/fixture.name
        with closing(sqlite3.connect(source.resolve().as_uri()+"?mode=ro",uri=True)) as old,closing(sqlite3.connect(new)) as db:
            old.backup(db)
            profile=json.loads(db.execute("SELECT gameplay_json FROM local_entry_characters WHERE id=1").fetchone()[0])
            for pre in (3248,5653,6211):
                profile["Quests"]["Cleared"][str(pre)]=1
            db.execute("UPDATE local_entry_characters SET level=100,gameplay_json=? WHERE id=1",(json.dumps(profile,separators=(",",":")),))
            db.commit()
            assert db.execute("PRAGMA integrity_check").fetchone()==("ok",)
            db.execute("PRAGMA wal_checkpoint(TRUNCATE)")
        new.rename(fixture)


def sqlstate(legacy,source,libpq):
    if legacy:
        with closing(sqlite3.connect(source.resolve().as_uri()+"?mode=ro",uri=True)) as db:
            revision,profile=db.execute("SELECT revision,gameplay_json FROM local_entry_characters WHERE id=1").fetchone()
            bag=db.execute("SELECT slot,count,instance_id,template_json,attributes_json,hex(raw119),hex(detail) FROM local_item_assets WHERE location=0 AND json_extract(template_json,'$.TemplateId')=4365 ORDER BY slot").fetchall()
        return {"revision":revision,"quests":json.loads(profile)["Quests"],"items":[list(x[:2]) for x in bag],"uids":[x[2] for x in bag],
                "frozen":[[json.loads(x[3]),json.loads(x[4]),x[5].lower(),x[6].lower()] for x in bag]}
    return json_query(libpq,source,"""SELECT json_build_object('revision',revision,'quests',gameplay_json->'Quests',
            'items',a.items,'uids',a.uids,'frozen',a.frozen)
        FROM s9a1.characters c CROSS JOIN LATERAL (
            SELECT coalesce(json_agg(json_build_array(slot,count) ORDER BY slot),'[]') items,
                coalesce(json_agg(instance_id ORDER BY slot),'[]') uids,
                coalesce(json_agg(json_build_array(template_json,attributes_json,encode(core,'hex'),encode(detail,'hex')) ORDER BY slot),'[]') frozen
            FROM s9a1.assets WHERE account=c.account AND character_id=c.wire_id AND location=0 AND template_id=4365
        ) a WHERE c.wire_id=1""")


def record(connect,read):
    out={}
    issued=set()
    def enter(c,fresh=False):
        for op,seq,body in [(8,2,b'\xff\xff\x02'),(4,4,b'\0\0'),(120,6,b''),(120,8,b'')]:
            if fresh and op==120 and seq==6:
                operation(c,"beforeReady",32,5,3145)
            e=exchange(c,op,seq,body)
            assert not e.get("closed"),("entry",op)
    def operation(c,label,op,seq,id,prefix=False,body=None):
        if body is None:body=struct.pack("<HH",op,id) if prefix else struct.pack("<H",id)
        e=exchange(c,op,seq,body)
        e["responses"]=[x for x in e["responses"] if bytes.fromhex(x)[15]==1 or struct.unpack_from("<H",bytes.fromhex(x),11)[0]==seq]
        e["state"]=read()
        uids=e["state"].pop("uids")
        if uids:
            assert len(uids)==1 and len(uids[0].replace("-",""))==32
            issued.add(uids[0])
            assert len(issued)==1,"quest-item instance changed on replay or reacceptance"
        out[label]=e
        return e
    with connect() as c:
        enter(c,True)
        operation(c,"inactive",32,20,3145)
        operation(c,"accept",31,22,3145,True)
        operation(c,"firstRepeat",31,22,3145,True)
        operation(c,"suppressed",31,22,3145,True)
    with connect() as c:
        enter(c)
        operations=[("existingAfterReconnect",31,3145),("abandon",32,3145),("abandonRepeat",32,3145),
                    ("parent",31,3249),("child",31,3609),("blockedByChild",32,3249),
                    ("releaseChild",32,3609),("releaseParent",32,3249),("immutableQuest",31,6220),
                    ("blockedByPvf",32,6220),("acceptMaterial",31,5654),("materialRepeat",31,5654),
                    ("releaseMaterial",32,5654),("reuseMaterial",31,5654),("releaseAgain",32,5654),
                    ("unknown",31,20000),("unknownAbandon",32,20000)]
        for i,(label,op,id) in enumerate(operations):
            assert not operation(c,label,op,20+2*i,id,i%2==1).get("closed"),label
        assert operation(c,"malformed",32,80,0,body=bytes.fromhex("1f00490c")).get("closed")
    unchanged=out["beforeReady"]["state"]["revision"]
    assert out["inactive"]["state"]["revision"]==unchanged
    assert out["accept"]["state"]["revision"]==unchanged+1
    def acks(label):
        e=out[label];return [bytes.fromhex(x)[16:] for x in e["responses"] if identity(bytes.fromhex(x))[0]==1]
    assert acks("inactive")==[bytes([0,7,0])]
    assert acks("blockedByPvf")==acks("inactive")==acks("blockedByChild")==acks("abandonRepeat")
    shape={k:[identity(bytes.fromhex(x)) for x in out[k]["responses"]] for k in ("accept","firstRepeat","suppressed","acceptMaterial","materialRepeat","reuseMaterial")}
    assert len(out["accept"]["responses"])>=4 and len(out["firstRepeat"]["responses"])==1 and not out["suppressed"]["responses"],shape
    assert acks("existingAfterReconnect")==acks("firstRepeat")
    assert out["acceptMaterial"]["state"]["items"]==[[177,1]]
    assert out["releaseAgain"]["state"]["items"]==[[177,1]]
    assert len(out["materialRepeat"]["responses"])==1
    assert acks("materialRepeat")==acks("reuseMaterial")
    return out


def scheduled_cash_reset(old_wire,new_wire,periods):
    old=bytearray.fromhex(old_wire)
    new=bytes.fromhex(new_wire)
    if old==new or identity(old)!=(0,851) or identity(new)!=(0,851) or len(old)!=len(new):
        return old_wire
    now=datetime.now(timezone(timedelta(hours=8)))
    day=(now-timedelta(hours=6)).replace(hour=6,minute=0,second=0,microsecond=0)
    monday=day-timedelta(days=day.weekday())
    ends={0:0x7fff53f0,1:int((day+timedelta(days=1)).timestamp()),101:int((monday+timedelta(days=7)).timestamp()),
          102:int((day.replace(day=28)+timedelta(days=4)).replace(day=1).timestamp())}
    for i in range(20,len(old),12):
        sku,reset=struct.unpack_from("<II",new,i)
        assert reset==ends[periods[str(sku)]["Period"]],("invalid N851 reset for",sku)
        struct.pack_into("<I",old,i+4,reset)
    struct.pack_into("<I",old,7,checksum(old[11:]))
    return old.hex()


def main():
    p=argparse.ArgumentParser(description=__doc__)
    p.add_argument("--prepare",action="store_true")
    p.add_argument("--legacy-dotnet",type=Path)
    p.add_argument("--dsn-file",type=Path)
    p.add_argument("--libpq",type=Path)
    p.add_argument("--bin",type=Path,default=project/"bin")
    args=p.parse_args()
    work.mkdir(parents=True,exist_ok=True)
    if args.prepare:
        prepare();print(fixture);return
    with tempfile.TemporaryDirectory(dir=work) as directory:
        root=Path(directory)
        if args.legacy_dotnet:
            with old_host(root,"weapon",args.legacy_dotnet,fixture) as connect:
                data=record(connect,lambda:sqlstate(True,root/"runtime/data/next-local.db",None))
            reference.write_bytes(gzip.compress(json.dumps(data,separators=(",",":")).encode(),mtime=0))
        else:
            if not args.dsn_file or not args.libpq:p.error("fresh quest test database and libpq required")
            with go_host(root,args.bin.resolve(),args.dsn_file,"quest-wire-v35") as connect:
                data=record(connect,lambda:sqlstate(False,args.dsn_file,args.libpq))
            expected=json.loads(gzip.decompress(reference.read_bytes()))
            catalog=json.loads((project/".work/catalog/characters.json").read_text())
            periods=catalog["CashPurchasePolicies"]|catalog["CharacterCashPurchasePolicies"]
            for key in expected:
                actual=data[key]["responses"]
                if len(expected[key]["responses"])==len(actual):
                    expected[key]["responses"]=[scheduled_cash_reset(o,n,periods) for o,n in zip(expected[key]["responses"],actual)]
                if data[key]!=expected[key]:
                    print(key, "expected", [identity(bytes.fromhex(x)) for x in expected[key]["responses"]], "actual", [identity(bytes.fromhex(x)) for x in data[key]["responses"]])
                    for n,(o,a) in enumerate(zip(expected[key]["responses"],data[key]["responses"])):
                        if o!=a:
                            b,v=bytes.fromhex(o)[16:],bytes.fromhex(a)[16:]
                            i=next((i for i,(x,y) in enumerate(zip(b,v)) if x!=y),min(len(b),len(v)))
                            print("frame",n,"body",i,b[i:i+12].hex(),v[i:i+12].hex())
                    if data[key]["state"]!=expected[key]["state"]:print(data[key]["state"],expected[key]["state"])
                    raise ValueError(key)
    print(len(data),"requests",sum(len(e["responses"]) for e in data.values()),"original response frames; ending revision",data["releaseAgain"]["state"]["revision"])


if __name__=="__main__":main()
