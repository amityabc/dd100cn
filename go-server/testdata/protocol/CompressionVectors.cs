// Run under the same .NET runtime as the old Host. These deterministic test
// inputs exercise both native streaming buffer boundaries and the protocol cap.
using System;
using System.Collections.Generic;
using System.IO;
using System.IO.Compression;
using System.Runtime.InteropServices;
using System.Security.Cryptography;
using System.Text.Json;

var vectors = new List<object>();
foreach (var (size, mixed) in new[] { (0, false), (8192, false), (65537, true), (1048576, false), (1048576, true) })
{
    var input = new byte[size];
    uint state = 0x536a100;
    for (int i = 0; i < size; i++)
    {
        state ^= state << 13;
        state ^= state >> 17;
        state ^= state << 5;
        input[i] = mixed && i % 257 < 158 ? (byte)0 : (byte)state;
    }
    using var output = new MemoryStream();
    using (var z = new ZLibStream(output, CompressionLevel.Optimal, true)) z.Write(input);
    vectors.Add(new { size, mixed, compressed = output.Length, sha256 = Convert.ToHexString(SHA256.HashData(output.ToArray())) });
}
Console.WriteLine(JsonSerializer.Serialize(new { runtime = RuntimeInformation.FrameworkDescription, vectors }));
