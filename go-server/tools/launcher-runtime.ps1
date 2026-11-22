# Source and deployment helpers for the original Local-Run lifecycle.
function Get-S9A1GoProject([string]$Root) {
    if ([IO.File]::Exists((Join-Path $Root 'release-manifest.json'))) { return }
    $candidates = @((Join-Path $Root 'go-server'))
    if ([IO.Path]::GetFileName($Root) -ieq 's9a1') {
        $candidates += (Join-Path (Split-Path -Parent $Root) 'go-server')
    }
    foreach ($p in $candidates) {
        $module = Join-Path $p 'go.mod'
        if ([IO.File]::Exists($module) -and [IO.File]::Exists((Join-Path $p 'cmd/serve100/main.go')) -and
            [IO.File]::Exists((Join-Path $p 'tools/go.ps1')) -and [IO.File]::ReadAllText($module) -match '(?m)^module s9a1/server\r?$') {
            return [IO.Path]::GetFullPath($p)
        }
    }
}

function Get-S9A1GoSourceDigest([string]$Project) {
    $files = @((Join-Path $Project 'go.mod'), (Join-Path $Project 'go.sum'), (Join-Path $Project 'tools/go.ps1'))
    foreach ($tree in @('cmd/serve100','internal')) {
        $files += @(Get-ChildItem -LiteralPath (Join-Path $Project $tree) -Recurse -File | Where-Object {
            $_.Extension -in @('.go','.sql','.wasm','.bin') -and $_.Name -notlike '*_test.go'
        } | ForEach-Object FullName)
    }
    $rows = @($files | Sort-Object | ForEach-Object {
        $_.Substring($Project.Length+1).Replace('\','/') + ' ' + (Get-FileHash -LiteralPath $_ -Algorithm SHA256).Hash
    })
    $sha = [Security.Cryptography.SHA256]::Create()
    try { return [BitConverter]::ToString($sha.ComputeHash([Text.Encoding]::UTF8.GetBytes($rows -join "`n"))).Replace('-','') }
    finally { $sha.Dispose() }
}

function Get-S9A1GoBuildIdentity([string]$HostFile) {
    $path = $HostFile + '.build.json'
    if (![IO.File]::Exists($path) -or ![IO.File]::Exists($HostFile)) { return }
    try { $stamp = [IO.File]::ReadAllText($path) | ConvertFrom-Json } catch { return }
    if ($stamp.Schema -eq 1 -and $stamp.HostSha256 -eq (Get-FileHash -LiteralPath $HostFile -Algorithm SHA256).Hash) { return $stamp }
}

function Test-S9A1GoBuild([string]$Project,[string]$HostFile) {
    if (!$Project) { return [IO.File]::Exists($HostFile) }
    $stamp = Get-S9A1GoBuildIdentity $HostFile
    return $stamp -and $stamp.SourceDigest -eq (Get-S9A1GoSourceDigest $Project)
}

function Install-S9A1GoHost([string]$Project,[string]$HostFile) {
    $parent = [IO.Path]::GetDirectoryName($HostFile)
    [void][IO.Directory]::CreateDirectory($parent)
    $digest = Get-S9A1GoSourceDigest $Project
    $pending = Join-Path $parent ('serve100-'+[guid]::NewGuid().ToString('N')+'.pending')
    try {
        & (Join-Path $Project 'tools/go.ps1') build -trimpath -o $pending ./cmd/serve100
        if ($LASTEXITCODE -ne 0) { throw 'Go candidate build failed; the installed server was preserved.' }
        if ((Get-S9A1GoSourceDigest $Project) -ne $digest) { throw 'Go source changed during the build; the installed server was preserved.' }
        $stamp = [ordered]@{Schema=1;SourceDigest=$digest;HostSha256=(Get-FileHash -LiteralPath $pending -Algorithm SHA256).Hash}
        if ([IO.File]::Exists($HostFile)) { [IO.File]::Replace($pending,$HostFile,[NullString]::Value) }
        else { [IO.File]::Move($pending,$HostFile) }
        Write-LauncherUIJson ($HostFile+'.build.json') $stamp
    } finally { if ([IO.File]::Exists($pending)) { [IO.File]::Delete($pending) } }
}

function Invoke-S9A1StoppedSave([string]$Root,[string]$Backup,[ValidateSet('backup','verify')][string]$Action) {
    $Root = [IO.Path]::GetFullPath($Root).TrimEnd('\','/')
    $server = Join-Path $Root 'Server'
    $work = Join-Path $Root 'Work'
    $backupPath = [IO.Path]::GetFullPath($(if ([IO.Path]::IsPathRooted($Backup)) { $Backup } else { Join-Path $Root $Backup })).TrimEnd('\','/')
    if (!$backupPath.StartsWith($work+[IO.Path]::DirectorySeparatorChar,[StringComparison]::OrdinalIgnoreCase)) {
        throw 'The stopped-data backup must be a directory under this repository Work.'
    }
    Assert-LocalCandidateNoReparse $Root $backupPath
    if ($Action -eq 'verify') {
        if (![IO.Directory]::Exists($backupPath)) { throw 'The stopped-data backup directory is absent.' }
        if ([IO.Directory]::GetFileSystemEntries($backupPath).Count -ne 2) { throw 'The stopped-data PostgreSQL archive file set differs from its manifest.' }
        foreach ($file in @('manifest.json','save.dump')) {
            $path = Join-Path $backupPath $file
            Assert-LocalCandidateNoReparse $backupPath $path
            if (![IO.File]::Exists($path)) { throw 'The stopped-data PostgreSQL manifest or archive is absent.' }
        }
    }
    $hostFile = Join-Path $server 'artifacts/bin/serve100/release/serve100.exe'
    $source = Get-S9A1GoProject $Root
    if ($source) {
        $identity = Get-S9A1GoSourceDigest $source
        $tool = Join-Path $source 'tools/postgres/save_archive.py'
    } else {
        $identity = (Get-FileHash -LiteralPath $hostFile -Algorithm SHA256).Hash
        $tool = Join-Path $Root 'Tool/postgres/save_archive.py'
    }
    if (![IO.File]::Exists($tool)) { throw 'The PostgreSQL save archive tool is absent.' }
    $pyArgs = @()
    $python = $env:S9A1_PYTHON
    if (!$python) {
        $launcher = Get-Command py -CommandType Application -ErrorAction SilentlyContinue | Select-Object -First 1
        if ($launcher) { $python=$launcher.Source; $pyArgs+= '-3' }
        else { $python = (Get-Command python -CommandType Application -ErrorAction Stop | Select-Object -First 1).Source }
    }
    $pyArgs += @('-B',$tool,$Action,'--archive',$backupPath,'--server-root',$server,'--source-id',$identity)
    & $python @pyArgs
    if ($LASTEXITCODE -ne 0) { throw 'Stopped-data PostgreSQL backup verification failed; the server was not started.' }
}
