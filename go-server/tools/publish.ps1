#requires -Version 7.2
[CmdletBinding()]
param([string]$Output,[string]$Catalog,[string]$Pvf,[string]$Dotnet = $env:S9A1_DOTNET,[switch]$SkipLauncherUI,[switch]$ArchiveOnly)
$ErrorActionPreference = 'Stop'
$project = Split-Path -Parent $PSScriptRoot
$workspace = Split-Path -Parent $project
$reference = Join-Path $workspace 's9a1'
$publishRoot = Join-Path $project '.work/publish'
if (!$Output) { $Output = Join-Path $publishRoot ('Serve100-Go-win-x64-' + [DateTime]::UtcNow.ToString('yyyyMMdd-HHmmss-fff')) }
elseif (![IO.Path]::IsPathRooted($Output)) { $Output = Join-Path $publishRoot $Output }
$destination = [IO.Path]::GetFullPath($Output)
if (!$destination.StartsWith($publishRoot + [IO.Path]::DirectorySeparatorChar, [StringComparison]::OrdinalIgnoreCase)) {
    throw 'Publish output must be inside go-server/.work/publish.'
}
foreach ($p in @($destination, $destination + '.zip', $destination + '.zip.sha256')) {
    if (Test-Path -LiteralPath $p) { throw ('Publish output already exists: ' + $p) }
}
for ($p = $destination; $p; $p = [IO.Path]::GetDirectoryName($p)) {
    if ((Test-Path -LiteralPath $p) -and ((Get-Item -LiteralPath $p).Attributes -band [IO.FileAttributes]::ReparsePoint)) {
        throw ('Publish output crosses a reparse point: ' + $p)
    }
}
if (!$Catalog) { $Catalog = Join-Path $project '.work/catalog/characters.json' }
if (!$Pvf) { $Pvf = Join-Path $workspace '客户端/Script.pvf' }
$Catalog = (Resolve-Path -LiteralPath $Catalog).Path
$Pvf = (Resolve-Path -LiteralPath $Pvf).Path
$gm = [IO.Path]::ChangeExtension($Catalog, '.gm.zip')
$sidecar = Join-Path ([IO.Path]::GetDirectoryName($Pvf)) 'sk.dat'
$profile = Join-Path $reference 'Server/Data/Pvf/Client100Profile.json'
$catalogStream = [IO.File]::OpenRead($Catalog)
try {
    $resources = [System.Text.Json.JsonDocument]::Parse($catalogStream)
    try {
        $catalogRoot = $resources.RootElement
        if ($catalogRoot.GetProperty('Schema').GetInt32() -ne 4 -or $catalogRoot.GetProperty('Characters').GetArrayLength() -eq 0) {
            throw 'The Go resource catalog is missing or incompatible.'
        }
        $identities = @(@($Pvf,$catalogRoot.GetProperty('PvfSha256').GetString()),
            @($sidecar,$catalogRoot.GetProperty('SidecarSha256').GetString()),
            @($profile,$catalogRoot.GetProperty('ProfileSha256').GetString()))
    } finally { $resources.Dispose() }
    if ($ArchiveOnly) {
        $catalogStream.Position = 0
        $catalogSHA256 = [Convert]::ToHexString([Security.Cryptography.SHA256]::HashData($catalogStream))
    }
} finally { $catalogStream.Dispose() }
foreach ($row in $identities) {
    if ((Get-FileHash -LiteralPath $row[0] -Algorithm SHA256).Hash -ne $row[1]) { throw ('Catalog identity mismatch: ' + $row[0]) }
}
if (![IO.File]::Exists($gm)) { throw ('Missing catalog GM archive: ' + $gm) }

$staticFiles = [ordered]@{
    'Server/Data/Pvf/Script.pvf' = $Pvf
    'Server/Data/Pvf/sk.dat' = $sidecar
    'Server/Data/Pvf/Client100Profile.json' = $profile
    'Server/Data/Pvf/characters.json' = $Catalog
    'Server/Data/Pvf/characters.gm.zip' = $gm
}
$releaseTree = $destination
$archivePath = $destination + '.zip'
$zipOutput = $archivePath
if ($ArchiveOnly) {
    $suffix = [Guid]::NewGuid().ToString('N')
    $releaseTree = $destination + '.stage-' + $suffix
    $zipOutput = $archivePath + '.stage-' + $suffix
}

function Copy-ReleaseFile([string]$Source, [string]$Relative) {
    $target = Join-Path $releaseTree $Relative
    [void][IO.Directory]::CreateDirectory([IO.Path]::GetDirectoryName($target))
    [IO.File]::Copy($Source, $target)
}

