using System.Collections;
using System.Reflection;
using Serve100.Game;
using Serve100.Infrastructure.Resources;

// The pinned legacy parser owns the PVF grammar. Read its prepared snapshot offline so Go can
// evaluate the original conditional rules for each character, including ones imported later.
internal static class TownExport
{
    private static readonly BindingFlags Private = BindingFlags.Instance | BindingFlags.NonPublic;

    private static object Field(object obj, string name) =>
        obj.GetType().GetField(name, Private)?.GetValue(obj)
        ?? throw new InvalidDataException($"Missing prepared catalog field {name}");

    private static object Property(object obj, string name) =>
        obj.GetType().GetProperty(name)?.GetValue(obj)
        ?? throw new InvalidDataException($"Missing prepared catalog value {name}");

    private static IEnumerable<(object Key, object Value)> Pairs(object dictionary) =>
        ((IEnumerable)dictionary).Cast<object>().Select(p => (Property(p, "Key"), Property(p, "Value")));

    private static (ushort Town, ushort Area) Key(object k) => ((ushort)Property(k,"Town"), (ushort)Property(k,"Area"));

    private static object Permission(object p) => new {
        Level = Property(p,"Level"), All = Property(p,"All"), Any = Property(p,"Any"),
        Except = Property(p,"Except"), Started = Property(p,"Started"), Blocked = Property(p,"Blocked")
    };

    public static object Read(PvfResourceProvider resources, LocalQuestCatalog quests)
    {
        var routes = new LocalTownRoutes(resources);
        routes.Prepare();
        var snapshot = Field(Field(routes, "_catalog"), "_snapshot");
        var areas = Pairs(Property(snapshot, "Areas")).OrderBy(p => Key(p.Key)).Select(p => new {
            Key(p.Key).Town, Key(p.Key).Area, Path = Property(p.Value, "Path"),
            Permission = Permission(Property(p.Value,"Permission")), Phases = Property(p.Value,"Phases")
        }).ToArray();
        var maps = Pairs(Property(snapshot,"Maps")).OrderBy(p => (string)p.Key, StringComparer.Ordinal).Select(p => new {
            Path = (string)p.Key, Npcs = Property(p.Value, "Npcs"),
            AirshipRoomMenu = Property(p.Value, "AirshipRoomMenu"), DockMenu = Property(p.Value, "DockMenu"),
            Routes = Pairs(Property(p.Value,"Routes")).OrderBy(r => Key(r.Key)).Select(r =>
                new { Key(r.Key).Town, Key(r.Key).Area, Permission = Permission(r.Value) }).ToArray()
        }).ToArray();
        var phases = Pairs(Property(snapshot,"Phases")).OrderBy(p => (ushort)p.Key).ToDictionary(p => (ushort)p.Key, p => p.Value);
        var returns = Pairs(Property(snapshot,"RoomReturns")).OrderBy(p => Key(p.Key)).Select(p =>
            new { Key(p.Key).Town, Key(p.Key).Area, Point = p.Value }).ToArray();
        var supported = typeof(LocalQuestTownRange).GetMethod("Supported", BindingFlags.Static | BindingFlags.NonPublic)
            ?? throw new InvalidDataException("Missing native town range eligibility predicate");
        var ranges = quests.All.Where(q => q.Kind == LocalQuestKind.ReachRange && (bool)supported.Invoke(null, [q])!)
            .Select(q => new { q.Id, q.Hash, q.Subtype, q.Data, q.NpcRangeTarget, q.ChildRangeBinding }).ToArray();
        Console.WriteLine($"Town catalog: {routes.TownCount} towns, {areas.Length} areas, {maps.Length} maps, {ranges.Length} eligible range quests.");
        return new { Areas = areas, Maps = maps, Phases = phases, RoomReturns = returns,
            Warps = Property(snapshot, "Warps"), NpcWarps = Property(snapshot, "NpcWarps"), Menus = Property(snapshot, "Menus"),
            Room = routes.RoomEntryPoint, Exit = Property(snapshot, "Exit"), Ranges = ranges };
    }
}
