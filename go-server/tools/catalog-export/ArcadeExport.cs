using System.Collections.Immutable;
using Serve100.Game;
using Serve100.Infrastructure.Resources;

internal static class ArcadeExport
{
    public static object Read(PvfResourceProvider resources)
    {
        var arcade = new LocalArcadeCatalog(resources);
        arcade.Prepare();
        var rows = new List<object>();
        var profile = LocalGameplayProfile.Empty;
        var stats = new LocalCharacterStats(ImmutableDictionary<string,long>.Empty);
        for (byte job = 0; job < 16; job++)
        for (byte grow = 0; grow <= 5; grow++)
        {
            // Both lookups key only on job and the base grow nibble, including imported characters.
            var c = new LocalCharacter(1, 0, "", job, 50, 0, 0, stats, 0, 0, 0, 0, 0, 0, "")
            { Gameplay = profile with { Progression = profile.Progression with { GrowType = grow } } };
            void Add(LocalArcadeDefinition? definition)
            {
                if (definition != null) rows.Add(new { Job = job, Grow = grow, Definition = definition });
            }
            for (byte difficulty = 0; difficulty <= 2; difficulty++) Add(arcade.Get(c, difficulty));
            foreach (ushort quest in new ushort[] { 2672, 2673, 2674 }) Add(arcade.GetTraining(c, quest));
        }
        return rows;
    }
}
