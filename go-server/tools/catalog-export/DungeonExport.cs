using System.Collections;
using System.Collections.Immutable;
using System.Reflection;
using Serve100.Game;
using Serve100.Infrastructure.Resources;

internal static class DungeonExport
{
    private const BindingFlags Private = BindingFlags.Instance | BindingFlags.NonPublic;

    public static int Audit(string pvf, string profile)
    {
        using var resources = new PvfResourceProvider(Path.GetFullPath(pvf), Path.GetFullPath(profile), stableSource: true);
        var items = new LocalPvfItemTemplates(resources);
        var boss = new LocalOculusBossEquipmentCatalog(resources, items);
        var wisdom = new LocalWisdomEquipmentCatalog(resources, items, boss);
        wisdom.Prepare();
        var quests = new LocalQuestCatalog(resources);
        var tutorials = new LocalTutorialCatalog(resources);
        quests.Prepare();
        tutorials.Prepare();
        var catalog = new LocalDungeonCatalog(resources, quests, wisdom, items, tutorials);
        _ = catalog.PreparedDefinitions();
        var snapshot = Field(catalog, "_snapshot");
        var mainline = Property(snapshot, "Mainline");
        var story = (ImmutableArray<LocalDungeonDefinition>)Property(mainline, "Definitions");
        var advancement = ((IEnumerable)Property(snapshot, "Advancement")).Cast<object>()
            .Select(row => ((LocalDungeonDefinition)Property(row, "Dungeon"), ((LocalQuestDefinition)Property(row, "Quest")).Id)).ToArray();
        void Report(string scope, IEnumerable<(LocalDungeonDefinition Dungeon, ushort Quest)> source)
        {
            var rows = source.ToArray();
            var byMaze = rows.Select(p => p.Dungeon).DistinctBy(d => (d.Id, d.MazeIndex, d.ResourceHash)).ToArray();
            var skipped = rows.Where(p => p.Dungeon.Tournament is null && !SupportsPreparedLayer(p.Dungeon)).ToArray();
            Console.WriteLine($"{scope}: layouts={byMaze.Length}, dungeons={byMaze.Select(d => d.Id).Distinct().Count()}, " +
                $"quests={rows.Select(row => row.Quest).Where(id => id != 0).Distinct().Count()}, unsupported={skipped.Length}");
            foreach (var row in skipped.OrderBy(row => row.Dungeon.Id).ThenBy(row => row.Dungeon.MazeIndex).ThenBy(row => row.Quest))
                Console.WriteLine($"  dungeon={row.Dungeon.Id} maze={row.Dungeon.MazeIndex} quest={row.Quest} " +
                    $"layer={string.Join(',', row.Dungeon.Rooms.Prepend(row.Dungeon.Start).Select(r => r.CinematicLayer?.MapId).Where(id => id is not null).Distinct())}");
        }
        Report("mainline", story.SelectMany(d => d.QuestConnections.DefaultIfEmpty((ushort)0).Select(q => (d, q))));
        Report("advancement", advancement);
        var formerlySkipped = story.Concat(advancement.Select(row => row.Item1))
            .Where(d => d.Tournament is null && (d.Id == 239 || d.Rooms.Prepend(d.Start)
                .Any(room => !(room.CinematicLayer?.Monsters.IsEmpty ?? true) && !LocalWisdomStory.Layer(d, room))))
            .DistinctBy(d => (d.Id, d.MazeIndex, d.ResourceHash)).OrderBy(d => d.Id).ThenBy(d => d.MazeIndex).ToArray();
        Console.WriteLine("historical Go filter excluded: " + formerlySkipped.Length);
        foreach (var d in formerlySkipped)
            Console.WriteLine($"  dungeon={d.Id} maze={d.MazeIndex} quests={string.Join(',', d.QuestConnections.Order())}");
        var blocked = Pairs(Field(mainline, "_blocked")).ToArray();
        Console.WriteLine($"upstream resource-blocked quest combinations={blocked.Length}, upstream resource issue rows={catalog.UnavailableMainlineMazes.Length}");
        foreach (var issue in catalog.UnavailableMainlineMazes.OrderBy(row => row.Dungeon).ThenBy(row => row.Maze))
            Console.WriteLine($"  upstream issue dungeon={issue.Dungeon} maze={issue.Maze}: {issue.Reason}");
        return story.Concat(advancement.Select(row => row.Item1)).All(SupportsPreparedLayer) ? 0 : 1;
    }

