param([string]$Dotnet = $env:S9A1_DOTNET, [string]$LegacyOutput, [string]$Output)
$ErrorActionPreference = 'Stop'
$project = Split-Path -Parent $PSScriptRoot
$workspace = Split-Path -Parent $project
if (-not $Dotnet) { $Dotnet = (Get-Command dotnet -ErrorAction Stop).Source }
if (-not $LegacyOutput) { $LegacyOutput = Join-Path $workspace 'baseline/.work/source/Server/artifacts/bin/Serve100.Host/release' }
if (-not $Output) { $Output = Join-Path $project 'testdata/routes-upstream.json' }
$run = Join-Path $project '.work/route-export'
$dir = Join-Path $run 'config'
[IO.Directory]::CreateDirectory($dir) | Out-Null
$config = Get-Content -Raw -LiteralPath (Join-Path $workspace 's9a1/Server/config/server.json') | ConvertFrom-Json
$config.Server.ChannelPort = 0
$config.Server.GamePort = 0
$config.Gameplay | Add-Member -Force -NotePropertyName ClientDirectory -NotePropertyValue (Join-Path $workspace '客户端')
$config.Gameplay.ChannelScript = Join-Path $workspace 's9a1/Server/Data/channel_info.etc'
$config.Resources.Pvf = Join-Path $workspace '客户端/Script.pvf'
$config.Resources.ClientProfile = Join-Path $workspace 's9a1/Server/Data/Pvf/Client100Profile.json'
[IO.File]::WriteAllText((Join-Path $dir 'server.json'), ($config | ConvertTo-Json -Depth 20))
$settings = @{
    DOTNET_CLI_HOME = Join-Path $project '.work/dotnet-home'
    DOTNET_GENERATE_ASPNET_CERTIFICATE = 'false'
    DOTNET_CLI_TELEMETRY_OPTOUT = '1'
    DOTNET_CLI_WORKLOAD_UPDATE_NOTIFY_DISABLE = 'true'
    NUGET_PACKAGES = Join-Path $workspace 'baseline/.work/nuget'
    TEMP = Join-Path $project '.work/tmp'
    TMP = Join-Path $project '.work/tmp'
}
$saved = @{}
try {
    foreach ($entry in $settings.GetEnumerator()) {
        $saved[$entry.Key] = [Environment]::GetEnvironmentVariable($entry.Key, 'Process')
        [Environment]::SetEnvironmentVariable($entry.Key, $entry.Value, 'Process')
    }
    [IO.Directory]::CreateDirectory($env:TEMP) | Out-Null
    $artifacts = Join-Path $project '.work/catalog-build'
    & $Dotnet build (Join-Path $PSScriptRoot 'catalog-export/CatalogExport.csproj') --nologo -c Release --artifacts-path $artifacts "-p:LegacyOutput=$LegacyOutput" -p:NuGetAudit=false -p:UseSharedCompilation=false
    if ($LASTEXITCODE) { throw 'Route inventory exporter build failed' }
    $log = Join-Path $run 'capture.log'
    & $Dotnet (Join-Path $artifacts 'bin/CatalogExport/release/CatalogExport.dll') --routes $run $LegacyOutput $Output *> $log
    if ($LASTEXITCODE) { throw "Could not capture the active registry. See $log" }
    Get-Content -LiteralPath $log -Tail 1
} finally {
    foreach ($entry in $saved.GetEnumerator()) { [Environment]::SetEnvironmentVariable($entry.Key, $entry.Value, 'Process') }
}
