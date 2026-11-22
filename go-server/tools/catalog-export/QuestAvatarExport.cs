using Serve100.Game;
using Serve100.Infrastructure.Resources;

internal static class QuestAvatarExport
{
    public static object Read(PvfResourceProvider resources,LocalPvfItemTemplates items,IEnumerable<uint> abilitySources,
        IEnumerable<uint> dyes,IReadOnlyDictionary<uint,LocalEmblemCompoundSource> emblems,IEnumerable<uint> absolute,
        IReadOnlyList<LocalAvatarConversionDefinition> avatarConversion)
    {
        var abilities=new LocalPvfAvatarAbilityChangeCatalog(resources);
        var dye=new LocalAvatarDyeCatalog(resources);
        var drill=new LocalAvatarDrillCatalog(resources);
        var skins=new LocalAuraSkinCatalog(resources);
        var skinTickets=new Dictionary<uint,LocalItemTemplate>();
        try
        {
            skins.Prepare();
            if(skins.Source is {} ticket)skinTickets.Add(ticket.TemplateId,ticket);
        }
        catch(InvalidDataException){ }
        catch(FileNotFoundException){ }
        foreach(uint id in new uint[]{490020769,490022060,490703887})
        {
            try
            {
                skins.PreparePet(id);
                if(skins.PetSource(id) is {} ticket)skinTickets.Add(id,ticket);
            }
            catch(InvalidDataException){ }
            catch(FileNotFoundException){ }
        }
        PrepareValid(abilitySources.Distinct().ToArray(),ids=>abilities.Prepare(ids));
        PrepareValid(dyes.Distinct().ToArray(),ids=>dye.Prepare(ids));
        drill.Prepare(0,[]);
        var abilityTargets=new Dictionary<uint,LocalAvatarAbilityChangeTarget>();
        var dyeTargets=new Dictionary<uint,LocalAvatarDyeTarget>();
        var drillTargets=new Dictionary<uint,LocalAvatarDrillTarget>();
        var actionParts=new Dictionary<uint,LocalUniqueAvatarActionPart>();
        var disassembly=new LocalAvatarDisassemblyCatalog(resources,new LocalPlayerMailPolicy(resources,items));
        var disassemblyTargets=new Dictionary<uint,object>();
        LocalAvatarDisassemblyDefinition? disassemblyRules=null;
        var avatarIds=resources.ReadCommerceIndex("equipment").Entries
            .Where(r=>r.Key>0&&r.Key<uint.MaxValue&&r.Value.Contains("/avatar/",StringComparison.OrdinalIgnoreCase))
            .Select(r=>(uint)r.Key).Distinct().Order().ToArray();
        foreach(var batch in avatarIds.Chunk(4096))
        {
            var abilityCatalog=new LocalPvfAvatarAbilityChangeCatalog(resources);
            var dyeCatalog=new LocalAvatarDyeCatalog(resources);
            var drillCatalog=new LocalAvatarDrillCatalog(resources);
            var actionCatalog=new LocalUniqueAvatarActionCatalog(resources);
            PrepareValid(batch,ids=>abilityCatalog.Prepare(ids));
            PrepareValid(batch,ids=>dyeCatalog.Prepare(ids));
            foreach(uint id in batch)
            {
                var ability=abilityCatalog.GetTarget(id);
                var d=dyeCatalog.GetTarget(id);
                if(ability is not null)abilityTargets.Add(id,ability);
                if(d is not null)dyeTargets.Add(id,d);
                var grade=ability?.Grade??d?.Grade;
                try
                {
                    disassembly.Prepare(0,id,[]);
                    if(disassembly.Get(id) is {} target)
                    {
                        disassemblyRules??=target;
                        disassemblyTargets.Add(id,new {target.Template.ResourceHash,target.Points});
                    }
                }
                catch(InvalidDataException){ }
                catch(FileNotFoundException){ }
                catch(FormatException){ }
                catch(OverflowException){ }
                if(grade is null or 4)
                {
                    try
                    {
                        actionCatalog.Prepare([id]);
                        if(actionCatalog.Part(id) is {} part)actionParts.Add(id,part);
                    }
                    catch(InvalidDataException){ }
                    catch(FileNotFoundException){ }
                }
                if(grade!=3)continue;
                try
                {
                    drillCatalog.Prepare(0,[id]);
                    if(drillCatalog.GetTarget(id) is {Template:{WornSlot:<=7,InitialAvatarSocketCount:>0}} target)
                        drillTargets.Add(id,target);
                }
                catch(InvalidDataException){ }
                catch(FileNotFoundException){ }
                catch(FormatException){ }
                catch(OverflowException){ }
            }
        }
        var emblemPoints=new Dictionary<uint,int>();
        if(disassemblyRules is {} rules)
        {
            uint sample=rules.Template.TemplateId;
            foreach(var grade in emblems.GroupBy(x=>x.Value.Grade))
            {
                uint representative=grade.First().Key;
                try
                {
                    disassembly.Prepare(0,sample,[representative]);
                    if(disassembly.Get(sample) is {} qualified && qualified.Emblems.TryGetValue(representative,out int points))
                        foreach(var emblem in grade)emblemPoints.Add(emblem.Key,points);
                }
                catch(InvalidDataException){ }
                catch(FileNotFoundException){ }
            }
        }
        return new {
            AbilitySources=abilities.SourceTemplates.ToDictionary(t=>t.TemplateId,t=>abilities.GetSource(t.TemplateId)),
            AbilityTargets=abilityTargets,
            DyeSources=dye.SourceTemplates.ToDictionary(t=>t.TemplateId,t=>dye.GetDye(t.TemplateId)),
            DyeTargets=dyeTargets,DrillSource=drill.GetDrill(7421),DrillTargets=drillTargets,ActionParts=actionParts,
            Disassembly=new {Targets=disassemblyTargets,Emblems=emblemPoints,Tiers=disassemblyRules?.Tiers,Nodes=disassemblyRules?.Nodes},
            Compound=QuestAvatarCompoundExport.Read(resources,items,avatarIds,absolute),SkinTickets=skinTickets,
            Conversions=Conversions(resources,avatarConversion)
        };
    }

    private static object Conversions(PvfResourceProvider resources,IReadOnlyList<LocalAvatarConversionDefinition> definitions)
    {
        var catalog=new LocalAvatarConversionCatalog(resources);
        var targets=new Dictionary<uint,LocalAvatarConversionTarget>();
        foreach(var definition in definitions)
        foreach(var pair in definition.Replacements)
        {
            try
            {
                catalog.Prepare(definition.Source.TemplateId,pair.Key);
                if(catalog.GetTarget(pair.Key) is {} input&&catalog.GetTarget(pair.Value) is {} output)
                {
                    targets.TryAdd(pair.Key,input);
                    targets.TryAdd(pair.Value,output);
                }
            }
            catch(Exception error) when(error is InvalidDataException or FileNotFoundException or FormatException or OverflowException) { }
        }
        return new {Sources=definitions.ToDictionary(d=>d.Source.TemplateId),Targets=targets};
    }

    private static void PrepareValid(uint[] ids,Action<uint[]> prepare)
    {
        if(ids.Length==0)return;
        try { prepare(ids); }
        catch(Exception error) when(error is InvalidDataException or FileNotFoundException)
        {
            if(ids.Length==1)return;
            int half=ids.Length/2;
            PrepareValid(ids[..half],prepare);
            PrepareValid(ids[half..],prepare);
        }
    }
}