    public static object Read(PvfResourceProvider resources, LocalQuestCatalog quests, LocalTutorialCatalog tutorials)
    {
        var itemTemplates = new LocalPvfItemTemplates(resources);
        var bossEquipment = new LocalOculusBossEquipmentCatalog(resources, itemTemplates);
        var wisdom = new LocalWisdomEquipmentCatalog(resources, itemTemplates, bossEquipment);
        wisdom.Prepare();
        var catalog = new LocalDungeonCatalog(resources, quests, wisdom, itemTemplates, tutorials);
        catalog.PreparedDefinitions();
        var snapshot = Field(catalog, "_snapshot");
        var gran = (ImmutableDictionary<uint, ImmutableArray<LocalDungeonDefinition>>)Property(snapshot, "Gran");
        var training = (ImmutableDictionary<uint, LocalDungeonDefinition>)Property(snapshot, "Training");
        var experience = new LocalDungeonExperienceCatalog(resources);
        experience.Prepare();
        var luck = LocalDungeonLuckParameters.Prepare(resources);
        var entries = new List<(int Gate, LocalDungeonDefinition Definition, object Data)>();
        void Add(int gate, LocalDungeonDefinition d, string admission, ushort[]? accepted = null, ushort[]? cleared = null,
            object[]? selector = null, ushort[]? blocked = null)
        {
            var targets = accepted ?? d.QuestConnections.Where(id => quests.Get(id) is { } q && catalog.IsQuestDestination(gate, d.Id, q)).Order().ToArray();
            var monsters = LocalDungeonNavigation.Scenes(d.Rooms.Prepend(d.Start)).SelectMany(room => room.Monsters)
                .Where(m => m.Code != 0 && m.Level != 0 && m.AiScriptIndex is null)
                .DistinctBy(m => (m.Level, Type: LocalDungeonNamedExperienceRules.IsNamed(d, m) ? (byte)2 : m.Type))
                .ToArray();
            var difficulties = admission == "mainline" && !d.SelectableDifficulties.IsEmpty ? d.SelectableDifficulties :
                d.QuestConnections.IsEmpty ? d.SelectableDifficulties.Add(d.Difficulty) : [d.Difficulty];
            var variants = difficulties.Distinct().Order().Select(difficulty => admission == "mainline"
                ? d with { Difficulty = difficulty }
                : d.SelectDifficulty(difficulty) ?? d with { Difficulty = difficulty }).ToArray();
            bool HasExp(LocalDungeonDefinition v) => v.DisableExperience || !v.UsesOrdinaryRewards ||
                v.StoryExperience is not { } story || v.Difficulty < story.BonusPercents.Length;
            var noExperience = variants.Where(v => !HasExp(v)).Select(v => v.Difficulty).ToArray();
            var awards = variants.Where(HasExp).Select(variant => new
                {
                    variant.Difficulty,
                    Monsters = d.DisableExperience ? [] : monsters.Select(m => new
                    {
                        m.Level,
                        Type = LocalDungeonNamedExperienceRules.IsNamed(d, m) ? (byte)2 : m.Type,
                        ByPlayerLevel = Enumerable.Range(1, 100)
                            .Select(level => experience.Reward(variant, m, (uint)level)).ToArray()
                    }).ToArray()
                }).ToArray();
            entries.Add((gate, d, new
            {
                Gate = gate, Dungeon = d, Admission = admission, EligibleQuests = targets, RequiredClears = cleared,
                Selector = selector, BlockedQuests = blocked, Awards = awards, UnavailableExperienceDifficulties = noExperience
            }));
        }

