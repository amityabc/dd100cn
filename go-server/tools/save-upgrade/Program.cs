using Serve100.Infrastructure.Storage;

if (args.Length != 1 || !File.Exists(args[0]))
{
    Console.Error.WriteLine("Expected one existing private SQLite snapshot.");
    return 2;
}

try
{
    // The Go importer provides only its backup, after matching the original staged digest.
    var store = new SqliteStateStore(args[0]);
    await store.InitializeLocalEntryAsync(CancellationToken.None);
    return 0;
}
catch (Exception e)
{
    Console.Error.WriteLine(e.Message);
    return 1;
}