function Read-ReleaseFile([string]$Source, [string]$Relative) {
    $stream = [IO.File]::OpenRead($Source)
    try {
        [pscustomobject]@{
            Source = $Source
            Path = $Relative
            Bytes = $stream.Length
            SHA256 = [Convert]::ToHexString([Security.Cryptography.SHA256]::HashData($stream))
        }
    } finally { $stream.Dispose() }
}

function Write-ReleaseArchive([string]$Path, [string]$Prefix, [object[]]$Rows) {
    $stream = [IO.File]::Open($Path,[IO.FileMode]::CreateNew,[IO.FileAccess]::Write,[IO.FileShare]::None)
    try {
        $zip = [IO.Compression.ZipArchive]::new($stream,[IO.Compression.ZipArchiveMode]::Create,$true)
        try {
            foreach ($row in $Rows) {
                $sourceStream = [IO.File]::OpenRead($row.Source)
                try {
                    if ($sourceStream.Length -ne $row.Bytes) { throw ('Publish input length changed: ' + $row.Path) }
                    $entry = $zip.CreateEntry($Prefix + $row.Path,[IO.Compression.CompressionLevel]::Optimal)
                    $entryStream = $entry.Open()
                    try { $sourceStream.CopyTo($entryStream) } finally { $entryStream.Dispose() }
                } finally { $sourceStream.Dispose() }
            }
        } finally { $zip.Dispose() }
    } finally { $stream.Dispose() }
}

