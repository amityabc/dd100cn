$ErrorActionPreference = 'Stop'
$project = Split-Path -Parent $PSScriptRoot
$tool = $env:S9A1_GO
if (-not $tool) {
    $local = Join-Path $project '.tools/go/bin/go.exe'
    $command = Get-Command go -CommandType Application -ErrorAction SilentlyContinue | Select-Object -First 1
    if (Test-Path -LiteralPath $local -PathType Leaf) { $tool = $local }
    elseif ($command) { $tool = $command.Source }
    elseif ($env:LOCALAPPDATA) {
        $installed = Join-Path $env:LOCALAPPDATA 'Codex/bns-toolchain'
        if (Test-Path -LiteralPath $installed -PathType Container) {
            $tool = Get-ChildItem -LiteralPath $installed -Directory -Filter 'go1.*' |
                Sort-Object { [version]$_.Name.Substring(2) } -Descending |
                ForEach-Object { Join-Path $_.FullName 'go/bin/go.exe' } |
                Where-Object { Test-Path -LiteralPath $_ -PathType Leaf } | Select-Object -First 1
        }
    }
}
if (-not $tool) { throw 'Go 1.26 or later is required. Set S9A1_GO to the Go executable.' }

$work = Join-Path $project '.work'
$saved = @{}
$settings = @{
    GOPATH = Join-Path $work 'gopath'
    GOMODCACHE = Join-Path $work 'modules'
    GOCACHE = Join-Path $work 'build-cache'
    GOTMPDIR = Join-Path $work 'tmp'
    TMP = Join-Path $work 'tmp'
    TEMP = Join-Path $work 'tmp'
    GOBIN = Join-Path $project 'bin'
}
foreach ($dir in $settings.Values) { [IO.Directory]::CreateDirectory($dir) | Out-Null }
$settings['GOTOOLCHAIN'] = 'local'
$settings['GOTELEMETRY'] = 'off'
$shared = & $tool env GOMODCACHE
if ($LASTEXITCODE -ne 0) { exit $LASTEXITCODE }
if ($shared -and $shared -ne $settings.GOMODCACHE) {
    $archive = Join-Path $shared 'cache/download'
    if (Test-Path -LiteralPath $archive -PathType Container) {
        $fallback = & $tool env GOPROXY
        if ($LASTEXITCODE -ne 0) { exit $LASTEXITCODE }
        $settings['GOPROXY'] = ([uri]([IO.Path]::GetFullPath($archive))).AbsoluteUri + ',' + $fallback
    }
}
Push-Location -LiteralPath $project
try {
    foreach ($entry in $settings.GetEnumerator()) {
        $saved[$entry.Key] = [Environment]::GetEnvironmentVariable($entry.Key, 'Process')
        [Environment]::SetEnvironmentVariable($entry.Key, $entry.Value, 'Process')
    }
    & $tool @args
    $code = $LASTEXITCODE
} finally {
    foreach ($entry in $saved.GetEnumerator()) { [Environment]::SetEnvironmentVariable($entry.Key, $entry.Value, 'Process') }
    Pop-Location
}
exit $code
