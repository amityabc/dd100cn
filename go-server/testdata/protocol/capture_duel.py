"""Record the original two duel lobbies against a private copy of an old save."""
import argparse
from contextlib import closing
import gzip
import json
import os
from pathlib import Path
import shutil
import socket
import sqlite3
import struct
import subprocess
import tempfile
import time
import uuid

from capture_legacy import original_directory, exact, frame, identity
from capture_town import HERE, exchange, saved_player


def ports(ready):
    channel=[]
    with socket.create_connection(("127.0.0.1",ready["ChannelPort"]),timeout=20) as sock:
        for op,payload in [(11,bytes(32)),(9,b""),(17,bytes(24))]:
            sock.sendall(struct.pack("<BBIIB",0,op,11+len(payload),0,1)+payload)
            h=exact(sock,11);wire=h+exact(sock,struct.unpack_from("<I",h,2)[0]-11)
            channel.append({"response":wire.hex()})
    return {row["id"]:row["port"] for row in original_directory(channel)}


def session(port,root,actions):
    with socket.create_connection(("127.0.0.1",port),timeout=20) as sock:
        sock.setsockopt(socket.IPPROTO_TCP,socket.TCP_NODELAY,1)
        greet=[]
        while len(greet)<5:
            pkt=frame(sock);greet.append(pkt.hex())
            if identity(pkt)==(1,1):break
        else:raise ValueError("missing duel login")
        steps=[]
        for i,(label,op,body) in enumerate(actions,1):
            item=exchange(sock,op,i*2,body)
            item.update(action=label,state=saved_player(root))
            steps.append(item)
            if item.get("closed"):break
        return {"greeting":greet,"steps":steps}


def seed(root,baseline):
    directory=root/"runtime/data";directory.mkdir(parents=True)
    original=(baseline/"fixtures/v35-character.sqlite3").as_uri()+"?mode=ro&immutable=1"
    with closing(sqlite3.connect(original,uri=True)) as old, closing(sqlite3.connect(directory/"next-local.db")) as db:
        old.backup(db)
        row=db.execute("SELECT gameplay_json FROM local_entry_characters WHERE id=1").fetchone()
        g=json.loads(row[0]);g["Tutorial"]["Completed"]=True
        db.execute("UPDATE local_entry_characters SET level=50,gameplay_json=? WHERE id=1",(json.dumps(g,separators=(",",":")),))
        db.commit()

def snapshot(root,destination):
    # The importer receives the exact pre-lobby v35 data, including any
    # changes in the live WAL. Keep the original and the running save intact.
    destination = destination.resolve()
    destination.parent.mkdir(parents=True, exist_ok=True)
    with closing(sqlite3.connect((root/"runtime/data/next-local.db").as_uri()+"?mode=ro",uri=True)) as db:
        with closing(sqlite3.connect(destination)) as copy:
            db.backup(copy)
            if copy.execute("PRAGMA integrity_check").fetchone()!=("ok",):
                raise ValueError("copied fixture failed SQLite integrity_check")


