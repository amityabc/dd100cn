using Serve100.Game;
using Serve100.Infrastructure.Resources;

internal static class QuestAvatarCompoundExport
{
    public static object Read(PvfResourceProvider resources,LocalPvfItemTemplates items,uint[] avatars,IEnumerable<uint> absoluteIds)
    {
        var compound=new LocalAvatarCompoundCatalog(resources);
        var absolute=new LocalAbsoluteAvatarCatalog(resources,compound);
        var unique=new LocalUniqueAvatarCatalog(resources,compound,new LocalPlayerMailPolicy(resources,items));
        var normalPools=new Dictionary<byte,Dictionary<ushort,LocalAvatarCompoundPool>>();
        var memoryPools=new Dictionary<byte,Dictionary<ushort,LocalAvatarCompoundPool>>();
        var absoluteRules=new Dictionary<byte,Dictionary<uint,LocalAbsoluteAvatarDefinition>>();
        var uniqueRules=new Dictionary<byte,LocalUniqueAvatarDefinition>();
        for(byte job=0;job<16;job++)
        {
            var activeJob=job;
            if(Try(()=>compound.Prepare(activeJob,[])))
            {
                normalPools.Add(job,Pools(job,21));
                if(Try(()=>compound.PrepareMemory(activeJob)))memoryPools.Add(job,Pools(job,2683630));
                var rules=new Dictionary<uint,LocalAbsoluteAvatarDefinition>();
                foreach(uint id in absoluteIds.Distinct())
                    if(Try(()=>absolute.Prepare(activeJob,id,[]))&&absolute.Definition(job,id) is {} rule)rules.Add(id,rule);
                if(rules.Count>0)absoluteRules.Add(job,rules);
                if(Try(()=>unique.Prepare(activeJob,[]))&&unique.Definition(job) is {} uniqueRule)uniqueRules.Add(job,uniqueRule);
            }
        }
        Dictionary<ushort,LocalAvatarCompoundPool> Pools(byte job,uint source)=>Enumerable.Range(0,8)
            .Select(part=>(Part:(ushort)part,Pool:compound.Pool(job,(ushort)part,source)))
            .Where(row=>row.Pool is not null).ToDictionary(row=>row.Part,row=>row.Pool!);
        var sources=new Dictionary<uint,object>();
        foreach(uint id in new uint[]{21,899,2683630,490704046,490709241,10007495})
            if(compound.Source(id) is {} template&&((ILocalAvatarCompoundCatalog)compound).SourceKind(id) is {} kind)
                sources.Add(id,new {Template=template,Kind=kind});
        var inputs=new Dictionary<uint,object>();
        if(normalPools.Count>0)
        {
            byte job=uniqueRules.Count>0?uniqueRules.Keys.First():normalPools.Keys.First();
            foreach(uint id in avatars)
            {
                if(!Try(()=>compound.Prepare(job,[id]))||compound.Input(id) is not {Allowed:true,Grade:>=1 and <=3} input)continue;
                bool clone=input.Grade==3&&uniqueRules.Count>0&&Try(()=>unique.Prepare(job,[id]))&&unique.Clone(id);
                inputs.Add(id,new {input.Template.ResourceHash,input.Template.WornSlot,input.Grade,Clone=clone});
            }
        }
        return new {Sources=sources,Inputs=inputs,NormalPools=normalPools,MemoryPools=memoryPools,Absolute=absoluteRules,Unique=uniqueRules};
    }

    private static bool Try(Action read)
    {
        try {read();return true;}
        catch(Exception e) when(e is InvalidDataException or FileNotFoundException or FormatException or OverflowException) {return false;}
    }
}