        foreach (var d in gran.OrderBy(p => p.Key).SelectMany(p => p.Value.OrderBy(d => d.MazeIndex))) Add(2, d, "gran");
        foreach (var p in training.OrderBy(p => p.Key)) Add(102, p.Value, "training");
        foreach (var p in Pairs(Property(snapshot, "Sky")).OrderBy(row => (ushort)row.Key))
        {
            var d = (LocalDungeonDefinition)Property(p.Value, "Dungeon");
            Add(185, d, "sky", [(ushort)p.Key], d.Id == 11 ? null : [3167]);
        }
        foreach (var p in ((ImmutableDictionary<uint, LocalDungeonDefinition>)Property(snapshot, "PassOrdinary")).OrderBy(p => p.Key))
            Add(185, p.Value, "sky", cleared: [3167]);
        foreach (var branch in ((IEnumerable)Property(snapshot, "Advancement")).Cast<object>())
        {
            var d = (LocalDungeonDefinition)Property(branch, "Dungeon");
            var quest = (LocalQuestDefinition)Property(branch, "Quest");
            if (d.Tournament is not null)
                Add((int)Property(branch, "Gate"), d with { EntryFatigueCost = 0, RoomFatigueCost = 0 }, "advancement", [quest.Id]);
            else
            {
                RequirePreparedLayer(d);
                Add((int)Property(branch, "Gate"), d, "advancement", [quest.Id]);
            }
        }
        foreach (var branch in ((IEnumerable)Property(snapshot, "Farming")).Cast<object>())
        {
            var d = (LocalDungeonDefinition)Property(branch, "Dungeon");
            ushort required = (ushort)Property(branch, "RequiredQuest"), guide = (ushort)Property(branch, "GuideQuest");
            Add((int)Property(branch, "Gate"), d, "farming", cleared: guide != 0 && guide != required ? [required, guide] : [required]);
        }
        foreach (var d in ((ImmutableArray<LocalDungeonDefinition>)Property(snapshot, "StormLane")).OrderBy(d => d.Id))
            Add(182, d, "storm", cleared: [12172]);
        foreach (var d in (ImmutableArray<LocalDungeonDefinition>)Property(snapshot, "Wisdom"))
            Add(LocalWisdomRules.Gate, d, "wisdom", cleared: [12177]);
        foreach (var d in ((ImmutableArray<LocalDungeonDefinition>)Property(snapshot, "Abaddon")).OrderBy(d => d.Id))
            Add(100, d, "abaddon", cleared: [6071]);
        foreach (var d in ((ImmutableArray<LocalDungeonDefinition>)Property(snapshot, "Ruby")))
            Add(180, d, "ruby");

        var mainline = Property(snapshot, "Mainline");
        var allMainline = (ImmutableArray<LocalDungeonDefinition>)Property(mainline, "Definitions");
        foreach (var definition in allMainline) RequirePreparedLayer(definition);
        var mainlineDefinitions = allMainline.GroupBy(d => d.Id)
            .ToDictionary(group => group.Key, group => group.OrderBy(d => d.MazeIndex).ToArray());
        var blockedQuests = Pairs(Field(mainline, "_blocked"))
            .Select(row => ((uint Dungeon, ushort Quest))row.Key)
            .ToLookup(row => row.Item1, row => row.Item2);
        foreach (var gate in Pairs(Field(mainline, "_gates")).OrderBy(row => (int)row.Key))
        {
            foreach (var choices in ((IEnumerable)gate.Value).Cast<object>().GroupBy(row => (uint)Property(row, "Dungeon")).OrderBy(group => group.Key))
            {
                if (!mainlineDefinitions.TryGetValue(choices.Key, out var mazes)) continue;
                var selector = choices.Select(row => new { Quest = (int)Property(row, "Quest"), InProgress = (bool)Property(row, "InProgress") }).ToArray();
                ushort[] blocked = blockedQuests[choices.Key].Order().ToArray();
                foreach (var maze in mazes) Add((int)gate.Key, maze, "mainline", selector: selector, blocked: blocked);
            }
        }

