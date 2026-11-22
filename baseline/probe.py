"""Measure complete responses on an isolated Serve100 game connection."""

import argparse
import json
import math
import socket
import sqlite3
import struct
import time
from pathlib import Path


def checksum(payload):
    table = [0] * 256
    value = 1
    for bit in (128, 64, 32, 16, 8, 4, 2, 1):
        value = (0x4DB89129 if value & 1 else 0) ^ (value >> 1)
        for start in range(0, 256, bit * 2):
            for index in range(bit):
                table[start + bit + index] = table[start + index] ^ value
    crc = 0xFFFFFFFF
    for byte in payload:
        crc = table[(crc ^ byte) & 0xFF] ^ (crc >> 8)
    return crc ^ 0xFFFFFFFF


def request(opcode, sequence, body):
    tail = struct.pack('<H', sequence) + body
    return struct.pack('<BHiI', 1, opcode, 13 + len(body), checksum(tail)) + tail


def exact(stream, count):
    result = bytearray()
    while len(result) < count:
        chunk = stream.recv(count - len(result))
        if not chunk:
            raise EOFError('Old host closed before the complete frame arrived')
        result.extend(chunk)
    return bytes(result)


def frame(stream):
    header = exact(stream, 16)
    size = struct.unpack_from('<i', header, 3)[0]
    if not 16 <= size <= 1048576:
        raise ValueError(f'Unexpected game frame length {size}')
    return header + exact(stream, size - 16)


def identity(wire):
    return struct.unpack_from('<BH', wire)


def receive_until(stream, wanted, sequence):
    trace = []
    for _ in range(128):
        wire = frame(stream)
        trace.append(identity(wire))
        if trace[-1] == wanted and struct.unpack_from('<H', wire, 11)[0] == sequence:
            return wire, trace
    raise ValueError(f'Expected {wanted} with sequence {sequence}, observed {trace}')


def create_character(ready, database, snapshot):
    """Generate a genuine character save in the isolated old Host and give the importer a consistent copy."""
    with socket.create_connection(('127.0.0.1', ready['GamePort']), timeout=15) as stream:
        stream.setsockopt(socket.IPPROTO_TCP, socket.TCP_NODELAY, 1)
        receive_until(stream, (1, 1), 0)
        stream.sendall(request(8, 1, b'\xff\xff\x02'))
        receive_until(stream, (0, 2), 1)
        name = '基线角色'.encode('gb18030')
        stream.sendall(request(5, 2, b'\x00' + struct.pack('<I', len(name)) + name))
        ack, _ = receive_until(stream, (1, 5), 2)
        if ack[16] != 1:
            raise ValueError(f'Character creation rejected: {ack[16:].hex()}')
        receive_until(stream, (0, 2), 2)
    snapshot.parent.mkdir(parents=True, exist_ok=True)
    if snapshot.exists():
        raise FileExistsError(snapshot)
    with sqlite3.connect(database.resolve().as_uri() + '?mode=ro', uri=True) as source, sqlite3.connect(snapshot) as copy:
        source.backup(copy)
        if copy.execute('PRAGMA integrity_check').fetchone() != ('ok',):
            raise ValueError('SQLite backup integrity check failed')
        row = copy.execute('SELECT id,name FROM local_entry_characters').fetchone()
        if row is None or row[1] != '基线角色':
            raise ValueError('Isolated character missing from consistent backup')
    return {'id': row[0], 'name': row[1], 'createAckBodyHex': ack[16:].hex(), 'snapshot': snapshot.name}


