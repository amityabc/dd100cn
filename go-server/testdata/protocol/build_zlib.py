"""Build the portable S9A1 compressor from zlib-ng 2.2.5 with Zig 0.13.0."""
import argparse
import hashlib
import os
import shutil
import subprocess
import tarfile
from pathlib import Path

HERE = Path(__file__).resolve().parent
BASE = HERE.parents[1]
SOURCE_SHA = "5b3b022489f3ced82384f06db1e13ba148cbce38c7941e424d6cb414416acd18"
SOURCES = [
    "adler32.c", "compress.c", "crc32.c", "crc32_braid_comb.c", "deflate.c",
    "deflate_fast.c", "deflate_huff.c", "deflate_medium.c", "deflate_quick.c",
    "deflate_rle.c", "deflate_slow.c", "deflate_stored.c", "insert_string.c",
    "insert_string_roll.c", "trees.c", "zutil.c", "arch/generic/adler32_c.c",
    "arch/generic/adler32_fold_c.c", "arch/generic/compare256_c.c",
    "arch/generic/crc32_braid_c.c", "arch/generic/crc32_fold_c.c",
    "arch/generic/slide_hash_c.c",
]


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--zig", required=True, type=Path)
    parser.add_argument("--archive", required=True, type=Path)
    args = parser.parse_args()
    archive, zig = args.archive.resolve(), args.zig.resolve()
    if hashlib.sha256(archive.read_bytes()).hexdigest() != SOURCE_SHA:
        raise ValueError("zlib-ng source archive hash mismatch")
    if subprocess.check_output([str(zig), "version"],text=True).strip() != "0.13.0":
        raise ValueError("Zig 0.13.0 is required to regenerate the frozen module")
    work = BASE / ".work" / "protocol-capture"
    work.mkdir(parents=True, exist_ok=True)
    source = work / "zlib-ng-2.2.5"
    with tarfile.open(archive) as tar:
        tar.extractall(work, filter="data")
    for name in ("zlib-ng.h", "zconf-ng.h"):
        content = (source / (name + ".in")).read_text()
        (source / name).write_text(content.replace("@ZLIB_SYMBOL_PREFIX@", ""),encoding="utf8")
    shutil.copyfile(source / "zlib_name_mangling.h.empty", source / "zlib_name_mangling-ng.h")
    env = dict(os.environ, ZIG_GLOBAL_CACHE_DIR=str(work/"zig-global"),
               ZIG_LOCAL_CACHE_DIR=str(work/"zig-local"), TEMP=str(work), TMP=str(work),
               TMPDIR=str(work), SOURCE_DATE_EPOCH="0")
    destination = BASE / "internal/protocol/zlibng.wasm"
    command = [str(zig), "cc", "-target", "wasm32-wasi", "-mexec-model=reactor",
               "-Oz", "-g0", "-DNDEBUG", "-DDISABLE_RUNTIME_CPU_DETECTION",
               "-DHAVE_BUILTIN_CTZ", "-DHAVE_BUILTIN_CTZLL", "-DHAVE_ATTRIBUTE_ALIGNED", "-DHAVE_ALIGNED_ALLOC",
               "-DHAVE_ATTRIBUTE_VISIBILITY_HIDDEN", "-DWITH_GZFILEOP=0",
               "-ffunction-sections", "-fdata-sections", "-fno-stack-protector", "-Wl,-z,stack-size=65536", "-Wl,--gc-sections",
               "-Wl,--strip-all", "-Wl,--export=wire_reserve", "-Wl,--export=wire_encode",
               "-I.", *SOURCES, str(HERE/"wire_zlib.c"), "-o", str(destination)]
    subprocess.run(command, cwd=source, env=env, check=True)
    print(hashlib.sha256(destination.read_bytes()).hexdigest(), destination.stat().st_size)


if __name__ == "__main__":
    main()
