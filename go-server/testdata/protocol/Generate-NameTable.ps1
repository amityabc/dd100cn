# Regenerate the portable strict CP54936 mapping from the same .NET provider
# as LocalEntryPackets. This is a development tool; Go embeds its result.
$ErrorActionPreference = 'Stop'
$target = Join-Path $PSScriptRoot '../../internal/protocol/name54936.bin'
Add-Type -TypeDefinition @'
using System;
using System.Collections.Generic;
using System.IO;
using System.Text;

public static class LegacyNameTable {
    // Four-byte index from GB18030. 189000 is the first supplementary scalar.
    static byte[] Four(int n) {
        var b = new byte[4];
        b[3] = (byte)(0x30 + n % 10); n /= 10;
        b[2] = (byte)(0x81 + n % 126); n /= 126;
        b[1] = (byte)(0x30 + n % 10); n /= 10;
        b[0] = (byte)(0x81 + n);
        return b;
    }
    static void Check(Encoding enc, byte[] bytes, string value) {
        if (!enc.GetBytes(value).AsSpan().SequenceEqual(bytes))
            throw new InvalidDataException("Non-canonical byte sequence: " + Convert.ToHexString(bytes));
    }
    public static void Write(string target, Encoding enc) {
        using var memory = new MemoryStream();
        using var data = new BinaryWriter(memory);
        var occupied = new HashSet<int>();
        for (int lead = 0x81; lead <= 0xfe; lead++) {
            for (int trail = 0x40; trail <= 0xfe; trail++) {
                if (trail == 0x7f) continue;
                byte[] wire = {(byte)lead, (byte)trail};
                string value = enc.GetString(wire);
                if (value.Length != 1 || !occupied.Add(value[0])) throw new InvalidDataException("Invalid pair mapping");
                Check(enc, wire, value);
                data.Write((ushort)value[0]);
            }
        }
        int previous = -2;
        for (int index = 0; index < 39420; index++) {
            var wire = Four(index);
            string value = enc.GetString(wire);
            if (value.Length != 1 || !occupied.Add(value[0])) throw new InvalidDataException("Invalid BMP mapping");
            Check(enc, wire, value);
            if (value[0] != previous + 1) {
                data.Write((ushort)index);
                data.Write((ushort)value[0]);
            }
            previous = value[0];
        }
        for (int cp = 0x10000; cp <= 0x10ffff; cp++) {
            byte[] wire = Four(189000 + cp - 0x10000);
            string value = char.ConvertFromUtf32(cp);
            if (enc.GetString(wire) != value) throw new InvalidDataException("Invalid supplementary mapping");
            Check(enc, wire, value);
        }
        File.WriteAllBytes(target, memory.ToArray());
    }
}
'@
[Text.Encoding]::RegisterProvider([Text.CodePagesEncodingProvider]::Instance)
$encoding = [Text.Encoding]::GetEncoding(54936, [Text.EncoderFallback]::ExceptionFallback, [Text.DecoderFallback]::ExceptionFallback)
[LegacyNameTable]::Write([IO.Path]::GetFullPath($target), $encoding)