def measure_town(ready, database, samples=100):
    """Use the old server's native opening-skip request, then measure persisted C35 movement plus a C1715 barrier."""
    with sqlite3.connect(database.resolve().as_uri() + '?mode=ro', uri=True) as db:
        gameplay = json.loads(db.execute('SELECT gameplay_json FROM local_entry_characters WHERE id=1').fetchone()[0])
        pending = not gameplay['Tutorial']['Completed']
    with socket.create_connection(('127.0.0.1', ready['GamePort']), timeout=20) as stream:
        stream.setsockopt(socket.IPPROTO_TCP, socket.TCP_NODELAY, 1)
        receive_until(stream, (1, 1), 0)
        entered = time.perf_counter()
        stream.sendall(request(8, 1, b'\xff\xff\x02'))
        receive_until(stream, (0, 2), 1)
        stream.sendall(request(4, 2, b'\x00\x00'))
        receive_until(stream, (0, 1378), 2)
        if pending:
            stream.sendall(request(143, 3, bytes.fromhex('00 1F 00 00 00 01')))
            receive_until(stream, (0, 24), 3)
        # Formal completion may produce additional notifications. A ping with a new sequence delimits all of them.
        tick = bytes(13) + struct.pack('<I', 1234)
        stream.sendall(request(120, 4, b'') + request(120, 5, b'') + request(1715, 6, tick))
        receive_until(stream, (1, 1715), 6)
        enter_ms = round((time.perf_counter() - entered) * 1000, 3)
        with sqlite3.connect(database.resolve().as_uri() + '?mode=ro', uri=True) as db:
            x, y, revision = db.execute('SELECT x,y,revision FROM local_entry_characters WHERE id=1').fetchone()
            sequence = 7
            timings = []
            for i in range(samples + 1):
                target_x = x + (1 if i % 2 == 0 else 0)
                packets = request(35, sequence, struct.pack('<HHBH', target_x, y, 5, 1)) + request(1715, sequence + 1, tick)
                start = time.perf_counter_ns()
                stream.sendall(packets)
                receive_until(stream, (1, 1715), sequence + 1)
                if i:
                    timings.append((time.perf_counter_ns() - start) / 1e6)
                sequence += 2
            saved = db.execute('SELECT x,y,revision FROM local_entry_characters WHERE id=1').fetchone()
            if saved != (target_x, y, revision + samples + 1):
                raise ValueError(f'Movement did not persist each request, expected {(target_x,y,revision + samples + 1)}, got {saved}')
    return {'rosterSelectToTownReadyBarrierMs': enter_ms, 'usedNativeOpeningSkip': pending, 'moveThenPingBarrierMs': percentiles(timings),
            'warmupMoves': 1, 'verifiedDatabaseRevisions': saved[2] - revision}


def percentiles(values):
    ordered = sorted(values)
    def quantile(fraction):
        return round(ordered[math.ceil(len(ordered) * fraction) - 1], 3)
    return {'samples': len(ordered), 'p50Ms': quantile(.5), 'p95Ms': quantile(.95), 'p99Ms': quantile(.99)}


def measure(ready, samples=100):
    with socket.create_connection(('127.0.0.1', ready['GamePort']), timeout=10) as stream:
        stream.setsockopt(socket.IPPROTO_TCP, socket.TCP_NODELAY, 1)
        greeting = []
        for _ in range(8):
            wire = frame(stream)
            greeting.append(wire)
            if identity(wire) == (1, 1):
                break
        else:
            raise RuntimeError('No login ACK1')
        cases = [
            ('emptyRoster', 8, bytes.fromhex('FF FF 02'), (0, 2), None),
            ('emptyRosterProbe', 679, bytes.fromhex('20 00'), (1, 679), bytes.fromhex('00 00')),
            ('tickEcho', 1715, bytes(13) + bytes.fromhex('78 56 34 12'), (1, 1715), bytes.fromhex('01 78 56 34 12')),
        ]
        timings = {name: [] for name, *_ in cases}
        vectors = {}
        sequence = 1
        for index in range(samples + 1):
            # C679 is valid once after each empty C8 roster, including after reconnect.
            for name, op, body, expected, expected_body in cases:
                sent = request(op, sequence, body)
                started = time.perf_counter_ns()
                stream.sendall(sent)
                received = frame(stream)
                elapsed = (time.perf_counter_ns() - started) / 1e6
                if identity(received) != expected or struct.unpack_from('<H', received, 11)[0] != sequence:
                    raise RuntimeError(f'{name} expected {expected}, sequence {sequence}, got {identity(received)} / {received[:16].hex()}')
                if expected_body is not None and received[16:] != expected_body:
                    raise RuntimeError(f'{name} unexpected reply body {received[16:].hex()}')
                if index == 0:
                    vectors[name] = {'c2s': sent.hex(), 's2c': received.hex()}
                else:
                    timings[name].append(elapsed)
                sequence += 1
        results = {name: percentiles(values) for name, values in timings.items()}
        return {'gamePort': ready['GamePort'], 'channelPort': ready['ChannelPort'], 'greetingHex': [v.hex() for v in greeting],
                'responses': vectors, 'loopbackRtt': results, 'warmupsPerOperation': 1}


if __name__ == '__main__':
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('ready', type=Path, help='Isolated runtime/next-ready.json')
    parser.add_argument('output', type=Path)
    args = parser.parse_args()
    args.output.write_text(json.dumps(measure(json.loads(args.ready.read_text(encoding='utf-8-sig'))), indent=2) + '\n', encoding='utf-8')