        var routes = new LocalTownRoutes(resources);
        routes.Prepare();
        var towns = Field(Field(routes, "_catalog"), "_snapshot");
        var gates = entries.Select(row => row.Gate).ToHashSet();
        var entrances = new List<object>();
        foreach (var pair in Pairs(Property(towns, "Areas")))
        {
            var key = pair.Key;
            ushort town = (ushort)Property(key, "Town"), area = (ushort)Property(key, "Area");
            if (routes.DungeonGate(town, area) is int gate && gates.Contains(gate))
                entrances.Add(new { Town = town, Area = area, Gate = gate });
        }
        var definitions = entries.Select(row => row.Data).ToArray();
        var tournamentItems = new LocalPvfItemTemplates(resources);
        tournamentItems.Prepare(LocalDungeonCatalog.TournamentRewardTemplates);
        var tournamentRewards = LocalDungeonCatalog.TournamentRewardTemplates.ToDictionary(id => id, id => tournamentItems.Get(id)
            ?? throw new InvalidDataException("Missing tournament material template " + id));
        var oculusCatalog = new LocalOculusCatalog(resources);
        oculusCatalog.Prepare();
        var oculusRewards = new LocalOculusRewardCatalog(resources, new LocalPvfItemTemplates(resources));
        oculusRewards.Prepare(oculusCatalog.Modes);
        itemTemplates.Prepare([LocalOculusBossDropRules.TimeGuideStone]);
        var oculusCards = oculusRewards.Prepared.ToDictionary(row=>row.DungeonId, row=>new
        { row.DungeonId, row.CounterDungeonId, row.Limit, row.CycleRaw, row.RoundCount, row.ResourceHash, row.Rows, row.Containers, row.Templates });
        var oculus = oculusCatalog.Modes.ToDictionary(mode => mode.Mode, mode =>
        {
            var dungeon = mode.Dungeon with { Difficulty = 4 };
            var spawns = dungeon.Rooms.SelectMany(r => r.Monsters).Where(m => m.AiScriptIndex is null && m.Code != 0 && m.Level != 0).ToArray();
            var samples = spawns.Concat(spawns.Select(m => m with { Type = 3 })).DistinctBy(m => (m.Level,m.Type));
            return new
            {
                mode.Mode, Dungeon = dungeon, mode.Rules, mode.SharedRules, mode.UsesPublishedLayout, mode.EventSpawnMaps,
                dungeon.DisableDrops, dungeon.DisableClearReward, dungeon.DisableExperience, dungeon.UsesOrdinaryRewards,
                dungeon.UsesOrdinaryMonsterDrops, dungeon.ExperienceMultiplier,
                SelectableDifficulties = dungeon.SelectableDifficulties.Select(value => (int)value).ToArray(),
                mode.StartMaps, mode.WarpConditions, mode.ClearMapId, mode.ClearMapRawValue, mode.SpawnPlanKind, mode.SpawnPlans,
                mode.SourceDungeonEntryFatigue, mode.SourceCoinLimit, mode.SourcePathgateObjectsRaw, mode.SourceHash,
                Awards = new[] { new { Difficulty = (byte)4, Monsters = dungeon.DisableExperience ? [] : samples.Select(m => new
                {
                    m.Level, m.Type, ByPlayerLevel = Enumerable.Range(1,100).Select(level => experience.Reward(dungeon,m,(uint)level)).ToArray()
                }).ToArray() } }
            };
        });
        Console.WriteLine($"Ordinary dungeon catalog: {definitions.Length} layouts and {entrances.Count} town entrances.");
        return new
        {
            Entrances = entrances, Definitions = definitions, ExperienceHash = experience.Hash, Luck = luck, Ruby = catalog.RubyResources,
            ResourceIssues = catalog.UnavailableMainlineMazes,
            BossEquipment = WisdomExport.Boss(bossEquipment), Wisdom = WisdomExport.Rewards(wisdom),
            ClearExperience = experience.ClearExperience, RankExperience = experience.RankExperience,
            Drops = DungeonDropExport.Read(resources, entries.Select(row => row.Definition)),
            Cards = DungeonCardExport.Read(resources, entries.Select(row => row.Definition)),
            TournamentRewards = tournamentRewards,
            Oculus = oculus,
            OculusCards = oculusCards,
            WorldDrops = DungeonDropExport.ReadWorld(resources, catalog.PreparedDefinitions(), oculusCatalog.Modes),
            SpecialRewards = DungeonSpecialExport.Read(resources, itemTemplates, catalog.PreparedDefinitions()),
            OculusStone = itemTemplates.Get(LocalOculusBossDropRules.TimeGuideStone) ??
                throw new InvalidDataException("Oculus time guide stone template is unavailable")
        };
    }

    private static bool SupportsPreparedLayer(LocalDungeonDefinition definition) =>
        definition.Rooms.Prepend(definition.Start).All(room =>
            (room.CinematicLayer?.Monsters.IsEmpty ?? true) || LocalWisdomStory.Layer(definition, room) ||
            LocalNoireCinematic.SharedLayer(definition, room) || LocalDungeonScriptTerminal.SharedAwakeningLayer(definition, room) ||
            definition.Id == 100000326 && room.CinematicLayer?.MapId == 100001178);

    private static void RequirePreparedLayer(LocalDungeonDefinition definition)
    {
        if (!SupportsPreparedLayer(definition))
            throw new InvalidDataException($"No Go layer owner for prepared dungeon {definition.Id}, maze {definition.MazeIndex}");
    }

    private static IEnumerable<(object Key, object Value)> Pairs(object source) => ((IEnumerable)source).Cast<object>()
        .Select(row => (Property(row, "Key"), Property(row, "Value")));

    private static object Field(object source, string name) =>
        source.GetType().GetField(name, Private)?.GetValue(source)
        ?? throw new InvalidDataException("Missing dungeon resource field " + name);

    private static object Property(object source, string name) =>
        source.GetType().GetProperty(name, BindingFlags.Instance | BindingFlags.Public | BindingFlags.NonPublic)?.GetValue(source)
        ?? throw new InvalidDataException("Missing dungeon resource property " + name);
}
