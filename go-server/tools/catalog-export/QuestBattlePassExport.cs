using Serve100.Game;
using Serve100.Infrastructure.Resources;

internal static class QuestBattlePassExport
{
    public static object Read(PvfResourceProvider resources)
    {
        const string path="etc/chn_battlepass.etc";
        var definition=LocalBattlePassCatalog.Parse(resources.ReadText(path,1024*1024),resources.Probe(path).Sha256);
        var source=resources.ReadCommerceIndex("stackable");
        var templates=new Dictionary<uint,LocalItemTemplate>();
        foreach(uint id in definition.Rewards.Values.SelectMany(rows=>rows).Select(r=>r.Template)
            .Concat(definition.MaximumBonus is {} bonus ? [bonus.Template] : Array.Empty<uint>()).Distinct().Order())
        {
            if(!source.Entries.TryGetValue(id,out var item))continue;
            try { templates.Add(id,LocalPvfGiftItemTemplates.Parse(id,false,resources.ReadText(item,1024*1024),resources.Probe(item).Sha256,battlePassSource:true)); }
            catch(InvalidDataException){ /* A reward with no complete acquisition owner cannot be claimed. */ }
        }
        return new {Definition=definition,Templates=templates};
    }
}
