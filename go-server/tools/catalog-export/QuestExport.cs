using Serve100.Game;
using Serve100.Infrastructure.Resources;

internal static class QuestExport
{
    public static object Read(LocalQuestCatalog catalog, LocalPvfItemTemplates? items=null, LocalSkillCatalog? skills=null, PvfResourceProvider? resources=null)
    {
        var ordinary = catalog.All.Select(q => new {
            q.Id, q.Hash, q.Kind, q.Subtype, q.Data, q.MinimumLevel, q.MaximumLevel, q.Job, q.Grow,
            q.FateIntroduction, q.LegacyUpperAwakening, q.AdvancementChain, q.AwakeningAward,
            q.Required, Answers=q.Answers.Select(a=>new {a.Quest,a.Answer}), q.Collisions, q.Targets, q.NonTargets,
            q.Parent, q.Children, q.Materials, q.QuestionChoices, q.CommunicatorTarget, q.NpcRangeTarget,
            q.TownCinematicTarget, q.DungeonCinematicTarget, q.Rewards, q.RewardRows, q.SelectionRewardRows, q.DestinationDungeons,
            q.DropRules, q.DestinationRequirements,
            q.GoldAward, q.GoldRequirement, q.TpAward, q.CascadeClear, q.SkillEffects,
            ExperienceByLevel=q.UnsupportedReason is null ? ExperienceByLevel(catalog,q) : null,
            Supported=q.UnsupportedReason is null && q.Kind!=LocalQuestKind.Unsupported,
            AcceptanceSupported=q.UnsupportedReason is null && q.Kind!=LocalQuestKind.Unsupported
                && (q.Kind!=LocalQuestKind.ReachRange || q.Subtype!=0 || LocalQuestTownRange.SupportedNpcObjective(q))
                && (q.Kind!=LocalQuestKind.UseItemAndMeetNpc || LocalCommunicatorRules.Supported(q))
                && (q.Kind!=LocalQuestKind.Question || LocalQuestQuestions.Supported(q)),
            TownHandIn=LocalQuestTownHandInParent.Supported(q,catalog),
            OrderedDungeonParent=LocalQuestsRules.IsOrderedDungeonParent(q,catalog),
            DungeonNpcHandIn=LocalQuestsRules.IsDungeonNpcHandInParent(q,catalog),
            TownPassive=LocalQuestsRules.IsStandalonePassiveObjective(q)||LocalQuestsRules.IsOrderedPassiveFirstChild(q,catalog),
            DungeonPassive=LocalQuestsRules.IsDungeonStandaloneHuntPassive(q)||LocalQuestsRules.IsOrderedPassiveFirstChild(q,catalog),
            MixedDungeonParent=LocalQuestMixedDungeonParents.Supported(q,catalog),
            OutsideDungeonClaim=LocalQuestMixedDungeonParents.AllowsOutsideDungeonClaim(q),
            Operations=new { q.CanAbandon, q.Grade, q.AcceptItems, Available=q.UnsupportedReason is null,
                Daily=LocalDailyQuestRules.Supported(q), DuelTraining=LocalQuestDuelTraining.Supported(q) }
        }).ToArray();
        bool Equipment(LocalQuestTicketQuestDefinition q) => q.EquipmentSlotQuest && q.UnsupportedReason is null
            && q.ProhibitedFlags.IsEmpty && q.Grade=="side" && q.Parent==0 && q.Children.IsEmpty && q.ConditionQuestHide.IsEmpty
            && q.AdvancementChain==0 && q.AwakeningAward is null && q.SkillEffects.IsEmpty && q.RewardType is "item" or "slot expansion";
        var extra = catalog.TicketQuests.OrderBy(q=>q.Id).Select(q=>new {
            q.Id, q.Hash, q.MinimumLevel, q.MaximumLevel, q.Job, q.Grow, q.GrowNumber, q.Parent, q.Children,
            q.Required, Answers=q.Answers.Select(a=>new {a.Quest,a.Answer}), q.Collisions, q.Targets, q.NonTargets,
            q.LegacyUpperAwakening, q.AdvancementChain, q.AwakeningAward, q.QuestionChoices, q.ObjectiveType, q.CascadeClear,
            Equipment=Equipment(q), Supported=q.UnsupportedReason is null, q.ProhibitedFlags, q.Grade,
            q.Name,q.EquipmentSlotQuest,q.RewardType,q.GoldAward,q.Rewards,q.RewardRows,q.TitleRewards,q.SkillEffects,q.ConditionQuestHide
        }).ToArray();
        var ids=catalog.All.SelectMany(q=>q.AcceptItems).Select(row=>row.Template).Distinct().Order().ToArray();
        items?.Prepare(ids);
        var grants=ids.Where(id=>items?.Get(id) is not null).Select(id=>new {
            Template=items!.Get(id), Attributes=new LocalItemAttributes(0,0), Core=new byte[119], Detail=Array.Empty<byte>()
        });
        var rewardIds=catalog.RewardTemplateIds;
        items?.Prepare(rewardIds);
        var rewards=rewardIds.Select(id=>items?.Get(id)).Where(t=>t is not null);
        // The pinned assembly already prepared these exact supported definitions.
        var definitions=skills is null ? new Dictionary<(byte,ushort),LocalSkillDefinition>() :
            (Dictionary<(byte,ushort),LocalSkillDefinition>?)typeof(LocalSkillCatalog)
                .GetField("_definitions",System.Reflection.BindingFlags.Instance|System.Reflection.BindingFlags.NonPublic)?.GetValue(skills)
            ?? throw new InvalidDataException("Pinned prepared skill definitions are unavailable");
        var skillRows=definitions.OrderBy(d=>d.Key).Select(d=>new {Job=d.Key.Item1,Definition=d.Value});
        var initial=definitions.Keys.Select(d=>d.Item1).Distinct().Order().ToDictionary(job=>job,job=>skills!.Initial(job));
        var past=catalog.PastQuests.Select(q=>new {
            q.Id,q.Level,q.Job,q.Grow,q.Awakening,q.Epic,q.Parent,q.Children,q.Required,
            Answers=q.Answers.Select(a=>new{a.Quest,a.Answer}),q.Collisions,q.Targets,q.NonTargets,q.Hash,q.QuestionChoices
        });
        var growth=resources is null||items is null?null:QuestGrowthExport.Read(resources,items);
        var battlePass=resources is null?null:QuestBattlePassExport.Read(resources);
        var disguise=resources is null?null:new LocalDisguiseCatalog(resources);
        disguise?.Prepare();
        return new { Ordinary=ordinary, Tickets=extra, AcceptanceGrants=grants, RewardTemplates=rewards, SkillDefinitions=skillRows, InitialSkills=initial, Past=past, GrowthItems=growth, BattlePass=battlePass, Disguise=disguise?.Definition };
    }

    private static uint?[] ExperienceByLevel(LocalQuestCatalog catalog, LocalQuestDefinition quest)
    {
        var values=new uint?[100];
        for (byte level=1; level<=100; level++)
        {
            try { values[level-1]=catalog.ExperienceReward(quest,level); }
            catch(InvalidDataException) { /* Keep unavailable PVF awards absent. */ }
        }
        return values;
    }
}
