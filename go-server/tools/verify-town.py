"""Replay the original C# sequence on a running, empty, isolated Go/PG instance."""
import argparse
import gzip
import json
from pathlib import Path
import socket
import struct
import subprocess
import sys
import time
import uuid
from datetime import datetime, timedelta, timezone

project = Path(__file__).resolve().parents[1]
sys.path.insert(0, str(project.parent / 'baseline'))
from probe import checksum, frame, identity, percentiles, receive_until, request


def validate_wire(wire):
    if len(wire) != int.from_bytes(wire[3:7], 'little'):
        raise ValueError('native frame length mismatch')
    if wire[15] == 1:
        if identity(wire) != (0, 356) or wire[7:15] != bytes([1]) + bytes(7):
            raise ValueError('unexpected compressed header')
    elif checksum(wire[11:]) != int.from_bytes(wire[7:11], 'little'):
        raise ValueError('native CRC mismatch')


def compare(action, expected, actual, state):
    if len(expected) != len(actual):
        raise ValueError(f'{action} packet order/count differs: expected {[identity(b) for b in expected]}, got {[identity(b) for b in actual]}')
    for pos, (old, new) in enumerate(zip(expected, actual)):
        validate_wire(new)
        if identity(old) != identity(new) or old[11:16] != new[11:16] or len(old) != len(new):
            raise ValueError(f'{action} frame {pos}: old {identity(old)}/{len(old)}, Go {identity(new)}/{len(new)}')
        op = identity(old)
        a, b = bytearray(old[16:]), bytearray(new[16:])
        if op == (1, 4):
            created = struct.unpack_from('<I', b, 5)[0]
            if created < state['begin']-2 or created > time.time()+2 or state.setdefault('created', created) != created:
                raise ValueError('selected character creation time mismatch')
            b[5:9] = a[5:9]
        elif op == (0, 29):
            seed = struct.unpack_from('<I', b, 3)[0]
            if state.setdefault('seed', seed) != seed:
                raise ValueError('dungeon seed changed within one run')
            b[3:7] = a[3:7]
        elif op == (0, 851):
            # A fresh account's calendar windows may move since the original capture.
            zone = timezone(timedelta(hours=8))
            now = datetime.now(zone)
            day = (now-timedelta(hours=6)).replace(hour=6, minute=0, second=0, microsecond=0)
            monday = day-timedelta(days=day.weekday())
            next_month = (day.replace(day=28)+timedelta(days=4)).replace(day=1)
            for i in range(4,len(b),12):
                sku = struct.unpack_from('<I',b,i)[0]
                period = state['periods'][str(sku)]['Period']
                target = {0: 0x7fff53f0, 1: int((day+timedelta(days=1)).timestamp()),
                          101: int((monday+timedelta(days=7)).timestamp()),102: int(next_month.timestamp())}[period]
                if struct.unpack_from('<I',b,i+4)[0] != target:
                    raise ValueError(f'{action}: wrong reset for item {sku}')
                b[i+4:i+8] = a[i+4:i+8]
        if a != b:
            i = next(n for n in range(len(a)) if a[n] != b[n])
            raise ValueError(f'{action} frame {pos} {op} body differs at {i}: old {a[i:i+12].hex()}, Go {b[i:i+12].hex()}')


def database_reader(dsn):
    observer = project / 'bin/town-state.exe'
    def read():
        result = subprocess.run([observer, '-dsn-file', dsn], check=True, stdout=subprocess.PIPE)
        return json.loads(result.stdout)
    return read


def check_state(before, after, created):
    for key, value in before.items():
        if key in ('stats_json', 'gameplay_json'):
            value = json.loads(value)
        if key == 'gameplay_json':
            day = after[key]['Progression']['FatigueDay']
            today = (datetime.now(timezone.utc) + timedelta(hours=2)).strftime('%Y%m%d')
            if day != int(today):
                raise ValueError('saved fatigue reset day differs from the native game day')
            value['Progression']['FatigueDay'] = day
        if key == 'created':
            value = created
        if value != after[key]:
            raise ValueError(f'database field {key} differs from the old save')


def check_receipts(expected, actual):
    def normalize(rows):
        output = {}
        run = owner = None
        for key, entry in rows.items():
            source, reward = json.loads(entry['Input']), json.loads(entry['Result'])
            run_id, owner_id = uuid.UUID(source.pop('RunId')), uuid.UUID(source.pop('OwnerGuid'))
            actor = source['Actor']
            native_key = f"dungeon-death:{run_id.hex}:{source['MazeIndex']}:{source['RoomX']}:{source['RoomY']}:{actor}"
            if native_key != key or key != entry['Key'] or actor in output:
                raise ValueError('inconsistent death receipt identity')
            if not run_id.int or not owner_id.int or run not in (None, run_id) or owner not in (None, owner_id):
                raise ValueError('inconsistent run or owner across registered deaths')
            run, owner = run_id, owner_id
            elapsed = reward['ClearElapsedMilliseconds']
            if reward['DungeonCleared']:
                if not 0 <= elapsed <= 0xffffffff:
                    raise ValueError('invalid completion duration')
                reward['ClearElapsedMilliseconds'] = 0
            output[actor] = (source, reward, entry['Revision'], entry['Grants'])
        return output
    if normalize(expected) != normalize(actual):
        raise ValueError('committed combat receipts differ from the C# database')


