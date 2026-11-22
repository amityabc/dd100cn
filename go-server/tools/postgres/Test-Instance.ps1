param(
    [ValidateSet('Start','Stop')][string]$Action = 'Start',
    [string]$BinDirectory = $env:S9A1_PG_BIN,
    [ValidateRange(1024,65535)][int]$Port = 56439
)

$ErrorActionPreference = 'Stop'
if (!$BinDirectory) { $BinDirectory = Split-Path -Parent (Get-Command initdb -ErrorAction Stop).Source }
$initdb = Join-Path $BinDirectory 'initdb.exe'
$control = Join-Path $BinDirectory 'pg_ctl.exe'
$workspace = (Resolve-Path -LiteralPath (Join-Path $PSScriptRoot '..\..\..')).Path
$local = Join-Path $workspace '.work\postgres-storage'
$data = Join-Path $local 'data'
$dsnFile = Join-Path $local 'test-dsn.txt'

if ($Action -eq 'Stop') {
    if (Test-Path -LiteralPath (Join-Path $data 'postmaster.pid')) {
        & $control -D $data -m fast -w stop
        if ($LASTEXITCODE) { throw 'Project PostgreSQL did not stop.' }
    }
    return
}

New-Item -ItemType Directory -Force -Path $local | Out-Null
if (!(Test-Path -LiteralPath (Join-Path $data 'PG_VERSION'))) {
    $password = [Guid]::NewGuid().ToString('N')
    $pwFile = Join-Path $local 'init-password.txt'
    try {
        [IO.File]::WriteAllText($pwFile, $password + "`n", [Text.Encoding]::ASCII)
        & $initdb -D $data -U s9a1_test -A scram-sha-256 --locale=C -E UTF8 --pwfile=$pwFile
        if ($LASTEXITCODE) { throw 'Project PostgreSQL initialization failed.' }
        [IO.File]::WriteAllText($dsnFile, "postgresql://s9a1_test:${password}@127.0.0.1:$Port/postgres?sslmode=disable", [Text.Encoding]::ASCII)
    } finally {
        if (Test-Path -LiteralPath $pwFile) { Remove-Item -LiteralPath $pwFile }
    }
} elseif (!(Test-Path -LiteralPath $dsnFile)) { throw 'Project instance already exists without its connection file.' }
$uri = [Uri]([IO.File]::ReadAllText($dsnFile))
if ($uri.Port -ne $Port) { throw "Existing project instance uses port $($uri.Port)." }
& $control -D $data status *> $null
if ($LASTEXITCODE -ne 0) {
    & $control -D $data -l (Join-Path $local 'postgres.log') -o "-h 127.0.0.1 -p $Port" -w start
    if ($LASTEXITCODE) { throw 'Project PostgreSQL did not start.' }
}
Write-Output "Project PostgreSQL ready on 127.0.0.1:$Port. Connection file: $dsnFile"
