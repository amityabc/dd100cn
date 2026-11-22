param([Parameter(Mandatory)][string]$ProtocolAssembly)
$ErrorActionPreference = 'Stop'
[void][Reflection.Assembly]::LoadFrom((Resolve-Path -LiteralPath $ProtocolAssembly).Path)
$stats = [byte[]]::new(92)
$stats[84] = 100
$weapon = [Serve100.Protocol.LocalWeaponView]::new(701234, 87654, 98)
$char = [Serve100.Protocol.LocalCharacterView]::new(7,'测试',12,57,2,1234567,$stats,$true,38,1,450,234,5,3,1234,8,156,$weapon)
$options = [byte[]]::new(372)
$options[0] = 255
$options[371] = 128
$hotkeys = [byte[]](0,128,255,1,0)
$characters = [Serve100.Protocol.LocalCharacterView[]]@($char)
$vectors = [ordered]@{
    origin = 'Standalone original C# Serve100.Protocol, deterministic synthetic character and account data'
    assemblySha256 = (Get-FileHash -LiteralPath $ProtocolAssembly).Hash
    roster = [Convert]::ToHexString([Serve100.Protocol.LocalEntryPackets]::Roster($characters,32))
    created = [Convert]::ToHexString([Serve100.Protocol.LocalEntryPackets]::CreateAccepted($char))
    selectNative = [Convert]::ToHexString([Serve100.Protocol.LocalEntryPackets]::Select($char,10,22,0,700,$true))
    selectLegacy = [Convert]::ToHexString([Serve100.Protocol.LocalEntryPackets]::Select($char,38,0,0,0,$false))
    mode0 = [Convert]::ToHexString([Serve100.Protocol.LocalEntryPackets]::Mode0($char,$false))
    mode0Native140 = [Convert]::ToHexString([Serve100.Protocol.LocalEntryPackets]::Mode0($char,$true))
    mode1 = [Convert]::ToHexString([Serve100.Protocol.LocalEntryPackets]::Mode1($char,$false))
    mode1Full = [Convert]::ToHexString([Serve100.Protocol.LocalEntryPackets]::Mode1($char,$true))
    mode3 = [Convert]::ToHexString([Serve100.Protocol.LocalEntryPackets]::Mode3($char))
    position = [Convert]::ToHexString([Serve100.Protocol.LocalEntryPackets]::Position($char))
    area = [Convert]::ToHexString([Serve100.Protocol.LocalEntryPackets]::Area($char))
    town = [Convert]::ToHexString([Serve100.Protocol.LocalEntryPackets]::Town($char))
    fatigue = [Convert]::ToHexString([Serve100.Protocol.LocalEntryPackets]::Fatigue($char))
    gameOptions = [Convert]::ToHexString([Serve100.Protocol.LocalGameOptionPackets]::Restore($options))
    hotkeys = [Convert]::ToHexString([Serve100.Protocol.LocalHotkeyPackets]::Restore($hotkeys))
}
$vectors | ConvertTo-Json | Set-Content -Encoding utf8NoBOM -LiteralPath (Join-Path $PSScriptRoot 'entry-bodies.json')
