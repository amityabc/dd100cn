param([string]$DsnFile, [string]$Catalog, [string]$GatewayConfig, [switch]$Stop, [switch]$BuildOnly)
$ErrorActionPreference = 'Stop'
$project = Split-Path -Parent $PSScriptRoot
$workspace = Split-Path -Parent $project
$instance = Join-Path $project '.work/instance'
$bin = Join-Path $project 'bin/serve100.exe'
if ($Stop) { & $bin --root $instance --request-stop; exit $LASTEXITCODE }
& (Join-Path $PSScriptRoot 'go.ps1') build -o $bin ./cmd/serve100
if ($LASTEXITCODE -ne 0 -or $BuildOnly) { exit $LASTEXITCODE }
$dir = Join-Path $instance 'config'
[IO.Directory]::CreateDirectory($dir) | Out-Null
$baseConfig = Join-Path $dir 'server.json'
if (-not (Test-Path -LiteralPath $baseConfig)) {
    [IO.File]::Copy((Join-Path $workspace 's9a1/Server/config/server.json'), $baseConfig)
}
if (-not $Catalog) { $Catalog = Join-Path $project '.work/catalog/characters.json' }
$settings = @{
    S9A1_CREATION_CATALOG = $Catalog
    SERVE100_NEXT_SERVER__CHANNELPORT = '0'
    SERVE100_NEXT_SERVER__GAMEPORT = '0'
    SERVE100_NEXT_GAMEPLAY__CHANNELSCRIPT = Join-Path $workspace 's9a1/Server/Data/channel_info.etc'
    SERVE100_NEXT_RESOURCES__PVF = Join-Path $workspace '客户端/Script.pvf'
    SERVE100_NEXT_RESOURCES__CLIENTPROFILE = Join-Path $workspace 's9a1/Server/Data/Pvf/Client100Profile.json'
}
if ($DsnFile) { $settings['S9A1_POSTGRES_DSN'] = [IO.File]::ReadAllText((Resolve-Path -LiteralPath $DsnFile).Path).Trim() }
if ($GatewayConfig) { $settings['S9A1_GATEWAY_CONFIG'] = (Resolve-Path -LiteralPath $GatewayConfig).Path }
$saved = @{}
try {
    foreach ($entry in $settings.GetEnumerator()) {
        $saved[$entry.Key] = [Environment]::GetEnvironmentVariable($entry.Key, 'Process')
        [Environment]::SetEnvironmentVariable($entry.Key, $entry.Value, 'Process')
    }
    & $bin --root $instance
    $code = $LASTEXITCODE
} finally {
    foreach ($entry in $saved.GetEnumerator()) { [Environment]::SetEnvironmentVariable($entry.Key, $entry.Value, 'Process') }
}
exit $code
