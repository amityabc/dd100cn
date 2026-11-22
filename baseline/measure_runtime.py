"""Start two isolated old-host processes with the real project PVF, then report build/startup/TCP measurements."""

import argparse
import hashlib
import json
import os
import platform
import subprocess
import time
import uuid
from datetime import datetime, timezone
from pathlib import Path

from measure_build import BASELINE, PROJECT, benchmark_paths, write_json
from probe import create_character, measure, measure_town


def read_json(path):
    return json.loads(path.read_text(encoding='utf-8-sig'))


def run_hosts(work, sdk):
    work, sdk, env = benchmark_paths(work, sdk)
    server = work / 'source/Server'
    host = server / 'artifacts/bin/Serve100.Host/release/Serve100.Host.dll'
    if not host.is_file():
        raise FileNotFoundError('Run measure_build.py first')
    run = work / 'run'
    # A fresh run starts without any old save, stop request, ready receipt or resource cache.
    run.mkdir()
    (run / 'config').mkdir()
    client = PROJECT / '客户端'
    config = read_json(server / 'config/server.json')
    config['Server'].update(ChannelPort=0, GamePort=0)
    config['Gameplay'].update(ClientDirectory=str(client), ChannelScript=str(server / 'Data/channel_info.etc'))
    config['Resources'].update(Pvf=str(client / 'Script.pvf'), ClientProfile=str(server / 'Data/Pvf/Client100Profile.json'))
    write_json(run / 'config/server.json', config)
    slot = run / 'runtime/next-ready.json'
    for label in ('cold', 'warm'):
        runid = uuid.uuid4().hex
        with (work / f'logs/runtime-{label}.out').open('wb') as out, (work / f'logs/runtime-{label}.err').open('wb') as err:
            started = time.perf_counter()
            proc = subprocess.Popen([str(sdk), str(host), '--root', str(run), '--run-id', runid], env=env, cwd=run,
                                    stdout=out, stderr=err, creationflags=subprocess.CREATE_NO_WINDOW)
            try:
                while time.perf_counter() - started < 900:
                    if proc.poll() is not None:
                        raise RuntimeError(f'{label} host exited {proc.returncode}. Inspect its logs')
                    if slot.is_file():
                        try:
                            ready = read_json(slot)
                        except (OSError, ValueError):
                            ready = {}
                        if ready.get('ProcessId') == proc.pid and ready.get('RunId') == runid:
                            break
                    time.sleep(.025)
                else:
                    raise TimeoutError('Old host did not publish its own ready file within 15 minutes')
                write_json(work / f'runtime-{label}.json', {'seconds': time.perf_counter() - started, 'pollMs': 25})
                write_json(work / f'{label}-probe.json', measure(ready))
                if label == 'warm':
                    database = run / 'runtime/data/next-local.db'
                    write_json(work / 'character.json', create_character(ready, database, work / 'fixtures/v35-character.sqlite3'))
                    write_json(work / 'town.json', measure_town(ready, database))
            finally:
                if proc.poll() is None:
                    (run / 'runtime').mkdir(exist_ok=True)
                    (run / 'runtime/stop.request').write_text('stop\n', encoding='utf-8')
                    try:
                        proc.wait(timeout=20)
                    except subprocess.TimeoutExpired:
                        proc.terminate()
                        proc.wait(timeout=15)
                        raise RuntimeError('The owned old host did not stop gracefully')


def summarize(work):
    project_client = PROJECT / '客户端'
    result = {'source': 'S9A1 isolated Release host, original protocol and real client resources',
              'measuredUtcDate': datetime.fromtimestamp((work / 'runtime-cold.json').stat().st_mtime, timezone.utc).strftime('%Y-%m-%d'),
              'machine': {'windows': platform.version(), 'logicalProcessors': os.cpu_count()},
              'build': read_json(work / 'build-summary.json'), 'startup': {}, 'loopbackRttMs': {}}
    for label in ('cold', 'warm'):
        phases = []
        rates = {}
        with (work / f'logs/runtime-{label}.out').open(encoding='utf-8-sig', errors='replace') as f:
            for line in f:
                try:
                    record = json.loads(line)
                except ValueError:
                    continue
                message = record.get('Message', '')
                state = record.get('State', {})
                if message.startswith('StartupPhase '):
                    phases.append({'phase': state['Phase'], 'ms': state['Milliseconds']})
                elif message.startswith('PvfPreparationCache '):
                    rates = {k: state[k] for k in ('Entries','Bytes','Hits','Misses')}
        result['startup'][label] = {'freshProcessToReadySeconds': round(read_json(work / f'runtime-{label}.json')['seconds'], 3),
                                    'slowestReportedPhases': sorted(phases, key=lambda p: p['ms'], reverse=True)[:4],
                                    'preparationCache': rates}
        result['loopbackRttMs'][label] = read_json(work / f'{label}-probe.json')['loopbackRtt']
    result['town'] = read_json(work / 'town.json')
    result['character'] = read_json(work / 'character.json')
    result['resources'] = {}
    for p in [project_client / 'DNF.exe', project_client / 'Script.pvf', PROJECT / 's9a1/Server/Data/channel_info.etc',
              PROJECT / 's9a1/Server/Data/Pvf/Client100Profile.json', project_client / 'sk.dat']:
        with p.open('rb') as f:
            result['resources'][p.name] = {'bytes': p.stat().st_size, 'sha256': hashlib.file_digest(f, 'sha256').hexdigest()}
    return result


if __name__ == '__main__':
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--sdk', type=Path, help='dotnet.exe used by measure_build.py')
    parser.add_argument('--work', type=Path, default=BASELINE / '.work/measure')
    parser.add_argument('--report-only', action='store_true', help='Summarize already completed process and probe measurements')
    args = parser.parse_args()
    if not args.report_only:
        if not args.sdk:
            parser.error('--sdk is required to start the old host')
        run_hosts(args.work, args.sdk)
    report = args.work / 'results.json'
    write_json(report, summarize(args.work))
    print(report.resolve())
