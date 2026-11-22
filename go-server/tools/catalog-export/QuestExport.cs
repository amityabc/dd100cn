using Serve100.Game;
using Serve100.Infrastructure.Resources;

internal static class QuestExport
{
    private static readonly ushort[] DplTargets = [6225,6226,6227,6228,6229,8725,8726,6410,6411,6412,6413];

    public static object Read(LocalQuestCatalog catalog, LocalPvfItemTemplates? items=null)
    {
        var ordinary = catalog.All.Select(q => new {
            q.Id, q.Hash, q.Kind, q.Subtype, q.Data, q.MinimumLevel, q.MaximumLevel, q.Job, q.Grow,
            q.FateIntroduction, q.LegacyUpperAwakening, q.AdvancementChain, q.AwakeningAward,
            q.Required, Answers=q.Answers.Select(a=>new {a.Quest,a.Answer}), q.Collisions, q.Targets, q.NonTargets,
            q.Parent, q.Children, q.Materials, q.QuestionChoices, q.CommunicatorTarget, q.NpcRangeTarget,
            q.TownCinematicTarget, q.DungeonCinematicTarget, q.Rewards, q.RewardRows, q.DestinationDungeons,
            Supported=q.UnsupportedReason is null && q.Kind!=LocalQuestKind.Unsupported,
            AcceptanceSupported=q.UnsupportedReason is null && q.Kind!=LocalQuestKind.Unsupported
                && (q.Kind!=LocalQuestKind.ReachRange || q.Subtype!=0 || LocalQuestTownRange.SupportedNpcObjective(q))
                && (q.Kind!=LocalQuestKind.UseItemAndMeetNpc || LocalCommunicatorRules.Supported(q))
                && (q.Kind!=LocalQuestKind.Question || LocalQuestQuestions.Supported(q)),
            TownHandIn=LocalQuestTownHandInParent.Supported(q,catalog),
            OrderedDungeonParent=LocalQuestsRules.IsOrderedDungeonParent(q,catalog),
            DungeonNpcHandIn=LocalQuestsRules.IsDungeonNpcHandInParent(q,catalog),
            OutsideDungeonClaim=LocalQuestMixedDungeonParents.AllowsOutsideDungeonClaim(q),
            Operations=new { q.CanAbandon, q.Grade, q.AcceptItems, Available=q.UnsupportedReason is null,
                Daily=LocalDailyQuestRules.Supported(q), DuelTraining=LocalQuestDuelTraining.Supported(q) }
        }).ToArray();
        var tickets = catalog.TicketQuests.ToDictionary(q=>q.Id);
        var scope = new HashSet<ushort>(DplTargets);
        var queue = new Queue<ushort>(scope);
        while(queue.TryDequeue(out ushort id))
        {
            if (!tickets.TryGetValue(id,out var target)) continue;
            foreach(ushort child in target.Children) if(scope.Add(child)) queue.Enqueue(child);
        }
        var used = new HashSet<ushort>(scope);
        foreach(ushort id in scope)
            if (tickets.TryGetValue(id,out var q))
            {
                if(q.Parent!=0) used.Add(q.Parent);
                foreach(var group in q.Required) foreach(ushort needed in group) used.Add(needed);
            }
        bool Equipment(LocalQuestTicketQuestDefinition q) => q.EquipmentSlotQuest && q.UnsupportedReason is null
            && q.ProhibitedFlags.IsEmpty && q.Grade=="side" && q.Parent==0 && q.Children.IsEmpty && q.ConditionQuestHide.IsEmpty
            && q.AdvancementChain==0 && q.AwakeningAward is null && q.SkillEffects.IsEmpty && q.RewardType is "item" or "slot expansion";
        var extra = tickets.Values.Where(q=>used.Contains(q.Id)||Equipment(q)).OrderBy(q=>q.Id).Select(q=>new {
            q.Id, q.MinimumLevel, q.MaximumLevel, q.Job, q.Grow, q.GrowNumber, q.Parent, q.Children,
            q.Required, Answers=q.Answers.Select(a=>new {a.Quest,a.Answer}), q.Collisions, q.Targets, q.NonTargets,
            q.LegacyUpperAwakening, q.AdvancementChain, q.AwakeningAward, q.QuestionChoices, q.ObjectiveType, q.CascadeClear,
            Equipment=Equipment(q), Supported=q.UnsupportedReason is null, q.ProhibitedFlags, q.Grade
        }).ToArray();
        var ids=catalog.All.SelectMany(q=>q.AcceptItems).Select(row=>row.Template).Distinct().Order().ToArray();
        items?.Prepare(ids);
        var grants=ids.Where(id=>items?.Get(id) is not null).Select(id=>new {
            Template=items!.Get(id), Attributes=new LocalItemAttributes(0,0), Core=new byte[119], Detail=Array.Empty<byte>()
        });
        return new { Ordinary=ordinary, Tickets=extra, AcceptanceGrants=grants };
    }
}
