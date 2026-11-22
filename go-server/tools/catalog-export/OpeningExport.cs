using System.Collections.Immutable;
using System.Reflection;
using Serve100.Game;
using Serve100.Infrastructure.Resources;

internal static class OpeningExport
{
    public static object Read(PvfResourceProvider resources, LocalProgressionCatalog progression,
        LocalTutorialCatalog tutorials, LocalSkillCatalog skills)
    {
        var experience = new LocalDungeonExperienceCatalog(resources);
        experience.Prepare();
        var levels = Enumerable.Range(1,100).Select(i => new {
            Threshold = progression.ThresholdForNext((byte)i),
            Sp = progression.SkillPointsAtLevel((byte)i),
            Tp = progression.TraitPointsAtLevel((byte)i)
        }).ToArray();
        var jobs = new SortedDictionary<byte,object>();
        var index = resources.ReadIndex("character",1024*1024);
        for(byte job=0;job<16;job++)
        {
            var d=tutorials.Get(job);
            if(d is null)continue;
            var characters=resources.ReadText(index.Entries[job],1024*1024);
            var provider=LocalGrowthTable.Parse(characters);
            var initial = Field<ImmutableDictionary<string,decimal>>(provider,"_initial");
            var curves = Field<ImmutableDictionary<(int Branch,int Stage),ImmutableDictionary<string,decimal>>>(provider,"_growth");
            var growth = curves.OrderBy(c=>c.Key.Branch).ThenBy(c=>c.Key.Stage).Select(c=>new {
                c.Key.Branch,c.Key.Stage,Values=c.Value
            }).ToArray();
            var grants = LocalSkillCatalog.ParseAdvancementGrants(characters)
                .Where(p=>p.Key==0 ? p.Value.All(g=>skills.Get(job,g.Id) is not null) : skills.SupportsBranch(job,p.Key))
                .ToDictionary(p=>p.Key,p=>p.Value.Select(g=>new {
                    g.Id,g.Level,g.RequiredLevel,g.Awakening,Active=skills.Get(job,g.Id)!.Active
                }).ToArray());
            var awards = d.Dungeon.Rooms.Prepend(d.Dungeon.Start).SelectMany(r=>r.Monsters)
                .DistinctBy(m=>(m.Level,m.Type)).OrderBy(m=>m.Level).ThenBy(m=>m.Type)
                .Select(m=>new {m.Level,m.Type,ByPlayerLevel=Enumerable.Range(1,100)
                    .Select(level=>experience.Reward(d.Dungeon,m,(byte)level)).ToArray()}).ToArray();
            jobs.Add(job,new {Initial=initial,Growth=growth,Grants=grants,Awards=awards,
                d.Dungeon.ExperienceMultiplier,d.Dungeon.DisableExperience});
        }
        var stackable=resources.ReadCommerceIndex("stackable");
        var tickets=new Dictionary<uint,LocalClassTicketDefinition>();
        foreach(uint id in new uint[]{2660702,2660779})
        {
            if(!stackable.Entries.TryGetValue((int)id,out string? path))continue;
            string text=resources.ReadText(path,1024*1024);
            var template=LocalPvfGiftItemTemplates.Parse(id,false,text,resources.Probe(path).Sha256);
            tickets.Add(id,LocalClassTicketResources.Parse(template,text));
        }
        return new {ExperienceHash=experience.Hash,Levels=levels,Jobs=jobs,ClassTickets=tickets,Replacement=progression.ClassReplacementPolicy};
    }

    // Keep the exact decimal tables consumed by the pinned upstream algorithm.
    // Exporting sampled integer deltas loses its per-level truncation behavior.
    private static T Field<T>(LocalGrowthTable source,string name) where T:class =>
        typeof(LocalGrowthTable).GetField(name, BindingFlags.Instance|BindingFlags.NonPublic)?.GetValue(source) as T
        ?? throw new InvalidDataException("Pinned character growth field is unavailable: "+name);
}
