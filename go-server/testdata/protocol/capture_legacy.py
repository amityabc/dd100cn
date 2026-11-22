"""Capture read-only reference exchanges from an isolated empty C# baseline."""
import argparse
import json
import socket
import struct
import sys
import zlib
from datetime import datetime, timezone
from pathlib import Path

sys.path.insert(0, str(Path(__file__).resolve().parents[3] / "baseline"))
from probe import exact, frame, identity, request


def original_directory(channel):
    from cryptography.hazmat.primitives.ciphers import Cipher, algorithms, modes
    key = bytes.fromhex(channel[0]["response"])[15:31]
    encrypted = zlib.decompress(bytes.fromhex(channel[2]["response"])[11:])
    decoder = Cipher(algorithms.AES(key), modes.ECB()).decryptor()
    plain = decoder.update(encrypted) + decoder.finalize()
    count = struct.unpack_from("<I", plain, 8)[0]
    if not 1 <= count <= 32 or 12 + 48 * count > len(plain):
        raise ValueError("invalid captured directory")
    rows = []
    for offset in range(12, 12 + 48 * count, 48):
        name, capacity, load, address, port = struct.unpack_from("<20sII16sI", plain, offset)
        if address.rstrip(b"\0") != b"127.0.0.1":
            raise ValueError("unexpected captured directory address")
        rows.append({"id": int(name.rstrip(b"\0").decode().removeprefix("#ch.")), "port": port,
                     "capacity": capacity, "load": load})
    return rows


def capture(ready):
    result = {
        "origin": "Synthetic client requests / actual C# TCP responses, isolated empty account",
        "capturedUtc": datetime.now(timezone.utc).isoformat(),
        "hostSha256": ready["HostSha256"],
        "runId": ready["RunId"],
        "gamePort": ready["GamePort"],
        "channelPort": ready["ChannelPort"],
        "greeting": [],
        "channel": [],
        "game": [],
    }
    with socket.create_connection(("127.0.0.1", ready["ChannelPort"]), timeout=8) as stream:
        for op, body in [(11, bytes(32)), (9, b""), (17, bytes(24))]:
            sent = struct.pack("<BBIIB", 0, op, 11 + len(body), 0, 1) + body
            stream.sendall(sent)
            header = exact(stream, 11)
            length = struct.unpack_from("<I", header, 2)[0]
            if not 11 <= length <= 1048576 or header[:2] != bytes([124, op + 1]):
                raise ValueError("Unexpected channel response")
            received = header + exact(stream, length - 11)
            result["channel"].append({"opcode": op, "request": sent.hex(), "response": received.hex()})
    result["directory"] = original_directory(result["channel"])
    with socket.create_connection(("127.0.0.1", ready["GamePort"]), timeout=8) as stream:
        for _ in range(4):
            packet = frame(stream)
            result["greeting"].append(packet.hex())
            if identity(packet) == (1, 1):
                break
        else:
            raise RuntimeError("No automatic ACK1")
        cases = [
            ("roster", 8, bytes.fromhex("ffff02")),
            ("emptyRosterProbe", 679, bytes.fromhex("2000")),
            ("ping", 1715, bytes(range(13)) + bytes.fromhex("78563412")),
            ("availableName", 692, bytes.fromhex("04000000b2e2cad4")),
            ("oneCharacterName", 692, bytes.fromhex("02000000b2e2")),
            ("absentSelectIndex", 4, bytes.fromhex("0000")),
        ]
        for sequence, (name, op, body) in enumerate(cases, 1):
            sent = request(op, sequence, body)
            stream.sendall(sent)
            if name == "absentSelectIndex":
                if stream.recv(1):
                    raise ValueError("Unexpected response to invalid selection")
                result["game"].append({"name": name, "request": sent.hex(), "closed": True})
                break
            received = frame(stream)
            if struct.unpack_from("<H", received, 11)[0] != sequence:
                raise ValueError("Unexpected game sequence")
            result["game"].append({"name": name, "request": sent.hex(), "response": received.hex()})
    return result


if __name__ == "__main__":
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("ready", type=Path)
    parser.add_argument("output", type=Path)
    args = parser.parse_args()
    captured = capture(json.loads(args.ready.read_text(encoding="utf-8-sig")))
    args.output.write_text(json.dumps(captured, indent=2) + "\n", encoding="utf-8")
