using System.IO.Compression;
using System.Security.Cryptography;
using System.Text.Json;
using Serve100.Game;
using Serve100.Infrastructure.Resources;

internal static class GmExport
{
    private sealed record Row(LocalItemTemplate? Template, string? Error,long? Weight,
        LocalGiftSpecialProduct? Special);

    public static int Run(string pvf, string profile, string output)
    {
        pvf = Path.GetFullPath(pvf);
        profile = Path.GetFullPath(profile);
        output = Path.GetFullPath(output);
        Directory.CreateDirectory(Path.GetDirectoryName(output)!);
        using var source = File.OpenRead(pvf);
        using var sidecar = File.OpenRead(Path.Combine(Path.GetDirectoryName(pvf)!, "sk.dat"));
        using var profileFile = File.OpenRead(profile);
        using var resources = new PvfResourceProvider(pvf, profile, stableSource: true);
        var ids = new[] { "equipment", "stackable" }.SelectMany(kind => resources.ReadCommerceIndex(kind).Entries.Keys)
            .Where(id => id >= 0).Distinct().Order().ToArray();
        bool unprofiled = false;
        try
        {
            using var plain = new PvfResourceProvider(pvf);
            unprofiled = plain.ReadCommerceIndex("equipment").Entries.Count > 0;
        }
        catch (Exception error) when (error is InvalidOperationException or InvalidDataException or IOException) { }
        if (ids.Length is 0 or > 600000) throw new InvalidDataException("GM item index exceeds its resource capacity");
        string stage = output + "." + Guid.NewGuid().ToString("N") + ".tmp";
        string indexPath = stage + ".index";
        void Json(ZipArchive archive, string path, object value)
        {
            using var stream = archive.CreateEntry(path, CompressionLevel.Fastest).Open();
            JsonSerializer.Serialize(stream, value);
        }
        try
        {
            using (var archive = ZipFile.Open(stage, ZipArchiveMode.Create))
            {
                Json(archive, "manifest.json", new { Schema = 1, Count = ids.Length, Parser=typeof(PvfResourceProvider).Module.ModuleVersionId, Unprofiled=unprofiled,
                    PvfSha256 = Convert.ToHexString(SHA256.HashData(source)),
                    SidecarSha256 = Convert.ToHexString(SHA256.HashData(sidecar)),
                    ProfileSha256 = Convert.ToHexString(SHA256.HashData(profileFile)) });
                GmItemCatalog.LoadAsync(pvf, profile, indexPath).GetAwaiter().GetResult();
                archive.CreateEntryFromFile(indexPath, "item-index.json", CompressionLevel.Fastest);
                var allWeights=new SortedDictionary<uint,long>();
                var avatarClones=new SortedDictionary<uint,LocalAvatarCloneTemplate>();
                foreach (var group in ids.GroupBy(id => id & 255).OrderBy(g => g.Key))
                {
                    var rows = new SortedDictionary<uint, Row>();
                    var items = new LocalPvfItemTemplates(resources);
                    var specialItems=new LocalCommerceCatalog(resources);
                    var clones=new LocalAvatarCloneCatalog(resources);
                    var weights=new LocalPlayerMailPolicy(resources,items);
                    foreach (uint id in group.Select(x => (uint)x))
                    {
                        try
                        {
                            items.PrepareGm(id);
                            var item = items.GetGm(id);
                            LocalGiftSpecialProduct? special=null;
                            if(item is null)
                            {
                                specialItems.PrepareGmSpecialItem(id);
                                special=specialItems.FindGmSpecialItem(id);
                                if(special?.Template is {Kind:LocalSpecialItemKind.Avatar,WornSlot:<=7})
                                {
                                    try
                                    {
                                        clones.Prepare([id]);
                                        if(clones.Get(id) is {} clone)avatarClones.Add(id,clone);
                                    }
                                    catch(InvalidDataException) { }
                                }
                            }
                            long? weight=null;
                            if(item is not null)try
                            {
                                weights.Prepare([id]);
                                weight=weights.ItemWeight(id);
                                allWeights.Add(id,weight.Value);
                            }
                            catch(Exception ex) when(ex is InvalidDataException or FileNotFoundException) { }
                            rows.Add(id, new(item, item is not null && !LocalItemRules.ValidTemplate(item)
                                ? "Prepared PVF item template is invalid" : null,weight,special));
                        }
                        catch (Exception ex) when (ex is InvalidDataException or FileNotFoundException)
                        { rows.Add(id, new(null, ex.Message,null,null)); }
                    }
                    Json(archive, $"{group.Key:x2}.json", rows);
                }
                Json(archive,"weights.json",allWeights);
                Json(archive,"avatar-clones.json",avatarClones);
            }
            File.Move(stage, output, true);
        }
        finally
        {
            foreach (var path in new[] { stage, indexPath, indexPath + ".lock" })
                if (File.Exists(path)) File.Delete(path);
        }
        Console.WriteLine($"Exported {ids.Length} GM item definitions and resource decisions.");
        return 0;
    }
}
