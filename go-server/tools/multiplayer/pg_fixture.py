"""Read a single JSON value from a local test database using its PostgreSQL client library."""
import ctypes as C
from contextlib import nullcontext
import json
import os
from pathlib import Path


def json_query(libpq, dsn_file, sql):
    libpq = Path(libpq).resolve()
    directory = os.add_dll_directory(str(libpq.parent)) if os.name == "nt" else nullcontext()
    with directory:
        pg = C.CDLL(str(libpq))
        sig = {
            "PQconnectdb": (C.c_void_p, [C.c_char_p]),
            "PQstatus": (C.c_int, [C.c_void_p]),
            "PQsetClientEncoding": (C.c_int, [C.c_void_p,C.c_char_p]),
            "PQexec": (C.c_void_p, [C.c_void_p,C.c_char_p]),
            "PQresultStatus": (C.c_int, [C.c_void_p]),
            "PQgetvalue": (C.c_char_p, [C.c_void_p,C.c_int,C.c_int]),
            "PQresultErrorMessage": (C.c_char_p, [C.c_void_p]),
            "PQclear": (None,[C.c_void_p]),
            "PQfinish": (None,[C.c_void_p]),
        }
        for name,(result,arguments) in sig.items():
            fn=getattr(pg,name)
            fn.argtypes,fn.restype=arguments,result
        conn=pg.PQconnectdb(dsn_file.read_bytes().strip())
        try:
            if not conn or pg.PQstatus(conn) != 0 or pg.PQsetClientEncoding(conn,b"UTF8") != 0:
                raise ValueError("cannot open the fixture database with UTF-8")
            result=pg.PQexec(conn,sql.encode())
            try:
                if pg.PQresultStatus(result)!=2:
                    raise ValueError(pg.PQresultErrorMessage(result).decode(errors="replace"))
                return json.loads(pg.PQgetvalue(result,0,0))
            finally:
                pg.PQclear(result)
        finally:
            pg.PQfinish(conn)
