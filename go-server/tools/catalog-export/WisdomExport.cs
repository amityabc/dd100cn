using System.Collections;
using System.Collections.Immutable;
using System.Reflection;
using Serve100.Game;
using Serve100.Infrastructure.Resources;

internal static class WisdomExport
{
    private static object Field(object owner, string key) =>
        owner.GetType().GetField(key, BindingFlags.NonPublic | BindingFlags.Instance)?.GetValue(owner)
        ?? throw new InvalidDataException("Missing prepared Wisdom resource " + key);

    public static object Boss(LocalOculusBossEquipmentCatalog catalog)
    {
        var pools = new Dictionary<byte, object[]>();
        foreach (var pair in (IEnumerable)Field(catalog, "_pools"))
        {
            var key = (byte)pair.GetType().GetProperty("Key")!.GetValue(pair)!;
            var value = (IEnumerable)pair.GetType().GetProperty("Value")!.GetValue(pair)!;
            pools.Add(key, value.Cast<object>().ToArray());
        }
        return new { catalog.Hash, Epic = LocalOculusBossEquipmentRules.ReferenceEpic,
            Mythic = LocalOculusBossEquipmentRules.ReferenceMythic, Pools = pools };
    }

    public static object Rewards(LocalWisdomEquipmentCatalog catalog)
    {
        var products = (ImmutableArray<(LocalItemTemplate Template, uint Weight)>)Field(catalog, "_products");
        return new { catalog.Hash, catalog.Plan(0).PillarRewards,
            Products = products.Select(p => new { Id = p.Template.TemplateId, p.Template, p.Weight }),
            Materials = (ImmutableDictionary<uint, LocalItemTemplate>)Field(catalog, "_materials") };
    }
}
