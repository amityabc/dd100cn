using System.Collections;
using System.Collections.Immutable;
using System.Reflection;
using Serve100.Game;
using Serve100.Infrastructure.Resources;

internal static class DungeonDropExport
{
    public static object ReadWorld(PvfResourceProvider resources, IEnumerable<LocalDungeonDefinition> definitions,
        IEnumerable<LocalOculusModeDefinition> oculus)
    {
        var source = new LocalAwakeningMaterialDropCatalog(resources, new LocalPvfItemTemplates(resources));
        source.Prepare(definitions, oculus);
        var snapshot = GetField(source,"_snapshot");
        var world = (LocalAwakeningMaterialResource)Get(snapshot,"World");
        return new
        {
            source.Hash, Rows = world.AllRows.Select(row=>row.Columns).ToArray(), Seals = Get(Get(snapshot,"Independent"),"Seals"),
            Templates = Get(snapshot,"Templates")
        };
    }

    public static object Read(PvfResourceProvider resources, IEnumerable<LocalDungeonDefinition> definitions)
    {
        var layouts = definitions.DistinctBy(d => (d.Id, d.MazeIndex, d.ResourceVariant)).ToArray();
        var items = new LocalPvfItemTemplates(resources);
        var catalog = new LocalDungeonDropCatalog(resources, items);
        catalog.Prepare(layouts);
        var snapshot = GetField(catalog, "_snapshot");
        var native = Get(snapshot, "Native");
        var monsters = (LocalMonsterTemplateDropCatalog)Get(snapshot, "MonsterTemplates");
        var codes = layouts.SelectMany(d => d.Rooms.Prepend(d.Start)).SelectMany(r => r.Monsters)
            .Where(m => !m.Dummy).Select(m => m.Code).ToHashSet();
        var pools = GetField(monsters, "_monsters");
        var selected = ((IEnumerable)pools).Cast<object>().Where(row => codes.Contains((uint)Get(row, "Key")))
            .OrderBy(row => (uint)Get(row, "Key")).ToDictionary(row => (uint)Get(row, "Key"), row => Get(row, "Value"));
        var templateIds = selected.Values.SelectMany(row => new[] { "Ordinary", "Champion", "SuperChampion", "Boss" }
            .SelectMany(name => ((IEnumerable)Get(row, name)).Cast<object>())).Select(row => (uint)Get(row, "Item")).ToHashSet();
        var nativeTemplates = (ImmutableDictionary<uint, LocalItemTemplate>)GetField(monsters, "_templates");
        var templates = nativeTemplates.Where(p => templateIds.Contains(p.Key))
            .OrderBy(p => p.Key).ToDictionary(p => p.Key, p => p.Value);
        var candidates = new SortedDictionary<uint, LocalDungeonDropCandidate>();
        uint[] CandidateIds(ImmutableArray<LocalDungeonDropCandidate> pool)
        {
            foreach (var c in pool) candidates.TryAdd(c.Id, c);
            return pool.Select(c => c.Id).ToArray();
        }
        var mainPools = ((ImmutableDictionary<(byte Level, int Rarity, int Channel), ImmutableArray<LocalDungeonDropCandidate>>)
            Get(snapshot, "Pools")).OrderBy(p => p.Key).Where(p => !p.Value.IsEmpty).Select(p => new
            { p.Key.Level, p.Key.Rarity, p.Key.Channel, Ids = CandidateIds(p.Value) }).ToArray();
        var secondaryPools = ((ImmutableDictionary<(LocalDungeonSecondaryDropSource.SourceKind Kind, byte Level, int Rarity, int Channel), ImmutableArray<LocalDungeonDropCandidate>>)
            Get(snapshot, "SecondaryPools")).OrderBy(p => p.Key).Where(p => !p.Value.IsEmpty && p.Key.Kind is LocalDungeonSecondaryDropSource.SourceKind.MonsterExtra or LocalDungeonSecondaryDropSource.SourceKind.Object)
            .Select(p => new { p.Key.Kind, p.Key.Level, p.Key.Rarity, p.Key.Channel, Ids = CandidateIds(p.Value) }).ToArray();
        var secondary = ((LocalDungeonSecondaryDropSource)Get(snapshot, "Secondary")).Sources
            .Where(s => s.Kind is LocalDungeonSecondaryDropSource.SourceKind.MonsterExtra or LocalDungeonSecondaryDropSource.SourceKind.Object)
            .Select(s => new
            {
                s.Kind, s.Hash, s.DifficultyBonus, s.PartyBonus, s.MonsterBonus, s.RarityThresholds,
                Probabilities = s.ProbabilityRows.Select(row => new { row.MinimumLevel, row.MaximumLevel, Per10000 = row.Values })
            }).ToArray();
        var independent = Get(snapshot,"IndependentDrops");
        var independentCards = Get(snapshot,"IndependentCards");
        var independentEntries = Pairs(GetField(independent,"_entries"))
            .Select(p => (Key: ((byte Owner,uint Code))p.Key, p.Value))
            .Where(p=>codes.Contains(p.Key.Code))
            .Select(p => new { p.Key.Owner, p.Key.Code, Entries = p.Value }).ToArray();
        var independentTemplates = (ImmutableDictionary<uint,LocalItemTemplate>)GetField(independent,"_templates");
        var cardSnapshot = GetField(independentCards,"_snapshot");
        var cardEntries = Pairs(Get(cardSnapshot,"Entries")).Where(p=>codes.Contains((uint)p.Key)).Select(p => new
        {
            Code = (uint)p.Key, Entries = ((IEnumerable)p.Value).Cast<object>().Select(row => new
            {
                OwnerKind = Get(row,"OwnerKind"), DirectItem = Get(row,"Item"), Probabilities = Get(row,"Probabilities"),
                AttemptsByPartySize = Get(row,"AttemptsByPartySize"), ResultLimit = Get(row,"ResultLimit"),
                LevelMin = Get(row,"LevelMin"), LevelMax = Get(row,"LevelMax"), Difficulty = Get(row,"Difficulty"), Flag = Get(row,"Flag"),
                Main = ((ImmutableArray<(uint Item,uint Weight)>)Get(row,"List")).Select(w=>new { w.Item,w.Weight })
            }).ToArray()
        }).ToArray();
        var cardTemplates = (ImmutableDictionary<uint,LocalItemTemplate>)Get(cardSnapshot,"Templates");
        var pvfRewards = ReadPvfRewards(Optional(snapshot, "PvfRewards"), out var completionIds);
        var allIds = templates.Keys.Concat(candidates.Values.Where(c => c.Template is not null).Select(c => c.Id))
            .Concat(independentTemplates.Keys).Concat(cardTemplates.Keys).Concat(completionIds).Distinct().Order().ToArray();
        var first = catalog.FirstHuntPolicy;
        var weightPolicy = new LocalPlayerMailPolicy(resources, items);
        weightPolicy.Prepare(allIds);
        var weights = new SortedDictionary<uint,long>();
        foreach (uint id in allIds)
        {
            try { weights.Add(id, weightPolicy.ItemWeight(id)); }
            catch (InvalidDataException) { /* Candidate has no separate carrying-weight contract. */ }
            catch (FileNotFoundException) { }
        }
        return new
        {
            catalog.Hash, MonsterHash = monsters.Hash, Native = native, Gold = Get(snapshot, "Gold"),
            Monsters = selected, Templates = templates, Weights = weights, Candidates = candidates,
            MainPools = mainPools, SecondaryPools = secondaryPools, Secondary = secondary, catalog.EquipmentGeneration,
            FirstHunt = first is null ? null : new { first.Hash, first.BossMultiplier, first.NamedMultiplier },
            Independent = new { Hash = Get(independent,"Hash"), Entries = independentEntries, Templates = independentTemplates,
                AllowedDirectItems = GetField(independent,"_allowedDirectItems"), AllowedApcItems = GetField(independent,"_allowedApcItems"),
                AllowedApcSources = GetField(independent,"_allowedApcSources") },
            IndependentCards = new { Hash = Get(independentCards,"Hash"), Entries = cardEntries, Templates = cardTemplates },
            PvfRewards = pvfRewards
        };
    }

