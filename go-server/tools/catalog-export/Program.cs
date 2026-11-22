using System.Security.Cryptography;
using System.Text.Json;
using Serve100.Game;
using Serve100.Infrastructure.Resources;

if (args is ["--routes", var isolatedRoot, var legacyOutput, var routeFile])
    return await RouteExport.Run(isolatedRoot, legacyOutput, routeFile);

if (args.Length != 3)
{
    Console.Error.WriteLine("Usage: CatalogExport <Script.pvf> <Client100Profile.json> <output-file>");
    return 2;
}

var pvf = Path.GetFullPath(args[0]);
var profile = Path.GetFullPath(args[1]);
var destination = Path.GetFullPath(args[2]);
var sidecar = Path.Combine(Path.GetDirectoryName(pvf)!, "sk.dat");
var sources = new[] { pvf, sidecar, profile, typeof(PvfResourceProvider).Assembly.Location, typeof(LocalGameplayCharacterTemplates).Assembly.Location };
// Keep the actual inputs stable between hashing and exporting. The output contains only catalog data and identities.
using var pvfHandle = File.OpenRead(pvf);
using var sidecarHandle = File.OpenRead(sidecar);
using var profileHandle = File.OpenRead(profile);
var hashes = sources.Select(path =>
{
    using var input = File.OpenRead(path);
    return Convert.ToHexString(SHA256.HashData(input));
}).ToArray();
using var resources = new PvfResourceProvider(pvf, profile, stableSource: true);
var characters = new LocalCharacterTemplates(resources);
characters.Prepare();
var jobs = Enumerable.Range(0, 16).Select(i => (byte)i).Where(job => characters.Get(job) != null).ToArray();
var skills = new LocalSkillCatalog(resources);
var progression = new LocalProgressionCatalog(resources);
var tutorials = new LocalTutorialCatalog(resources);
var quests = new LocalQuestCatalog(resources);
skills.Prepare(jobs);
progression.Prepare(jobs);
tutorials.Prepare();
quests.Prepare();
var knownQuests = quests.All;
// FatigueDay is replaced with the creation date by Go, using the original UTC+8 06:00 boundary.
var templates = new LocalGameplayCharacterTemplates(characters, skills, progression, () => DateTimeOffset.UnixEpoch, tutorials);
var records = new List<object>();
foreach (var job in jobs)
{
    for (byte growType = 0; growType <= 5; growType++)
    {
        var template = templates.Get(job, growType);
        if (template is null) continue;
        var fresh = new LocalCharacter(1,0,"资源样本",job,1,1,0,template.Stats,38,0,1677,222,5,0,template.SourceHash) { Gameplay=template.Gameplay };
        ushort[] Available(LocalCharacter c) => knownQuests.Where(q=>LocalQuestsRules.AcceptanceIssue(c,q,quests) is null).Select(q=>q.Id)
            .Concat(LocalQuestTicketRules.EquipmentSlotCandidates(c,quests)).Concat(LocalQuestTicketRules.DplCandidates(c,quests))
            .Distinct().Order().ToArray();
        records.Add(new { Job=job,GrowType=growType,template.SourceHash,template.Stats,template.Gameplay,
            OutdoorQuests=Available(fresh),RoomQuests=Available(fresh with{Area=1}) });
    }
}
var commerce = new LocalCommerceCatalog(resources);
commerce.Prepare();
var fate = new LocalFateCatalog(resources);
fate.Prepare();
var items = new LocalPvfItemTemplates(resources);
var weights = new LocalPlayerMailPolicy(resources,items);
var ids = commerce.TemplateIds.Concat(quests.RewardTemplateIds).Concat(quests.MaterialTemplateIds).Distinct().Order().ToArray();
weights.Prepare(ids);
var weightMap = new SortedDictionary<uint,long>();
foreach(var id in ids) {
    try { weightMap.Add(id,weights.ItemWeight(id)); }
    catch (InvalidDataException) { /* Non-inventory resources have no prepared carrying weight. */ }
    catch (FileNotFoundException) { }
}
var openings = jobs.Select(job=>tutorials.Get(job)).Where(t=>t is not null).Select(t=>new {
    t!.Job,t.Hash,t.ReturnPoint, Dungeon=new {t.Dungeon.Id,t.Dungeon.MinimumLevel,t.Dungeon.MazeIndex,
        t.Dungeon.BossX,t.Dungeon.BossY,t.Dungeon.Start,t.Dungeon.Rooms,t.Dungeon.Difficulty,t.Dungeon.ChallengeStage,
        t.Dungeon.EntryFatigueCost,t.Dungeon.RoomFatigueCost,t.Dungeon.ResourceHash}
}).ToArray();
var limits = new SortedDictionary<uint,byte>();
string rewardText=resources.ReadText("etc/dungeonspecialreward.etc",1024*1024);
var parser = typeof(PvfResourceProvider).Assembly.GetType("Serve100.Infrastructure.Resources.LocalGameplayResourceText", true)!;
var blocks = (string[])parser.GetMethod("Blocks")!.Invoke(null,[rewardText,"dungeon"])!;
foreach(var block in blocks){
    int[] numbers=System.Text.RegularExpressions.Regex.Matches(block,@"-?\d+").Select(m=>int.Parse(m.Value,System.Globalization.CultureInfo.InvariantCulture)).ToArray();
    if(numbers.Length<4||numbers[0]<0||!LocalDungeonSpecialRewardRules.Supported((uint)numbers[0]))continue;
    uint dungeon=(uint)numbers[0];
    if(LocalDungeonSpecialRewardRules.CounterDungeon(dungeon)!=dungeon)continue;
    int aliases=dungeon==LocalDungeonSpecialRewardRules.DarkTemple?2:0;
    if(numbers[1]!=aliases||numbers.Length<4+aliases||numbers[2+aliases] is <1 or >32||numbers[3+aliases]!=0)throw new InvalidDataException("Unsupported daily counter");
    limits.Add(dungeon,(byte)numbers[2+aliases]);
}
var catalog = new { Schema = 4, PvfSha256 = hashes[0], SidecarSha256 = hashes[1], ProfileSha256 = hashes[2], InfrastructureSha256 = hashes[3], GameSha256 = hashes[4], Characters = records, Towns = TownExport.Read(resources,quests), Openings=openings, DailyLimits=limits,
    commerce.CashPurchasePolicies, commerce.CharacterCashPurchasePolicies, Weights=weightMap,
    Quests=QuestExport.Read(quests,items), OpeningCombat=OpeningExport.Read(resources,progression,tutorials,skills),
    Fate = fate.Definition is {} f ? new {f.StoryCapacity, f.MinimumLevel, f.Town, SinCosts=f.Sins.Select(s=>s.Cost).ToArray()} : null };
Directory.CreateDirectory(Path.GetDirectoryName(destination)!);
var pending = destination + "." + Guid.NewGuid().ToString("N") + ".tmp";
try
{
    using (var stream = File.Open(pending, FileMode.CreateNew, FileAccess.Write, FileShare.None)) JsonSerializer.Serialize(stream, catalog);
    File.Move(pending, destination, true);
}
finally { if (File.Exists(pending)) File.Delete(pending); }
Console.WriteLine($"Exported {records.Count} creation templates for {jobs.Length} jobs.");
return 0;
