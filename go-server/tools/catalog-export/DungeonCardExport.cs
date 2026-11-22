using System.Reflection;
using System.Collections;
using System.Collections.Immutable;
using Serve100.Game;
using Serve100.Infrastructure.Resources;

internal static class DungeonCardExport
{
    public static object Read(PvfResourceProvider resources, IEnumerable<LocalDungeonDefinition> definitions)
    {
        var templates = new LocalPvfItemTemplates(resources);
        var cards = new LocalDungeonCardRewardCatalog(resources, templates);
        cards.Prepare();
        var snapshot = Field(cards, "_snapshot");
        var paid = new LocalDungeonPaidCardRewardCatalog(resources, templates);
        var prepared = definitions.Where(d => d.UsesOrdinaryRewards).DistinctBy(d => (d.Id,d.MazeIndex)).ToArray();
        paid.Prepare(prepared);
        var challenge = new LocalGhentPremiumCardRewardCatalog(resources, templates, paid, challengeOnly:true);
        challenge.Prepare(prepared);
        var challengeRows = ((IEnumerable)Field(challenge, "_challengeSnapshots")).Cast<object>().Select(entry =>
        {
            var key = Get(entry, "Key")!;
            var source = Get(entry, "Value")!;
            var maps = new SortedDictionary<byte, string>();
            foreach (var row in (IEnumerable)Get(source, "Definitions")!)
            {
                var d = (LocalDungeonDefinition)Get(row, "Value")!;
                maps.Add(d.MazeIndex, d.ResourceHash);
            }
            return new {
                Dungeon = key.GetType().GetField("Item1")!.GetValue(key),
                Stage = key.GetType().GetField("Item2")!.GetValue(key),
                Hash = Get(source,"Hash"), Chance = Get(source,"Chance"), Cost = Get(source,"Cost"),
                Choices = Get(source,"Choices"), Templates = Get(source,"Templates"), Definitions = maps
            };
        }).ToArray();
        var second = Field(paid, "_snapshot");
        object? Specific(object parent) => Get(parent, "SpecificItems") is not { } source ? null : new
        {
            Dungeons = Field(source, "_dungeons"), Templates = Field(source, "_templates"), Luck = Field(source, "_luck")
        };
        object? Groups(object parent) => Get(parent, "Groups") is not { } source ? null : new
        {
            Groups = Field(source, "_groups"), Dungeons = Field(source, "_dungeons"), Templates = Field(source, "_templates"), Luck = Field(source, "_luck")
        };
        var paidEquipment = new SortedDictionary<uint,LocalDungeonDropCandidate>();
        var paidPools = new List<object>();
        foreach (var set in new (string Field,int Type)[] { ("StackablePools",1),("Pools",2),("RecipePools",3),("CreaturePools",4) })
        {
            var rows = (ImmutableDictionary<(byte Level,int Rarity),ImmutableArray<LocalDungeonDropCandidate>>)Get(second,set.Field)!;
            foreach (var row in rows.OrderBy(p=>p.Key).Where(p=>!p.Value.IsEmpty))
            {
                foreach (var c in row.Value) paidEquipment.TryAdd(c.Id,c);
                paidPools.Add(new { set.Type, row.Key.Level, row.Key.Rarity, Items = row.Value.Select(c=>c.Id).ToArray() });
            }
        }
        return new
        {
            cards.Hash, GuideHash = Get(snapshot,"GuideHash"), DifficultyRates = Get(snapshot,"DifficultyRates"),
            SoloPartyRate = Get(snapshot,"SoloPartyRate"), Rank = Get(snapshot,"Rank"),
            Probabilities = Get(snapshot,"Probabilities"), Equipment = Get(snapshot,"Equipment"),
            DropKinds = Get(snapshot,"DropKindProbabilities"), ItemTypes = Get(snapshot,"DropItemTypeProbabilities"),
            DifficultyBonuses = Get(snapshot,"DifficultyItemBonuses"), Rarity = Get(snapshot,"Rarity"),
            Windows = Get(snapshot,"Windows"), MapRates = Get(snapshot,"MapRates"),
            Specific = Specific(snapshot), Generation = Get(snapshot,"EquipmentGeneration"), Challenge = challengeRows,
            Paid = new
            {
                paid.Hash, KindHash = Get(second,"KindHash"), Costs = Get(second,"Costs"), paid.Profiles,
                DropKinds = Get(second,"DropKinds"), ItemTypes = Get(second,"ItemTypes"),
                Rarity = Get(second,"Rarity"), DifficultyBonuses = Get(second,"DifficultyItemBonuses"),
                CreateRateParts = Get(second,"CreateRateParts"), Pools = paidPools, Equipment = paidEquipment,
                Specific = Specific(second), Groups = Groups(second), Generation = Get(second,"EquipmentGeneration")
            }
        };
    }

    private static object? Get(object obj,string name) => obj.GetType().GetProperty(name,
        BindingFlags.Instance | BindingFlags.Public | BindingFlags.NonPublic)?.GetValue(obj);
    private static object Field(object obj,string name) => obj.GetType().GetField(name,
        BindingFlags.Instance | BindingFlags.NonPublic)?.GetValue(obj) ?? throw new InvalidDataException("Missing card resource " + name);
}
