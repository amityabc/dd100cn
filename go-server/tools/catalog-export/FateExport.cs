using Serve100.Game;

internal static class FateExport
{
    internal static object? Read(LocalFateDefinition? f)
    {
        if (f is null) return null;
        LocalFateRules.ValidateDefinition(f);
        return new { f.EventId,f.DungeonId,f.ResourceHash,f.MinimumLevel,f.Town,f.StoryCapacity,
            f.PointLimit,f.SinPointLimit,f.DefaultGrid,f.Blessings,f.Sins,SinCosts=f.Sins.Select(r=>r.Cost).ToArray(),
            f.Collection,f.Rewards,f.Points,f.ShopNodeTypes,f.ShopProducts,f.ShopMapId,f.ShopNpc,
            f.DropWeight,f.NoDropWeight,f.CarrierResetX,f.CarrierResetY,f.GoldenMonsterId,f.GoldenWave,f.GoldenPoints,
            f.BossPositions,f.InstanceBuff,f.BuffMinimumLevel,f.BuffMaximumLevel,
            Layouts=f.Layouts.Values.OrderBy(l=>l.Grid).ThenBy(l=>l.Preset).ToArray(),
            NodeMaps=f.NodeMaps.OrderBy(p=>p.Key.Phase).ThenBy(p=>p.Key.NodeType)
                .Select(p=>new {p.Key.Phase,p.Key.NodeType,Map=p.Value}).ToArray(),
            LoadingDungeon=new {f.LoadingDungeon.Id,f.LoadingDungeon.MinimumLevel,f.LoadingDungeon.MazeIndex,
                f.LoadingDungeon.Start,f.LoadingDungeon.Rooms,f.LoadingDungeon.BossX,f.LoadingDungeon.BossY,
                f.LoadingDungeon.Difficulty,f.LoadingDungeon.ChallengeStage,f.LoadingDungeon.ResourceHash} };
    }
}
