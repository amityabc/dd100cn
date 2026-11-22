param(
    [string]$Dotnet = $env:S9A1_DOTNET,
    [string]$LegacyOutput,
    [string]$Pvf,
    [string]$Profile,
    [string]$Output,
    [switch]$GmOnly
)
$ErrorActionPreference = 'Stop'
$project = Split-Path -Parent $PSScriptRoot
$workspace = Split-Path -Parent $project
$work = Join-Path $project '.work'
if (-not $Dotnet) { $Dotnet = (Get-Command dotnet -ErrorAction Stop).Source }
if (-not $LegacyOutput) { $LegacyOutput = Join-Path $workspace 'baseline/.work/source/Server/artifacts/bin/Serve100.Host/release' }
if (-not $Pvf) { $Pvf = Join-Path $workspace '客户端/Script.pvf' }
if (-not $Profile) { $Profile = Join-Path $workspace 's9a1/Server/Data/Pvf/Client100Profile.json' }
if (-not $Output) {
    $Output = Join-Path $work $(if ($GmOnly) { 'catalog/characters.gm.zip' } else { 'catalog/characters.json' })
}
$artifacts = Join-Path $work 'catalog-build'
$settings = @{
    DOTNET_CLI_HOME = Join-Path $work 'dotnet-home'
    DOTNET_CLI_TELEMETRY_OPTOUT = '1'
    DOTNET_GENERATE_ASPNET_CERTIFICATE = 'false'
    DOTNET_SKIP_FIRST_TIME_EXPERIENCE = '1'
    DOTNET_CLI_WORKLOAD_UPDATE_NOTIFY_DISABLE = 'true'
    NUGET_PACKAGES = Join-Path $workspace 'baseline/.work/nuget'
    TMP = Join-Path $work 'tmp'
    TEMP = Join-Path $work 'tmp'
}
$saved = @{}
try {
    foreach ($entry in $settings.GetEnumerator()) {
        $saved[$entry.Key] = [Environment]::GetEnvironmentVariable($entry.Key, 'Process')
        [Environment]::SetEnvironmentVariable($entry.Key, $entry.Value, 'Process')
    }
    [IO.Directory]::CreateDirectory($env:TEMP) | Out-Null
    & $Dotnet build (Join-Path $PSScriptRoot 'catalog-export/CatalogExport.csproj') --nologo -c Release --artifacts-path $artifacts "-p:LegacyOutput=$LegacyOutput" -p:NuGetAudit=false -p:UseSharedCompilation=false
    if ($LASTEXITCODE -ne 0) { throw 'Build of offline catalog exporter failed' }
    $arguments = @($Pvf, $Profile, $Output)
    if ($GmOnly) { $arguments = @('--gm') + $arguments }
    & $Dotnet (Join-Path $artifacts 'bin/CatalogExport/release/CatalogExport.dll') @arguments
    if ($LASTEXITCODE -ne 0) { throw 'PVF catalog export failed' }
} finally {
    foreach ($entry in $saved.GetEnumerator()) { [Environment]::SetEnvironmentVariable($entry.Key, $entry.Value, 'Process') }
}