def main():
    p=argparse.ArgumentParser(description=__doc__)
    p.add_argument("--dotnet",type=Path,required=True);p.add_argument("--baseline",type=Path,required=True)
    p.add_argument("--output",type=Path,default=HERE/"legacy-duel-lobby.json.gz")
    p.add_argument("--save",type=Path,help="keep an independent pre-lobby SQLite backup for PostgreSQL replay")
    args=p.parse_args()
    baseline=args.baseline.resolve();work=HERE.parents[1]/".work"/"duel-capture";work.mkdir(parents=True,exist_ok=True)
    with tempfile.TemporaryDirectory(dir=work,prefix="duel-") as temp:
        root=Path(temp);seed(root,baseline)
        (root/"config").mkdir();shutil.copyfile(baseline/"run/config/server.json",root/"config/server.json")
        sdk=args.dotnet.resolve();host=baseline/"source/Server/artifacts/bin/Serve100.Host/release/Serve100.Host.dll";run=uuid.uuid4().hex
        env=dict(os.environ,DOTNET_ROOT=str(sdk.parent),DOTNET_CLI_TELEMETRY_OPTOUT="1",TEMP=str(root),TMP=str(root))
        with (root/"launcher.log").open("wb") as log:
            proc=subprocess.Popen([str(sdk),str(host),"--root",str(root),"--run-id",run],cwd=root,env=env,stdout=log,stderr=subprocess.STDOUT,
                creationflags=getattr(subprocess,"CREATE_NO_WINDOW",0))
            try:
                deadline=time.monotonic()+180;slot=root/"runtime/next-ready.json"
                while proc.poll() is None and time.monotonic()<deadline:
                    if slot.is_file():
                        ready=json.loads(slot.read_text(encoding="utf-8-sig"))
                        if ready["RunId"]==run and ready["ProcessId"]==proc.pid:break
                    time.sleep(.12)
                else:raise RuntimeError("original duel host failed to become ready")
                channels=ports(ready)
                select=("select",4,bytes(2));roster=("roster",8,bytes.fromhex("ffff02"))
                info=("preflight",195,b"");name="大厅新角".encode("gb18030")
                add=("createNewLevelOne",5,b"\0"+struct.pack("<I",len(name))+name)
                session(channels[19],root,[roster,add])
                if args.save is not None:
                    snapshot(root,args.save)
                result={"origin":"Original C# with matched PVF and isolated synthetic level-50 fixture; C5 creates a real level-1 second slot",
                        "seed":"Original v35 C5 backup copied privately, first character level=50 and Tutorial.Completed=true; other character facts preserved",
                        "hostSha256":ready["HostSha256"],"runId":run,"channels":channels,
                        "nativeTownProfile140":ready["NativeTownProfile140"],"nativePartyNotices":ready["NativePartyNotices"]}
                for ch in (251,252):
                    run_steps=[info,roster,select,("preflightBeforeSync",195,b""),("bootstrap",143,bytes(6)),("preflightReady",195,b""),
                               ("sync",120,b""),("move",35,struct.pack("<HHBH",480,234,5,100)),
                               ("syncAfterMove",143,bytes(6)),("return",7,b""),("preflightAfterReturn",195,b"")]
                    result[str(ch)]=session(channels[ch],root,run_steps)
                    result[str(ch)+"LowLevel"]=session(channels[ch],root,[roster,("lowSelect",4,b"\x01\x00")])
                    # Source test LocalEntryTests.Arcade fixes the native button
                    # as this exact independent vector. Full PVF decides each road.
                    button=bytes.fromhex("0A0000000426022C01050A00000000000000000000")
                    exit=bytes.fromhex("0A000000011603DC00050A00000004000000000000")
                    toward_main=struct.pack("<IBHHBIB6x",10,0,474,234,5,10,1)
                    roads=[select,("earlyButton",36,button),("enterWith120",120,b""),
                           ("motion",623,struct.pack("<HHBHBH3x",480,234,9,100,0,0)),
                           ("button",36,button),("repeatButton",36,button),
                           ("exitRoad",36,exit),("repeatExit",36,exit),("toMain",36,toward_main),
                           ("staleExit",36,exit),("syncNative143",143,bytes.fromhex("001f00000001")),
                           ("return",7,b"")]
                    result[str(ch)+"Roads"]=session(channels[ch],root,roads)
                    print(ch,[(e["action"],len(e["responses"]),e.get("closed",False)) for e in result[str(ch)]["steps"]],flush=True)
                args.output.write_bytes(gzip.compress(json.dumps(result,separators=(",",":")).encode(),mtime=0))
            finally:
                if proc.poll() is None:
                    (root/"runtime/stop.request").write_text("stop\n",encoding="utf-8")
                    try:proc.wait(timeout=30)
                    except subprocess.TimeoutExpired:proc.terminate();proc.wait(timeout=20)


if __name__=="__main__":
    main()
