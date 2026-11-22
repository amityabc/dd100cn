using Serve100.Game;
using Serve100.Infrastructure.Resources;

internal static class QuestGrowthExport
{
    public static object Read(PvfResourceProvider resources,LocalPvfItemTemplates items)
    {
        var exp=new Dictionary<uint,LocalExperienceItemDefinition>();
        var level=new Dictionary<uint,LocalLevelTicketDefinition>();
        var capsules=new Dictionary<uint,LocalLevel100CapsuleDefinition>();
        var questTickets=new Dictionary<uint,object>();
        var colors=new Dictionary<uint,int>();
        var sockets=new LocalEmblemSocketCatalog(resources);
        var compound=new LocalEmblemCompoundCatalog(resources,new LocalPlayerMailPolicy(resources,items));
        var petCare=new LocalPetCareCatalog(resources);
        petCare.Prepare();
        var petGrowth=new LocalPetGrowthCatalog(resources,petCare);
        petGrowth.Prepare();
        var sources=new Dictionary<uint,LocalEmblemCompoundSource>();
        var abilitySources=new List<uint>();
        var dyes=new List<uint>();
        var teleports=new Dictionary<uint,LocalTeleportDefinition>();
        var absolute=new List<uint>();
        var avatarConversion=new List<LocalAvatarConversionDefinition>();
        var index=resources.ReadCommerceIndex("stackable");
        foreach(var row in index.Entries.OrderBy(r=>r.Key))
        {
            if(row.Key<=0||row.Key>int.MaxValue)continue;
            uint id=(uint)row.Key;
            try
            {
                var text=resources.ReadText(row.Value,1024*1024);
                if(text.Contains("[avatar select ability change]",StringComparison.Ordinal))abilitySources.Add(id);
                if(text.Contains("`[dye]`",StringComparison.Ordinal))dyes.Add(id);
                if(text.Contains("`[absolute bind cube]`",StringComparison.Ordinal))absolute.Add(id);
                if(text.Contains("`[change avatar]`",StringComparison.Ordinal))
                    avatarConversion.Add(LocalAvatarConversionCatalog.ParseSource(id,text,resources.Probe(row.Value).Sha256));
                if(text.Contains("`[add fatigue]`",StringComparison.Ordinal))items.Prepare([id]);
                if(text.Contains("`[teleport potion]`",StringComparison.Ordinal))
                {
                    items.Prepare([id]);
                    if(items.Teleports.Get(id) is {} teleport)teleports.Add(id,teleport);
                }
                if(text.Contains("[avatar emblem]",StringComparison.OrdinalIgnoreCase))
                {
                    int color=sockets.EmblemColor(id);
                    if(color!=0)colors.Add(id,color);
                    compound.Prepare([id]);
                    if(compound.Source(id) is {} source)sources.Add(id,source);
                }
                bool book=LocalExperienceBookResources.IsBook(id);
                bool experience=text.Contains("`[expUp",StringComparison.Ordinal);
                bool levelUp=text.Contains("`[level up ticket]`",StringComparison.Ordinal);
                bool capsule=text.Contains("`[jumping lv]`",StringComparison.Ordinal);
                bool ticket=text.Contains(" clear]`",StringComparison.Ordinal)&&LocalQuestTicketResources.IsTicket(text);
                if(!book&&!experience&&!levelUp&&!capsule&&!ticket)continue;
                var template=LocalPvfGiftItemTemplates.Parse(id,false,text,resources.Probe(row.Value).Sha256);
                if(book)exp.Add(id,LocalExperienceBookResources.Parse(template,text));
                else if(experience)exp.Add(id,LocalExperienceItemResources.Parse(template,text));
                else if(levelUp)level.Add(id,LocalLevelTicketResources.Parse(template,text));
                else if(capsule)
                {
                    var parsed=LocalLevel100CapsuleResources.Parse(template,text);
                    if(parsed.UnsupportedReason is null)capsules.Add(id,parsed);
                }
                else if(ticket)
                {
                    var parsed=LocalQuestTicketResources.Parse(id,text,template.ResourceHash);
                    if(parsed.UnsupportedReason is null)questTickets.Add(id,new {Template=template,Definition=new {
                        parsed.Kind,parsed.TemplateId,parsed.ResourceHash,parsed.MinimumLevel,parsed.MaximumLevel,parsed.UsableJobs,
                        parsed.ImpossibleJobs,parsed.UsablePlaces,parsed.CooldownMilliseconds,parsed.Arguments,parsed.NativeAction,
                        UnableUseGrowTypes=parsed.UnableUseGrowTypes.Select(a=>new{a.Job,a.Grow})
                    }});
                }
            }
            catch(InvalidDataException){ /* The pinned parser rejects incomplete or unsupported resource rows. */ }
            catch(FileNotFoundException){ }
        }
        var types=Enumerable.Range(14,10).ToDictionary(slot=>(ushort)slot,slot=>sockets.EquipmentSocketTypes((ushort)slot));
        var roots=new Dictionary<int,Dictionary<int,uint>>();
        var nodes=new Dictionary<uint,LocalEmblemCompoundNode>();
        foreach(int grade in sources.Values.Select(s=>s.Grade).Distinct().Order())
        {
            var counts=new Dictionary<int,uint>();
            for(int count=2;count<=5;count++)
            {
                if(compound.Recipe(grade,count) is not {} recipe)continue;
                counts.Add(count,recipe.Root);
                foreach(var entry in recipe.Nodes)nodes.TryAdd(entry.Key,entry.Value);
            }
            if(counts.Count>0)roots.Add(grade,counts);
        }
        return new {Experience=exp,LevelTickets=level,Capsules=capsules,QuestTickets=questTickets,
            Emblems=new {Colors=colors,SocketTypes=types,Compound=new {Sources=sources,Roots=roots,Nodes=nodes}},
            AvatarEdits=QuestAvatarExport.Read(resources,items,abilitySources,dyes,sources,absolute,avatarConversion),Teleports=teleports,
            PetGrowth=petGrowth.Get(100330838)};
    }
}
