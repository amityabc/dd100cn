using System.Collections;
using System.Diagnostics;
using System.Reflection;
using System.Runtime.Loader;
using System.Security.Cryptography;
using System.Text.Json;
using Microsoft.Extensions.Hosting;

internal static class RouteExport
{
    private sealed class Observe<T>(Action<T> received) : IObserver<T>
    {
        public void OnNext(T value) => received(value);
        public void OnCompleted() { }
        public void OnError(Exception error) => throw error;
    }

    private const BindingFlags Fields = BindingFlags.Instance | BindingFlags.Public | BindingFlags.NonPublic;
    private static object GetField(object obj, string name) =>
        obj.GetType().GetField(name, Fields)?.GetValue(obj) ?? throw new InvalidDataException($"Missing {name}");

    public static async Task<int> Run(string isolatedRoot, string legacyOutput, string output)
    {
        string dir = Path.GetFullPath(legacyOutput);
        Assembly? Resolve(AssemblyLoadContext context, AssemblyName name)
        {
            string path = Path.Combine(dir, name.Name + ".dll");
            return File.Exists(path) ? context.LoadFromAssemblyPath(path) : null;
        }
        AssemblyLoadContext.Default.Resolving += Resolve;
        string hostPath = Path.Combine(dir, "Serve100.Host.dll");
        var hostAssembly = AssemblyLoadContext.Default.LoadFromAssemblyPath(hostPath);
        var tcpType = hostAssembly.GetType("Serve100.Host.TcpServer", true)!;
        var handlerType = hostAssembly.GetType("Serve100.Host.IFrameHandler", true)!;
        var rows = new List<object>();
        var subscriptions = new List<IDisposable>();
        using var subscription = DiagnosticListener.AllListeners.Subscribe(new Observe<DiagnosticListener>(d =>
        {
            if (d.Name != "Microsoft.Extensions.Hosting") return;
            subscriptions.Add(d.Subscribe(new Observe<KeyValuePair<string,object?>>(e =>
            {
                if (e.Key != "HostBuilt" || e.Value is not IHost host) return;
                var tcp = host.Services.GetService(tcpType) ?? throw new InvalidDataException("TCP registry unavailable");
                var handlers = (IDictionary)GetField(GetField(tcp, "_routes"), "_handlers");
                foreach (DictionaryEntry entry in handlers)
                {
                    var key = entry.Key;
                    object Get(string name) => key.GetType().GetProperty(name)!.GetValue(key)!;
                    var outer = entry.Value!;
                    var chain = new List<string>();
                    var seen = new HashSet<object>(ReferenceEqualityComparer.Instance);
                    var pending = new Queue<object>();
                    pending.Enqueue(outer);
                    while (pending.TryDequeue(out var layer))
                    {
                        if (!seen.Add(layer)) continue;
                        chain.Add(layer.GetType().FullName!);
                        foreach (var f in layer.GetType().GetFields(Fields))
                            if (handlerType.IsAssignableFrom(f.FieldType) && f.GetValue(layer) is {} next) pending.Enqueue(next);
                    }
                    rows.Add(new { Transport = Get("Transport").ToString(), Direction = Get("Direction").ToString(),
                        Class = Get("Class"), Opcode = Get("Opcode"), Handlers = chain });
                }
                // Collection is complete before any hosted service or TCP listener starts.
                throw new OperationCanceledException("Offline route inventory captured");
            })));
        }));
        try
        {
            var main = hostAssembly.GetType("Serve100.Host.Program", true)!.GetMethod("Main")!;
            await (Task<int>)main.Invoke(null, [new[]{"--root", Path.GetFullPath(isolatedRoot)}])!;
        }
        finally
        {
            foreach (var s in subscriptions) s.Dispose();
            AssemblyLoadContext.Default.Resolving -= Resolve;
        }
        if (rows.Count == 0) throw new InvalidDataException("No routes captured");
        var sorted = rows.Select(row => JsonSerializer.SerializeToElement(row))
            .OrderBy(r => r.GetProperty("Transport").GetString(), StringComparer.Ordinal)
            .ThenBy(r => r.GetProperty("Class").GetByte()).ThenBy(r => r.GetProperty("Opcode").GetInt32()).ToArray();
        using var input = File.OpenRead(hostPath);
        File.WriteAllText(output, JsonSerializer.Serialize(new { HostSha256=Convert.ToHexString(SHA256.HashData(input)),
            Routes=sorted },new JsonSerializerOptions{WriteIndented=true}));
        Console.WriteLine($"Captured {rows.Count} formal route keys with their registered handler chains.");
        return 0;
    }
}
