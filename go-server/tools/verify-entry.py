"""Verify the Go TCP host against the captured empty C# account. Both use isolated data."""
import argparse
import json
import socket
import struct
import sys
from pathlib import Path

project = Path(__file__).resolve().parents[1]
sys.path.insert(0, str(project.parent / 'baseline'))
from probe import checksum, frame, identity, measure


def verify(ready, reference):
    ports = ready['Ports']
    game_port = ports['Games']['19']
    observed = []
    with socket.create_connection(('127.0.0.1', game_port), timeout=10) as conn:
        greeting = [frame(conn), frame(conn)]
        for wire in greeting:
            if checksum(wire[11:]) != int.from_bytes(wire[7:11], 'little'):
                raise ValueError('Invalid greeting CRC')
        if [identity(w) for w in greeting] != [(0, 1), (1, 1)]:
            raise ValueError('Unexpected N1/ACK1 order')
        original = bytes.fromhex(reference['greeting'][1])
        expected = bytearray(original)
        # The legacy login advertises the actual bound game port after its length-prefixed IP.
        port_offset = 16 + 13 + len(b'127.0.0.1')
        if int.from_bytes(expected[port_offset:port_offset+4], 'little') != reference['gamePort']:
            raise ValueError('Historical login port expectation changed')
        struct.pack_into('<I', expected, port_offset, game_port)
        struct.pack_into('<I', expected, 7, checksum(expected[11:]))
        if greeting[1] != expected:
            raise ValueError('Automatic login differs from old host')
        for exchange in reference['game']:
            conn.sendall(bytes.fromhex(exchange['request']))
            if exchange.get('closed'):
                if conn.recv(1):
                    raise ValueError('Invalid selection returned unexpected data')
            elif frame(conn) != bytes.fromhex(exchange['response']):
                raise ValueError('Wire response differs for ' + exchange['name'])
            observed.append(exchange['name'])
    benchmark = measure({'GamePort': game_port, 'ChannelPort': ports['Channel']})
    return {'checkedGameExchanges': observed, 'loginOrder': ['N1', 'ACK1'],
            'loopbackRttMs': benchmark['loopbackRtt'], 'samplesPerOperation': 100,
            'warmupsPerOperation': benchmark['warmupsPerOperation']}


if __name__ == '__main__':
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--ready', type=Path, default=project/'.work/instance/runtime/go-ready.json')
    parser.add_argument('--output', type=Path)
    args = parser.parse_args()
    result = verify(json.loads(args.ready.read_text(encoding='utf-8')),
                    json.loads((project/'testdata/protocol/legacy-empty.json').read_text(encoding='utf-8')))
    report = json.dumps(result, ensure_ascii=False, indent=2)
    if args.output:
        args.output.parent.mkdir(parents=True, exist_ok=True)
        args.output.write_text(report + '\n', encoding='utf-8')
    print(report)
