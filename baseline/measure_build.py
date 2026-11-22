"""Measure the original Release build and the Host-only development build on Windows."""

import argparse
import json
import os
import shutil
import subprocess
import time
from pathlib import Path

BASELINE = Path(__file__).resolve().parent
PROJECT = BASELINE.parent


def write_json(path, value):
    path.write_text(json.dumps(value, ensure_ascii=False, indent=2) + '\n', encoding='utf-8')


def benchmark_paths(work, sdk):
    work = work.resolve()
    if not work.is_relative_to(BASELINE / '.work'):
        raise ValueError('Choose a work directory under baseline/.work')
    sdk = sdk.resolve(strict=True)
    for folder in ('cli', 'nuget', 'http-cache', 'temp', 'logs'):
        (work / folder).mkdir(parents=True, exist_ok=True)
    env = {**os.environ, 'DOTNET_ROOT': str(sdk.parent), 'DOTNET_CLI_HOME': str(work / 'cli'),
           'DOTNET_CLI_TELEMETRY_OPTOUT': '1', 'DOTNET_NOLOGO': '1', 'DOTNET_GENERATE_ASPNET_CERTIFICATE': 'false',
           'DOTNET_SKIP_FIRST_TIME_EXPERIENCE': '1', 'NUGET_PACKAGES': str(work / 'nuget'),
           'NUGET_HTTP_CACHE_PATH': str(work / 'http-cache'), 'TEMP': str(work / 'temp'), 'TMP': str(work / 'temp'),
           'PATH': str(sdk.parent) + os.pathsep + os.environ['PATH']}
    return work, sdk, env


def measure_build(work, sdk, resume_cold=False):
    work, sdk, env = benchmark_paths(work, sdk)
    repo = work / 'source'
    server = repo / 'Server'
    shell = Path(os.environ['SystemRoot']) / 'System32/WindowsPowerShell/v1.0/powershell.exe'
    formal = [str(shell), '-NoProfile', '-ExecutionPolicy', 'Bypass', '-File', str(server / 'BUILD.ps1'), '-Root', str(server)]
    def timed(name, command):
        output = work / 'logs' / (name + '.log')
        with output.open('wb') as log:
            started = time.perf_counter()
            result = subprocess.run(command, cwd=server, env=env, stdout=log, stderr=subprocess.STDOUT)
            elapsed = round(time.perf_counter() - started, 3)
        if result.returncode:
            raise RuntimeError(f'{name} exited {result.returncode}. See {output}')
        return elapsed

    cold_result = work / 'attempt-cold.json'
    if resume_cold:
        cold = json.loads(cold_result.read_text(encoding='utf-8-sig'))
        if cold['exit'] or not (server / 'artifacts/build/identity-release.json').is_file():
            raise ValueError('A completed formal build and its identity are required to resume')
    else:
        if repo.exists() or cold_result.exists() or any((work / 'nuget').iterdir()):
            raise ValueError('A cold build requires a fresh work directory')
        shutil.copytree(PROJECT / 's9a1', repo, ignore=shutil.ignore_patterns('.git', 'artifacts', 'runtime', 'Work', 'dist', 'bin', 'obj', '*.pvf', '*.db', '*.db-*'))
        for args in (['init', '--quiet'], ['config', 'core.autocrlf', 'false'], ['add', '--all'],
                     ['-c', 'user.name=Baseline', '-c', 'user.email=baseline@localhost', 'commit', '--quiet', '-m', 'Isolated source baseline']):
            subprocess.run(['git', '-C', str(repo), *args], env=env, check=True)
        cold = {'seconds': timed('formal-cold', formal), 'exit': 0}
        write_json(cold_result, cold)
    reuse_seconds = timed('formal-reuse', formal)
    # Separate MSBuild outputs let the verified Host continue serving during a development build.
    dev = [str(sdk), 'build', 'src/Serve100.Host/Serve100.Host.csproj', '-c', 'Release', '--nologo',
           '-p:ArtifactsPath=' + str(work / 'dev-artifacts')]
    dev_initial = timed('dev-initial', dev)
    dev_noop = timed('dev-noop', dev)
    target = server / 'src/Serve100.Game/LocalEntry.cs'
    original = target.read_bytes()
    marker = b'Missing local character stat: '
    if original.count(marker) != 1:
        raise ValueError('Expected source diagnostic changed, inspect the selected baseline')
    try:
        # An implementation-only diagnostic change compiles real Game IL while preserving its public API.
        target.write_bytes(original.replace(marker, b'Missing benchmark character stat: '))
        dev_single = timed('dev-one-game-file', dev)
    finally:
        target.write_bytes(original)
    if subprocess.run(['git', '-C', str(repo), 'diff', '--quiet', '--', 'Server/src/Serve100.Game/LocalEntry.cs'], env=env).returncode:
        raise RuntimeError('Isolated source edit was not restored')
    identity = json.loads((server / 'artifacts/build/identity-release.json').read_text(encoding='utf-8-sig'))
    result = {'sdkVersion': subprocess.check_output([str(sdk), '--version'], cwd=server, env=env, text=True).strip(),
              'officialFullSeconds': cold['seconds'], 'officialNoChangeSeconds': reuse_seconds,
              'devInitialHostSeconds': dev_initial, 'devNoChangeSeconds': dev_noop, 'devOneGameSourceSeconds': dev_single,
              'sourceDigest': identity['SourceDigest'], 'isolatedSourceTree': identity['SourceTree']}
    write_json(work / 'build-summary.json', result)
    return result


if __name__ == '__main__':
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--sdk', required=True, type=Path, help='dotnet.exe from a compatible .NET 10 SDK')
    parser.add_argument('--work', type=Path, default=BASELINE / '.work/measure')
    parser.add_argument('--resume-cold', action='store_true', help='Continue after completed formal build in the same work directory')
    args = parser.parse_args()
    print(json.dumps(measure_build(args.work, args.sdk, args.resume_cold), indent=2))
