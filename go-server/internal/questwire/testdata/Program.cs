using System.Collections.Immutable;
using System.Security.Cryptography;
using System.Text.Json;
using Serve100.Game;
using Serve100.Host;
using Serve100.Infrastructure.Resources;

if (args.Length != 3) return 2;
using var source=new PvfResourceProvider(args[0],args[1],stableSource:true);
var catalog=new LocalQuestCatalog(source);
catalog.Prepare();
var list=new List<object>();
var stats=new LocalCharacterStats(ImmutableDictionary<string,long>.Empty);
void Capture(byte job,byte level,byte grow,bool progress)
{
    var active=ImmutableDictionary<ushort,LocalActiveQuest>.Empty;
    var cleared=ImmutableDictionary<ushort,byte>.Empty;
    var assets=new LocalAssetsSnapshot(Guid.Empty,1,1,1,[]);
    if(progress)
    {
        void Add(ushort id,uint[] remaining)
        {
            var q=catalog.Get(id) ?? throw new InvalidDataException($"Quest {id} missing");
            active=active.Add(id,new LocalActiveQuest(q.Hash,[..remaining],ImmutableHashSet<string>.Empty));
        }
        Add(156,[1]); Add(674,[1,0,1]);
        cleared=cleared.Add(3145,1).Add(12301,2);
        var material=new LocalItemTemplate(3034,LocalItemKind.Stackable,65,120,ushort.MaxValue,10000,0,1,ushort.MaxValue,new string('a',64));
        assets=assets with{Items=[new LocalItemAsset(Guid.Empty,material,1000,0,80,new(0,0),[..new byte[119]],[])]};
    }
    var gameplay=LocalGameplayProfile.Empty with{Initialized=true,Progression=LocalProgressionState.Empty with{GrowType=grow},Quests=new(active,cleared)};
    var c=new LocalCharacter(1,0,"任务样本",job,level,1,1,stats,38,1,450,234,5,3,""){Gameplay=gameplay};
    var packets=LocalQuestsModule.ProjectionPackets(c,assets,catalog);
    list.Add(new{Job=job,Level=level,Grow=grow,Gameplay=new{gameplay.Initialized,Progression=new{GrowType=grow},gameplay.Quests},Held=assets.Items.Select(a=>new{a.Template.TemplateId,a.Count}),Packets=packets.Select(p=>new{p.Opcode,p.Body})});
}
Capture(0,1,0,false);Capture(1,30,1,false);Capture(2,50,1,false);Capture(9,51,0,false);
Capture(0,75,0x11,true);Capture(0,100,0x21,true);Capture(11,100,0x34,false);Capture(15,100,0x01,false);
using var old=File.OpenRead(typeof(LocalQuestsModule).Assembly.Location);
var output=new{Origin="Original C# LocalQuestsModule and matched PVF, synthetic saved characters",HostSha256=Convert.ToHexString(SHA256.HashData(old)),Cases=list};
File.WriteAllText(args[2],JsonSerializer.Serialize(output));
return 0;