    private static object? ReadPvfRewards(object? catalog, out uint[] templateIds)
    {
        templateIds = [];
        if (catalog is null) return null;
        var ids = new HashSet<uint>();
        object? Pool(object? pool, bool indexed = false)
        {
            if (pool is null) return null;
            var candidates = ((IEnumerable)Get(pool, "Candidates")).Cast<object>().Select(entry =>
            {
                var template = (LocalItemTemplate?)Optional(entry, "Template");
                if (template is not null) ids.Add(template.TemplateId);
                var row = Get(entry, "Row");
                return new { Quantity = Get(row, "Quantity"), Weight = Get(row, "Weight"), Template = template };
            }).ToArray();
            return new { SelectionCount = indexed ? (byte)1 : (byte)Get(pool, "SelectionCount"), Candidates = candidates };
        }
        var groups = Pairs(GetField(catalog, "_groups"))
            .OrderBy(row => (uint)Get(row.Key, "Dungeon")).ThenBy(row => (byte)Get(row.Key, "Maze"))
            .ThenBy(row => (byte)Get(row.Key, "Difficulty")).ThenBy(row => (ushort)Get(row.Key, "ChallengeStage"))
            .Select(row => new
            {
                Dungeon = Get(row.Key, "Dungeon"), Maze = Get(row.Key, "Maze"),
                Difficulty = Get(row.Key, "Difficulty"), ChallengeStage = Get(row.Key, "ChallengeStage"),
                Branches = ((IEnumerable)Get(row.Value, "Branches")).Cast<object>().Select(branch => new
                {
                    Weight = Get(branch, "Weight"), Pool = Pool(Optional(branch, "Pool")), CompletionPool = Pool(Optional(branch, "CompletionPool"))
                }).ToArray()
            }).ToArray();
        var indexed = Pairs(GetField(catalog, "_indexedClearRewards")).Select(row =>
        {
            var key = ((uint Dungeon, byte Maze))row.Key;
            return new { key.Dungeon, key.Maze, Pool = Pool(row.Value, true) };
        }).OrderBy(row => row.Dungeon).ThenBy(row => row.Maze).ToArray();
        templateIds = ids.Order().ToArray();
        return new { Hash = Get(catalog, "Hash"), Groups = groups, Indexed = indexed };
    }

    private static object? Optional(object obj, string name) => obj.GetType().GetProperty(name,
        BindingFlags.Instance | BindingFlags.Public | BindingFlags.NonPublic)?.GetValue(obj);

    private static object Get(object obj, string name) => obj.GetType().GetProperty(name,
        BindingFlags.Instance | BindingFlags.Public | BindingFlags.NonPublic)?.GetValue(obj)
        ?? throw new InvalidDataException("Missing drop table value " + name);

    private static object GetField(object obj, string name) => obj.GetType().GetField(name, BindingFlags.Instance | BindingFlags.NonPublic)?.GetValue(obj)
        ?? throw new InvalidDataException("Missing drop table field " + name);
    private static IEnumerable<(object Key,object Value)> Pairs(object raw) => ((IEnumerable)raw).Cast<object>()
        .Select(row=>(Get(row,"Key"),Get(row,"Value")));
}
