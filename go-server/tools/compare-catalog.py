#!/usr/bin/env python3
"""Compare Go and C# S9A1 catalog JSON files or GM ZIPs."""

import argparse
from decimal import Decimal
import json
from pathlib import Path
import sys
from zipfile import BadZipFile, ZipFile


# Only outer rows from native dictionaries with a Go lookup by the same key.
# Keep the contents of each row in their original order.
KEYED_ARRAYS = {
    # dungeon/catalog.go, Catalog.Gate uses town and area as the lookup key.
    ("Dungeons", "Entrances"): ("Town", "Area", "Gate"),
    # dungeon/independent.go, independentDropData.source uses owner and code.
    ("Dungeons", "Drops", "Independent", "Entries"): ("Owner", "Code"),
    # dungeon/challenge_cards.go, CardData.paidPlan uses dungeon and stage.
    ("Dungeons", "Cards", "Challenge"): ("Dungeon", "Stage"),
    # host/commerce_shop.go selects a unique SKU by these four values. The
    # merchant counter packet sorts IDs inside each counter group before send.
    ("Commerce", "Products"): ("Npc", "Shop", "Job", "TemplateId"),
    ("Commerce", "DungeonProducts"): ("Npc", "Shop", "Job", "TemplateId"),
    ("Commerce", "TownPeers"): ("Npc", "Shop"),
}

# Town visibility checks use membership by NPC ID, including in a saved area.
NPC_SETS = {("Commerce", "Visibility", "Hidden"), ("Commerce", "Visibility", "Blocked")}


def json_object(pairs):
    result = {}
    for key, value in pairs:
        if key in result:
            raise ValueError(f"duplicate JSON member {key!r}")
        result[key] = value
    return result


def invalid_constant(name):
    raise ValueError(f"invalid JSON constant {name}")


def read_json(stream):
    return json.load(
        stream,
        parse_float=Decimal,
        parse_constant=invalid_constant,
        object_pairs_hook=json_object,
    )


def property_path(path, key):
    if key.isascii() and key.isidentifier():
        return f"{path}.{key}"
    return f"{path}[{json.dumps(key, ensure_ascii=True)}]"


def display(value):
    if isinstance(value, list):
        return f"<array of {len(value)} elements>"
    if isinstance(value, dict):
        return f"<object of {len(value)} members>"
    if isinstance(value, str):
        if len(value) > 120:
            return json.dumps(value[:120], ensure_ascii=True) + "..."
        return json.dumps(value, ensure_ascii=True)
    if isinstance(value, Decimal):
        return str(value)
    return json.dumps(value)


def key_rows(rows, fields, path, side):
    result = {}
    for i, row in enumerate(rows):
        if not isinstance(row, dict) or any(type(row.get(name)) is not int for name in fields):
            raise ValueError(f"{side} {path}[{i}] is missing an integer key in {fields}")
        key = tuple(row[name] for name in fields)
        if key in result:
            raise ValueError(f"{side} {path}[{i}] duplicates key {key}")
        result[key] = row
    return result


class Comparison:
    def __init__(self, limit):
        self.limit = limit
        self.count = 0

    def difference(self, path, description):
        self.count += 1
        if self.count <= self.limit:
            print(f"{path}: {description}")

    def compare(self, native, reference, path="$", fields=()):
        if isinstance(native, dict) and isinstance(reference, dict):
            for key in sorted(native.keys() | reference.keys()):
                at = property_path(path, key)
                if key not in native:
                    self.difference(at, "missing from native")
                elif key not in reference:
                    self.difference(at, "missing from reference")
                else:
                    self.compare(native[key], reference[key], at, (*fields, key))
            return
        if isinstance(native, list) and isinstance(reference, list):
            if fields in NPC_SETS:
                for rows in (native, reference):
                    if any(type(value) is not int for value in rows) or len(set(rows)) != len(rows):
                        raise ValueError(f"{path} requires distinct integer NPC IDs")
                for value in sorted(set(native) ^ set(reference)):
                    self.difference(f"{path}[NPC={value}]", "missing from reference" if value in native else "missing from native")
                return
            if key_fields := KEYED_ARRAYS.get(fields):
                lhs = key_rows(native, key_fields, path, "native")
                rhs = key_rows(reference, key_fields, path, "reference")
                for key in sorted(lhs.keys() | rhs.keys()):
                    label = ",".join(f"{name}={value}" for name, value in zip(key_fields, key))
                    at = f"{path}[{label}]"
                    if key not in lhs:
                        self.difference(at, "missing from native")
                    elif key not in rhs:
                        self.difference(at, "missing from reference")
                    else:
                        self.compare(lhs[key], rhs[key], at, (*fields, None))
                return
            if len(native) != len(reference):
                self.difference(path, f"native length {len(native)}, reference length {len(reference)}")
            for i, (left, right) in enumerate(zip(native, reference)):
                self.compare(left, right, f"{path}[{i}]", (*fields, None))
            return
        numeric = (int, Decimal)
        if (type(native) is type(reference) or type(native) in numeric and type(reference) in numeric) and native == reference:
            return
        self.difference(path, f"native {display(native)}, reference {display(reference)}")


def members(archive, side):
    result = {}
    for info in archive.infolist():
        if info.is_dir():
            continue
        if info.filename in result:
            raise ValueError(f"{side} ZIP contains duplicate member {info.filename!r}")
        result[info.filename] = info
    return result


def same_bytes(a, b):
    while True:
        x, y = a.read(65536), b.read(65536)
        if x != y:
            return False
        if not x:
            return True


def compare_zip(comparison, native, reference):
    with ZipFile(native) as left, ZipFile(reference) as right:
        lhs, rhs = members(left, "native"), members(right, "reference")
        for name in sorted(lhs.keys() | rhs.keys()):
            path = "$zip[" + json.dumps(name, ensure_ascii=True) + "]"
            if name not in lhs:
                comparison.difference(path, "missing from native")
            elif name not in rhs:
                comparison.difference(path, "missing from reference")
            else:
                with left.open(lhs[name]) as a, right.open(rhs[name]) as b:
                    if name.lower().endswith(".json"):
                        comparison.compare(read_json(a), read_json(b), path, (None,))
                    elif not same_bytes(a, b):
                        comparison.difference(path, "member bytes differ")


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("native", type=Path, help="catalog produced by Go")
    parser.add_argument("reference", type=Path, help="reference catalog produced by C#")
    parser.add_argument("--limit", type=int, default=40, help="maximum reported paths (the total is always counted)")
    args = parser.parse_args()
    if args.limit < 0:
        parser.error("--limit must be nonnegative")
    comparison = Comparison(args.limit)
    try:
        if args.native.suffix.lower() == args.reference.suffix.lower() == ".zip":
            compare_zip(comparison, args.native, args.reference)
        elif args.native.suffix.lower() == args.reference.suffix.lower() == ".json":
            with args.native.open("rb") as native, args.reference.open("rb") as reference:
                comparison.compare(read_json(native), read_json(reference))
        else:
            parser.error("both input files must be JSON or both must be ZIP")
    except (OSError, ValueError, BadZipFile) as exc:
        print(f"Unable to compare catalogs: {exc}", file=sys.stderr)
        return 2
    print(f"Differences: {comparison.count}")
    if comparison.count > args.limit:
        print(f"Paths omitted: {comparison.count - args.limit}")
    return int(comparison.count > 0)


if __name__ == "__main__":
    sys.exit(main())
