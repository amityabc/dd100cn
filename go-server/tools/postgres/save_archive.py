"""Back up the Go save and restore it to a newly created PostgreSQL database."""
import argparse
from contextlib import contextmanager
import hashlib
import json
import os
from pathlib import Path
import re
import shutil
import subprocess
import sys
import tempfile
from urllib.parse import parse_qsl, unquote, urlsplit


project = Path(__file__).resolve().parents[2]


def executable(name, directory):
    if directory is not None:
        name = str(directory / (name + ('.exe' if os.name == 'nt' else '')))
    path = shutil.which(name)
    if not path:
        raise ValueError(f'PostgreSQL executable unavailable: {name}')
    return path


@contextmanager
def connection(args):
    env = os.environ.copy()
    raw = args.dsn_file.read_text(encoding='utf-8-sig').strip() if args.dsn_file else env.get('S9A1_POSTGRES_DSN')
    if not raw:
        # Native libpq environment and named services work without a project URI.
        yield env
        return
    uri = urlsplit(raw)
    if uri.scheme not in ('postgres', 'postgresql'):
        raise ValueError('Expected a PostgreSQL URI in the connection file or S9A1_POSTGRES_DSN')
    options = dict(parse_qsl(uri.query, keep_blank_values=True))
    authority = {'host': uri.hostname, 'port': uri.port, 'user': uri.username, 'password': uri.password,
                 'dbname': uri.path.lstrip('/') or None}
    for key, value in authority.items():
        if value is not None:
            options.setdefault(key, unquote(str(value)))
    # Password stays in the child process environment. The service file keeps
    # other connection settings (including TLS and client certificates) off argv.
    if 'password' in options:
        env['PGPASSWORD'] = options.pop('password')
    work = project / '.work/postgres-backup'
    work.mkdir(parents=True, exist_ok=True)
    with tempfile.TemporaryDirectory(dir=work) as temp:
        service = Path(temp) / 'pg_service.conf'
        for key, value in options.items():
            if not key.replace('_', '').isalnum() or any(c in value for c in '\r\n\x00'):
                raise ValueError('Invalid libpq service option')
        with service.open('x', encoding='utf-8', newline='\n') as output:
            output.write('[s9a1_save]\n')
            for key, value in options.items():
                output.write(f'{key}={value}\n')
        env.update(PGSERVICE='s9a1_save', PGSERVICEFILE=str(service))
        yield env


def digest(path):
    with path.open('rb') as f:
        return hashlib.file_digest(f, 'sha256').hexdigest()


def locale_options(database):
    providers = {'c': 'libc', 'i': 'icu', 'b': 'builtin'}
    provider = providers[database['provider']]
    flags = [f'--encoding={database["encoding"]}', f'--locale-provider={provider}',
             f'--lc-collate={database["collate"]}', f'--lc-ctype={database["ctype"]}']
    if provider in ('icu', 'builtin'):
        flags.append(f'--{provider}-locale={database["locale"]}')
    if database.get('icuRules'):
        flags.append(f'--icu-rules={database["icuRules"]}')
    return flags


database_settings = """SELECT json_build_object('encoding',pg_encoding_to_char(encoding),
    'provider',datlocprovider,'collate',datcollate,'ctype',datctype,
    'locale',coalesce(to_jsonb(d)->>'datlocale',to_jsonb(d)->>'daticulocale'),
    'icuRules',to_jsonb(d)->>'daticurules') FROM pg_database d WHERE datname=current_database()"""


def database_option(name):
    # Explicit conninfo protects names containing '=' or matching a URI prefix.
    return 'dbname=\'' + name.replace('\\', '\\\\').replace("'", "\\'") + "'"