function Confirm-ReleaseArchive([string]$Path, [string]$Prefix, [object[]]$Rows) {
    $zip = [IO.Compression.ZipFile]::OpenRead($Path)
    try {
        $entries = [Collections.Generic.HashSet[string]]::new([StringComparer]::Ordinal)
        foreach ($entry in $zip.Entries) {
            if ($entry.Name -and !$entries.Add($entry.FullName)) { throw ('Duplicate ZIP entry: ' + $entry.FullName) }
        }
        if ($entries.Count -ne $Rows.Count) { throw 'ZIP file count differs from the manifest.' }
        foreach ($row in $Rows) {
            $entry = $zip.GetEntry($Prefix + $row.Path)
            if (!$entry -or $entry.Length -ne $row.Bytes) { throw ('ZIP entry mismatch: ' + $row.Path) }
            $stream = $entry.Open()
            try {
                $hash = [Convert]::ToHexString([Security.Cryptography.SHA256]::HashData($stream))
                if ($hash -ne $row.SHA256) { throw ('ZIP checksum mismatch: ' + $row.Path) }
            } finally { $stream.Dispose() }
        }
    } finally { $zip.Dispose() }
}
function Apply-ReleasePatch([string]$PatchRoot,[string[]]$Files) {
    $patch = Join-Path $PSScriptRoot 'legacy-launcher.patch'
    $patchRoot = [IO.Path]::GetRelativePath($workspace,$PatchRoot).Replace('\','/')
    $patchArgs = @('-C',$workspace,'apply','--ignore-space-change',('--directory='+$patchRoot))
    foreach ($file in $Files) { $patchArgs += ('--include='+$patchRoot+'/'+$file) }
    & git @patchArgs --reverse --check $patch 2>$null
    if ($LASTEXITCODE -eq 0) { return }
    & git @patchArgs --check $patch
    if ($LASTEXITCODE -ne 0) { throw 'The original tools do not match the pinned compatibility patch.' }
    & git @patchArgs $patch
    if ($LASTEXITCODE -ne 0) { throw 'Could not apply the compatibility patch to the release.' }
}

$externalRows = @()
if ($ArchiveOnly) {
    $externalRows = @($staticFiles.GetEnumerator() | ForEach-Object { Read-ReleaseFile $_.Value $_.Key })
    $expected = @{$Catalog = $catalogSHA256}
    foreach ($row in $identities) { $expected[$row[0]] = $row[1] }
    foreach ($row in $externalRows) {
        if ($expected.ContainsKey($row.Source) -and $row.SHA256 -ne $expected[$row.Source]) {
            throw ('Publish source changed during preflight: ' + $row.Source)
        }
    }
}

try {
    $shellFiles = @(
        'Tool/Local-Run.ps1','Tool/Start-Source.ps1','Tool/Launcher-UI.ps1','Tool/Build-LocalCandidate.ps1',
        'Tool/Read-MiniDump.ps1','Tool/Bounded-Logs.ps1','Tool/Capture-Source.ps1','Tool/Capture-Frames.ps1','Tool/Capture-Console.ps1',
        'Tool/Client-Diagnostics.ps1','Tool/Client-Selection.ps1','Tool/Runtime-Config.ps1','Tool/New-DiagnosticBundle.ps1',
        'ClientTools/Resource-Launcher.ps1','ClientTools/Resource-Paths.ps1','ClientTools/QuickTest-Plugin.ps1',
        'Server/START_SERVER.bat','Server/STOP_SERVER.bat','Server/config/server.json','Server/config/local.example.json',
        'Server/Data/channel_info.etc','Server/src/Serve100.Infrastructure/Diagnostics/BoundedLogFile.cs'
    )
    foreach ($file in $shellFiles) { Copy-ReleaseFile (Join-Path $reference $file) $file }
    Apply-ReleasePatch $releaseTree @('Tool/Local-Run.ps1','Tool/Start-Source.ps1','Tool/Launcher-UI.ps1','ClientTools/Resource-Launcher.ps1','Server/START_SERVER.bat','Server/STOP_SERVER.bat')
    Copy-ReleaseFile (Join-Path $PSScriptRoot 'launcher-runtime.ps1') 'Tool/Go-Runtime.ps1'
    Copy-ReleaseFile (Join-Path $PSScriptRoot 'postgres/save_archive.py') 'Tool/postgres/save_archive.py'

    $builds = [ordered]@{
        'Server/artifacts/bin/serve100/release/serve100.exe' = './cmd/serve100'
        'Tool/bin/launcher.exe' = './cmd/launcher'
        'Tool/bin/multiplayer-admin.exe' = './cmd/multiplayer-admin'
        'Tool/bin/import-save.exe' = './cmd/import-save'
        'Tool/bin/prepare-catalog.exe' = './cmd/prepare-catalog'
    }
    $buildEnv = @{GOOS='windows';GOARCH='amd64';CGO_ENABLED='0'}
    $saved = @{}
    try {
        foreach ($row in $buildEnv.GetEnumerator()) {
            $saved[$row.Key] = [Environment]::GetEnvironmentVariable($row.Key, 'Process')
            [Environment]::SetEnvironmentVariable($row.Key,$row.Value,'Process')
        }
        foreach ($build in $builds.GetEnumerator()) {
            $binary = Join-Path $releaseTree $build.Key
            [void][IO.Directory]::CreateDirectory([IO.Path]::GetDirectoryName($binary))
            & (Join-Path $PSScriptRoot 'go.ps1') build -trimpath -o $binary $build.Value
            if ($LASTEXITCODE -ne 0) { throw ('Go build failed: ' + $build.Value) }
        }
    } finally {
        foreach ($row in $saved.GetEnumerator()) { [Environment]::SetEnvironmentVariable($row.Key, $row.Value, 'Process') }
    }

    $nativeHost=Join-Path $releaseTree 'Server/artifacts/bin/serve100/release/serve100.exe'
    & $nativeHost --root (Join-Path $releaseTree 'Server') --create-release-database
    if ($LASTEXITCODE -ne 0) { throw 'Could not generate the offline PostgreSQL release seed.' }

    if (!$SkipLauncherUI) {
        if (!$Dotnet) { $Dotnet = (Get-Command dotnet -CommandType Application -ErrorAction Stop).Source }
        $staging = [IO.Path]::GetFullPath((Join-Path $releaseTree '.build'))
        $launcherUiSource = Join-Path $staging 'ClientTools/LauncherUI'
        [void][IO.Directory]::CreateDirectory($launcherUiSource)
        $uiReference = Join-Path $reference 'ClientTools/LauncherUI'
        foreach ($f in Get-ChildItem -LiteralPath $uiReference -File) {
            [IO.File]::Copy($f.FullName,(Join-Path $launcherUiSource $f.Name))
        }
        Copy-Item -LiteralPath (Join-Path $uiReference 'wwwroot') -Destination $launcherUiSource -Recurse
        Apply-ReleasePatch $staging @('ClientTools/LauncherUI/*')
        $managedWork = Join-Path $project '.work/dotnet-launcher'
        $managedEnv = @{
            DOTNET_CLI_HOME = Join-Path $managedWork 'home'
            NUGET_PACKAGES = Join-Path $managedWork 'packages'
            NUGET_HTTP_CACHE_PATH = Join-Path $managedWork 'http'
            NUGET_PLUGINS_CACHE_PATH = Join-Path $managedWork 'plugins'
            DOTNET_CLI_TELEMETRY_OPTOUT = '1'
            TMP = Join-Path $managedWork 'tmp'
            TEMP = Join-Path $managedWork 'tmp'
        }
        $saved = @{}
        try {
            foreach ($row in $managedEnv.GetEnumerator()) {
                $saved[$row.Key] = [Environment]::GetEnvironmentVariable($row.Key,'Process')
                [Environment]::SetEnvironmentVariable($row.Key,$row.Value,'Process')
            }
            [void][IO.Directory]::CreateDirectory($managedEnv.TMP)
            $uiBuild = @('publish',(Join-Path $launcherUiSource 'LauncherUI.csproj'),'-c','Release','-r','win-x64',
                '--self-contained','true','--nologo',
                '--artifacts-path',(Join-Path $managedWork 'artifacts'),'-o',(Join-Path $releaseTree 'LauncherUI'))
            & $Dotnet @uiBuild
            if ($LASTEXITCODE -ne 0) { throw 'Could not build LauncherUI.' }
        } finally {
            foreach ($row in $saved.GetEnumerator()) { [Environment]::SetEnvironmentVariable($row.Key,$row.Value,'Process') }
        }
        [IO.File]::WriteAllLines((Join-Path $releaseTree 'START_LAUNCHER.bat'),@('@echo off','start "" "%~dp0LauncherUI\S9A1LauncherUI.exe" --root "%~dp0."'),[Text.ASCIIEncoding]::new())
        if (![string]::Equals([IO.Path]::GetDirectoryName($staging),$releaseTree,[StringComparison]::OrdinalIgnoreCase)) {
            throw 'Invalid LauncherUI staging directory.'
        }
        Remove-Item -LiteralPath $staging -Force -Recurse
    }

    if (!$ArchiveOnly) {
        foreach ($file in $staticFiles.GetEnumerator()) { Copy-ReleaseFile $file.Value $file.Key }
    }
    Copy-ReleaseFile (Join-Path $PSScriptRoot 'publish-readme.md') 'README.md'
    Copy-Item -LiteralPath (Join-Path $reference 'Licenses') -Destination $releaseTree -Recurse

    $localRows = @(Get-ChildItem -LiteralPath $releaseTree -Recurse -File | ForEach-Object {
        $relative = [IO.Path]::GetRelativePath($releaseTree,$_.FullName).Replace('\','/')
        Read-ReleaseFile $_.FullName $relative
    })
    $releaseRows = @(($localRows + $externalRows) | Sort-Object Path)
    $files = @($releaseRows | ForEach-Object { [ordered]@{Path=$_.Path;Bytes=$_.Bytes;SHA256=$_.SHA256} })
    $manifest = [ordered]@{Schema=1;Runtime='Go';Platform='win-x64';BuiltAtUtc=[DateTime]::UtcNow.ToString('o');Files=$files}
    $manifestPath = Join-Path $releaseTree 'release-manifest.json'
    [IO.File]::WriteAllText($manifestPath,($manifest | ConvertTo-Json -Depth 5))
    $releaseRows += Read-ReleaseFile $manifestPath 'release-manifest.json'

    Add-Type -AssemblyName System.IO.Compression.FileSystem
    $prefix = [IO.Path]::GetFileName($destination) + '/'
    if ($ArchiveOnly) {
        Write-ReleaseArchive $zipOutput $prefix $releaseRows
    } else {
        [IO.Compression.ZipFile]::CreateFromDirectory($releaseTree,$zipOutput,[IO.Compression.CompressionLevel]::Optimal,$true)
    }
    Confirm-ReleaseArchive $zipOutput $prefix $releaseRows
    if ($ArchiveOnly) {
        foreach ($row in $externalRows) {
            $after = Read-ReleaseFile $row.Source $row.Path
            if ($after.Bytes -ne $row.Bytes -or $after.SHA256 -ne $row.SHA256) {
                throw ('Publish source changed while building ZIP: ' + $row.Source)
            }
        }
    }
    $checksum = (Get-FileHash -LiteralPath $zipOutput -Algorithm SHA256).Hash
    if ($ArchiveOnly) { [IO.File]::Move($zipOutput,$archivePath) }
    [IO.File]::WriteAllLines(($archivePath + '.sha256'),@($checksum + '  ' + [IO.Path]::GetFileName($archivePath)))
    $publishedDirectory = if (!$ArchiveOnly) { $destination }
    [pscustomobject]@{Outcome='GoReleasePrepared';Directory=$publishedDirectory;Archive=$archivePath;SHA256=$checksum} | ConvertTo-Json -Compress
} finally {
    if ($ArchiveOnly) {
        [IO.File]::Delete($zipOutput)
        if ([IO.Directory]::Exists($releaseTree) -and
            [string]::Equals([IO.Path]::GetDirectoryName($releaseTree),[IO.Path]::GetDirectoryName($destination),[StringComparison]::OrdinalIgnoreCase)) {
            [IO.Directory]::Delete($releaseTree,$true)
        }
    }
}
