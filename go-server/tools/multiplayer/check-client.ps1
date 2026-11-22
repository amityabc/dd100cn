[CmdletBinding()]
param([Parameter(Mandatory = $true)][string]$ClientRoot)

$ErrorActionPreference = 'Stop'
$project = (Resolve-Path -LiteralPath (Join-Path $PSScriptRoot '../..')).Path
$client = (Resolve-Path -LiteralPath $ClientRoot).Path
$scratch = Join-Path $project ('.work/multiplayer-check-' + [Guid]::NewGuid().ToString('N'))
$work = [IO.Path]::GetFullPath((Join-Path $project '.work')).TrimEnd('\') + '\'
$target = [IO.Path]::GetFullPath($scratch)
if (!$target.StartsWith($work, [StringComparison]::OrdinalIgnoreCase)) { throw 'Invalid temporary client directory' }
$files = @('Start-Client.ps1','Resource-Launcher.ps1','client-launch.local.json','DNF.exe','DNFBase.dll','TenProxy.dll',
    'Script.pvf','sk.dat','dstr.dat','D3DX9_43.dll','fmodex64.dll','LocalTools/DNF100Launcher.exe')
$tools = 'LocalTools/ResourceCompatibility/sound-v1/'
$files += @('DNF100ResourceLauncher.exe','DNF100ResourceLauncher.dll','DNF100ResourceLauncher.deps.json',
    'DNF100ResourceLauncher.runtimeconfig.json') | ForEach-Object { $tools + $_ }
try {
    foreach ($file in $files) {
        $original = (Resolve-Path -LiteralPath (Join-Path $client $file)).Path
        $destination = Join-Path $scratch $file
        [void][IO.Directory]::CreateDirectory([IO.Path]::GetDirectoryName($destination))
        [void](New-Item -ItemType HardLink -Path $destination -Target $original)
    }
    $shell = Join-Path $env:SystemRoot 'System32/WindowsPowerShell/v1.0/powershell.exe'
    foreach ($mode in @('-CheckOnly','-PrepareOnly')) {
        & $shell -NoProfile -ExecutionPolicy Bypass -File (Join-Path $scratch 'Start-Client.ps1') -ClientRoot $scratch $mode
        if ($LASTEXITCODE -ne 0) { throw "Client preparation failed ($mode): $LASTEXITCODE" }
    }
    $bridge = Get-Content -Raw -LiteralPath (Join-Path $scratch 'client-start/runtime/config/instance.json') | ConvertFrom-Json
    if ($bridge.server.channelListen -ne '127.0.0.1:7001' -or $bridge.server.advertiseIp -ne '127.0.0.1') { throw 'Unexpected original endpoint' }
    Write-Output 'Original matching standalone scripts prepared the client bridge. No game process was launched.'
} finally {
    if (Test-Path -LiteralPath $target) {
        $resolved = (Resolve-Path -LiteralPath $target).Path
        if (!$resolved.StartsWith($work, [StringComparison]::OrdinalIgnoreCase)) { throw 'Unexpected cleanup path' }
        Remove-Item -LiteralPath $resolved -Recurse -Force
    }
}