def run(args):
    directory = args.bin_dir
    names = ('psql', 'pg_dump', 'pg_restore', 'createdb')
    local = project / '.tools/pg17-client/bin'
    suffix = '.exe' if os.name == 'nt' else ''
    if directory is None:
        for candidate in (os.environ.get('S9A1_PG_BIN'), local):
            if candidate and all((Path(candidate) / (n + suffix)).is_file() for n in names):
                directory = candidate
                break
    if directory:
        directory = Path(directory).resolve()
    tools = {n: executable(n, directory) for n in names}
    archive_dir = args.archive.resolve()
    dump = archive_dir / 'save.dump'
    manifest = archive_dir / 'manifest.json'
    with connection(args) as env:
        def command(name, *options, read=False):
            result = subprocess.run([tools[name], *map(str, options)], env=env, check=True,
                                    stdout=subprocess.PIPE if read else None)
            return result.stdout.decode('utf-8', errors='replace').strip() if read else None

        def sql(query, db=None):
            where = ['-d', database_option(db)] if db else []
            return command('psql', '-X', '-A', '-t', '-w', '-v', 'ON_ERROR_STOP=1', *where, '-c', query, read=True)

        def tool_version(tool):
            output = command(tool, '--version', read=True)
            match = re.search(r'\(PostgreSQL\) (\d+(?:\.\d+)+)', output)
            if not match:
                raise ValueError(f'Unrecognized PostgreSQL version from {tool}')
            return match.group(1)

        version = tool_version('pg_dump')
        major = int(version.split('.')[0])
        endpoint = args.maintenance_db if args.action == 'restore' else None
        remote = int(sql("SHOW server_version_num", endpoint))
        if remote // 10000 != major or any(tool_version(t) != version for t in tools if t != 'pg_dump'):
            raise ValueError('Use matching PostgreSQL client tools from the same server major version')

        if args.action == 'backup':
            if sql("SELECT to_regclass('s9a1.accounts') IS NOT NULL") != 't':
                raise ValueError('Source database has no Go game-save schema')
            archive_dir.parent.mkdir(parents=True, exist_ok=True)
            archive_dir.mkdir(mode=0o700)  # Exclusive ownership. An existing backup is never replaced.
            temporary = archive_dir / 'save.dump.partial'
            try:
                command('pg_dump', '-w', '--format=custom', '--no-owner', '--no-privileges', '--no-tablespaces',
                        '--schema=s9a1', '--schema=s9a1_import', '--file', temporary)
                contents = command('pg_restore', '--list', temporary, read=True)
                if ' TABLE DATA s9a1 accounts ' not in contents:
                    raise ValueError('Archive is missing the game account table')
                header = {'format': 1, 'postgresVersion': remote, 'clientVersion': version,
                          'database': json.loads(sql(database_settings)), 'sha256': digest(temporary)}
                temporary.rename(dump)
                with manifest.open('x', encoding='utf-8') as f:
                    json.dump(header, f, indent=2)
                    f.write('\n')
            except BaseException:
                for path in (temporary, dump, manifest):
                    path.unlink(missing_ok=True)
                archive_dir.rmdir()
                raise
            print(f'Backed up {archive_dir} ({header["sha256"]})')
            return

        header = json.loads(manifest.read_text(encoding='utf-8'))
        if header.get('format') != 1 or header['postgresVersion'] // 10000 != major or header['sha256'] != digest(dump):
            raise ValueError('Backup version or SHA-256 mismatch')
        command('pg_restore', '--list', dump, read=True)
        target = args.new_database
        if not target or '\x00' in target or len(target.encode('utf-8')) > 63:
            raise ValueError('Supply a new PostgreSQL database name of at most 63 UTF-8 bytes')
        command('createdb', '-w', '--maintenance-db', database_option(args.maintenance_db), '--template=template0',
                *locale_options(header['database']), '--', target)
        command('pg_restore', '-w', '--exit-on-error', '--single-transaction', '--no-owner', '--no-privileges',
                '--no-tablespaces', '--dbname', database_option(target), dump)
        print(f'Restored {target} from {archive_dir} ({header["sha256"]})')


if __name__ == '__main__':
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('action', choices=('backup', 'restore'))
    parser.add_argument('--archive', type=Path, required=True, help='new backup directory or existing restore bundle')
    parser.add_argument('--dsn-file', type=Path, help='UTF-8 PostgreSQL URI, alternatively use S9A1_POSTGRES_DSN or native libpq settings')
    parser.add_argument('--bin-dir', type=Path, help='directory containing PostgreSQL client tools of the server major version')
    parser.add_argument('--maintenance-db', default='postgres', help='connect here to create the restore target')
    parser.add_argument('--new-database', help='fresh restore target, created using template0')
    try:
        run(parser.parse_args())
    except (OSError, ValueError, KeyError, subprocess.CalledProcessError) as e:
        sys.exit(str(e))