def replay(port, groups, catalog, read_state=None):
    state = {'begin': time.time(), 'periods': {**catalog['CashPurchasePolicies'],**catalog['CharacterCashPurchasePolicies']}}
    summary = {}
    for name, records in groups:
        with socket.create_connection(('127.0.0.1', port), timeout=10) as s:
            s.setsockopt(socket.IPPROTO_TCP, socket.TCP_NODELAY, 1)
            greeting = [frame(s), frame(s)]
            for b in greeting: validate_wire(b)
            if [identity(b) for b in greeting] != [(0,1),(1,1)]:
                raise ValueError('unexpected login sequence')
            for record in records:
                act = name + '/' + record['action']
                wanted = [bytes.fromhex(x) for x in record['responses']]
                raw = bytes.fromhex(record['request'])
                seq = struct.unpack_from('<H',raw,11)[0]+1
                s.sendall(raw+request(1715,seq,bytes(13)+struct.pack('<I',seq)))
                actual = []
                while len(actual)<160:
                    try: b=frame(s)
                    except (EOFError, ConnectionResetError) as exc: raise RuntimeError(f'{act} closed after {[identity(x) for x in actual]}') from exc
                    validate_wire(b)
                    if identity(b)==(1,1715) and struct.unpack_from('<H',b,11)[0]==seq: break
                    actual.append(b)
                else: raise ValueError(f'{act}: missing barrier')
                compare(act,wanted,actual,state)
                summary[act]=len(actual)
                if read_state is not None and name=='travel':
                    check_state(record['state'],read_state(),state['created'])
        if read_state is not None:
            check_state(records[-1]['state'][0] if isinstance(records[-1]['state'],list) else records[-1]['state'],
                        read_state(),state['created'])
    return summary


def measure_movement(port, read_state):
    # Match the original measure_town driver, including the single write used as warmup.
    if read_state()['gameplay_json']['Tutorial']['Completed'] is not True:
        raise ValueError('movement benchmark requires the completed isolated replay character')
    tick = bytes(13) + struct.pack('<I', 1234)
    with socket.create_connection(('127.0.0.1',port),timeout=20) as conn:
        conn.setsockopt(socket.IPPROTO_TCP,socket.TCP_NODELAY,1)
        receive_until(conn,(1,1),0)
        conn.sendall(request(8,1,b'\xff\xff\x02'))
        receive_until(conn,(0,2),1)
        conn.sendall(request(4,2,bytes(2)))
        receive_until(conn,(0,1378),2)
        conn.sendall(request(120,4,b'')+request(120,5,b'')+request(1715,6,tick))
        receive_until(conn,(1,1715),6)
        before = read_state()
        timings = []
        for i in range(101):
            target = before['x'] + (1 if i%2==0 else 0)
            seq = 7+2*i
            data = request(35,seq,struct.pack('<HHBH',target,before['y'],5,1))+request(1715,seq+1,tick)
            start = time.perf_counter_ns()
            conn.sendall(data)
            receive_until(conn,(1,1715),seq+1)
            if i:
                timings.append((time.perf_counter_ns()-start)/1e6)
        after = read_state()
        if (after['x'],after['y'],after['revision'])!=(target,before['y'],before['revision']+101):
            raise ValueError('movement benchmark did not commit all 101 requested positions')
    return {'moveThenPingBarrierMs':percentiles(timings),'verifiedDatabaseRevisions':101,'warmupMoves':1}


if __name__=='__main__':
    parser=argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--ready',type=Path,default=project/'.work/instance/runtime/go-ready.json')
    parser.add_argument('--catalog',type=Path,default=project/'.work/catalog/characters.json')
    parser.add_argument('--combat',action='store_true',help='use another empty database to replay the complete four-room opening')
    parser.add_argument('--dsn-file', type=Path, help='compare committed PostgreSQL fields using bin/town-state.exe')
    parser.add_argument('--measure-only',action='store_true',help='time movement on the already-completed local replay character')
    parser.add_argument('--output',type=Path,help='save the verification summary')
    args=parser.parse_args()
    name='legacy-tutorial-combat.json.gz' if args.combat else 'legacy-town.json.gz'
    source=json.loads(gzip.decompress((project/'testdata/protocol'/name).read_bytes()))
    groups=[('combat',source['combat']['entry']+source['combat']['path'])] if args.combat else [(k,source[k]) for k in ('opening','town')]
    port=json.loads(args.ready.read_text(encoding='utf-8'))['Ports']['Games']['19']
    read_state=database_reader(args.dsn_file.resolve()) if args.dsn_file else None
    if args.measure_only:
        if read_state is None: parser.error('--measure-only requires --dsn-file')
        result=measure_movement(port,read_state)
    else:
        if not args.combat:
            travel=project/'testdata/protocol/legacy-travel.json.gz'
            groups.append(('travel',json.loads(gzip.decompress(travel.read_bytes()))['travel']))
        actions=replay(port,groups,json.loads(args.catalog.read_text(encoding='utf-8')),read_state)
        result={'actions':actions,'originalResponseFrames':sum(actions.values()),'databaseChecked':read_state is not None}
        if read_state and args.combat:
            expected=json.loads(source['combat']['receipts'][0]['receipts_json'])
            check_receipts(expected,read_state()['receipts'])
            result['verifiedDeathReceipts']=len(expected)
    report=json.dumps(result,ensure_ascii=False,indent=2)
    if args.output:
        args.output.parent.mkdir(parents=True,exist_ok=True)
        args.output.write_text(report+'\n',encoding='utf-8')
    print(report)
