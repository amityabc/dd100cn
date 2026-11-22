using System.Collections;
using System.Reflection;
using Serve100.Game;
using Serve100.Infrastructure.Resources;

internal static class DungeonSpecialExport
{
    private const BindingFlags Private = BindingFlags.Instance | BindingFlags.NonPublic;
    public static object Read(PvfResourceProvider resources, LocalPvfItemTemplates items, IEnumerable<LocalDungeonDefinition> definitions)
    {
        var catalog = new LocalGhentSpecialRewardCatalog(resources, items);
        catalog.Prepare(definitions);
        var snapshots = typeof(LocalGhentSpecialRewardCatalog).GetField("_snapshots", Private)!.GetValue(catalog)!;
        var smartPool = typeof(LocalGhentSpecialRewardCatalog).GetMethod("SmartPool", Private)!;
        var result = new SortedDictionary<uint, object>();
        foreach (var row in (IEnumerable)snapshots)
        {
            var id = (uint)Get(row, "Key");
            var snapshot = Get(row, "Value");
            var containers = (IEnumerable)Get(snapshot, "Containers");
            var smart = containers.Cast<object>().Any(entry => (bool)Get(Get(entry, "Value"), "Smart"));
            var pools = new SortedDictionary<int, object>();
            if (smart)
                for (byte job = 0; job < 16; job++)
                    for (byte grow = 0; grow < 16; grow++)
                        pools.Add(job * 16 + grow, smartPool.Invoke(catalog, [job, grow, id])!);
            var maps = new SortedDictionary<byte, string>();
            foreach (var definition in (IEnumerable)Get(snapshot, "Definitions"))
            {
                var frozen = (LocalDungeonDefinition)Get(definition, "Value");
                maps.Add(frozen.MazeIndex, frozen.ResourceHash);
            }
            result.Add(id, new
            {
                Policy = LocalDungeonSpecialRewardRules.PolicyFor(id),
                Counter = LocalDungeonSpecialRewardRules.CounterDungeon(id),
                Limit = Get(snapshot, "Limit"),
                Hash = Get(snapshot, "Hash"),
                Rows = Get(snapshot, "Rows"),
                Containers = Get(snapshot, "Containers"),
                Templates = Get(snapshot, "Templates"),
                SmartPools = pools,
                Definitions = maps,
            });
        }
        return result;
    }
    private static object Get(object value, string name) =>
        value.GetType().GetProperty(name, BindingFlags.Public | BindingFlags.Instance)!.GetValue(value)!;
}
