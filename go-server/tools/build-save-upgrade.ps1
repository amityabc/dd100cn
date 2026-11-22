param(
    [string]$Dotnet = $env:S9A1_DOTNET,
    [string]$LegacyOutput
)
$ErrorActionPreference = 'Stop'
$project = Split-Path -Parent $PSScriptRoot
$workspace = Split-Path -Parent $project
$work = Join-Path $project '.work'
if (-not $Dotnet) { $Dotnet = (Get-Command dotnet -ErrorAction Stop).Source }
if (-not $LegacyOutput) { $LegacyOutput = Join-Path $workspace 'baseline/.work/source/Server/artifacts/bin/Serve100.Host/release' }
$LegacyOutput = (Resolve-Path -LiteralPath $LegacyOutput).Path
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
    $artifacts = Join-Path $work 'save-upgrade-build'
    & $Dotnet build (Join-Path $PSScriptRoot 'save-upgrade/LegacyUpgrade.csproj') --nologo -c Release --artifacts-path $artifacts "-p:LegacyOutput=$LegacyOutput" -p:NuGetAudit=false -p:UseSharedCompilation=false
    if ($LASTEXITCODE -ne 0) { throw 'Build of offline SQLite upgrader failed' }
} finally {
    foreach ($entry in $saved.GetEnumerator()) {
        [Environment]::SetEnvironmentVariable($entry.Key, $entry.Value, 'Process')
    }
}
