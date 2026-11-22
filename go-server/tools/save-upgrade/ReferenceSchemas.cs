using Microsoft.Data.Sqlite;
using Serve100.Infrastructure.Storage;
using System.Reflection;
using System.Text.Json;

// Developer-only extraction of the append-only schema definitions used by the Go offline importer.
internal static class ReferenceSchemas
{
    private sealed record SchemaObject(string Type,string Table,string SQL);
    private sealed record Delta(Dictionary<string,SchemaObject> Set,string[] Remove);
    private static readonly BindingFlags Private = BindingFlags.NonPublic | BindingFlags.Static;
    internal static void Write(string output)
    {
        var assembly=typeof(SqliteStateStore).Assembly;
        assembly.GetType("Serve100.Infrastructure.Storage.SqliteWindowsLoader",true)!.GetMethod("Register",Private)!.Invoke(null,null);
        SQLitePCL.Batteries_V2.Init();
        var owner=assembly.GetType("Serve100.Infrastructure.Storage.SqliteSchemaMigrations",true)!;
        var reference=owner.GetMethod("Reference",Private)!;
        Dictionary<string,SchemaObject> Snapshot(Dictionary<string,int> versions,string? variant=null)
        {
            using var db=(SqliteConnection)reference.Invoke(null,[versions])!;
            if(variant is not null)
            {
                using var tx=db.BeginTransaction();
                var method=owner.GetMethod(variant,Private)!;
                ((Task)method.Invoke(null,[db,tx,CancellationToken.None])!).GetAwaiter().GetResult();
                tx.Commit();
            }
            using var command=db.CreateCommand();
            command.CommandText="SELECT name,type,tbl_name,sql FROM sqlite_schema WHERE name NOT GLOB 'sqlite_*' AND sql IS NOT NULL ORDER BY name";
            using var rows=command.ExecuteReader();
            var objects=new Dictionary<string,SchemaObject>();
            while(rows.Read()) objects.Add(rows.GetString(0),new(rows.GetString(1),rows.GetString(2),rows.GetString(3)));
            return objects;
        }
        static Delta Diff(Dictionary<string,SchemaObject> older, Dictionary<string,SchemaObject> newer) =>
            new(newer.Where(p=>!older.TryGetValue(p.Key,out var old)||old!=p.Value).ToDictionary(),older.Keys.Except(newer.Keys).ToArray());
        var metadata=Snapshot([]);
        var versions=new List<Delta>();
        var previous=metadata;
        for(int n=1;n<=35;n++)
        {
            var next=Snapshot(new() {["local-entry"]=n});
            versions.Add(Diff(previous,next));
            previous=next;
        }
        var modules=new Dictionary<string,Delta>();
        foreach(var module in new[]{"foundation","counter","adjustment"})
        {
            var baseModules=new Dictionary<string,int>();
            if(module=="adjustment") baseModules.Add("counter",1);
            var before=Snapshot(baseModules);
            baseModules.Add(module,1);
            modules.Add(module,Diff(before,Snapshot(baseModules)));
        }
        var branches=new Dictionary<string,Delta>();
        foreach(var (name,n,method) in new[]{("oculus14",13,"UpgradeOculusBindingsAsync"),("oculus23",22,"UpgradeOculusBindingsAsync"),("crystal26",25,"UpgradeCrystalPremiumAsync"),("crystal27",26,"UpgradeCrystalPremiumAsync")})
        {
            var baseVersion=new Dictionary<string,int>{{"local-entry",n}};
            branches.Add(name,Diff(Snapshot(baseVersion),Snapshot(baseVersion,method)));
        }
        Directory.CreateDirectory(Path.GetDirectoryName(Path.GetFullPath(output))!);
        using var file=File.Create(output);
        JsonSerializer.Serialize(file,new {Metadata=metadata,Versions=versions,Modules=modules,Branches=branches});
    }
}
