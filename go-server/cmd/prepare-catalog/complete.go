package main

import (
	"context"
	"fmt"
	"slices"

	"s9a1/server/internal/catalogprep"
	"s9a1/server/internal/catalogprep/arcade"
	"s9a1/server/internal/catalogprep/commerce"
	"s9a1/server/internal/catalogprep/dungeons"
	"s9a1/server/internal/catalogprep/fate"
	"s9a1/server/internal/catalogprep/gm"
	"s9a1/server/internal/catalogprep/quest"
	"s9a1/server/internal/catalogprep/towns"
	"s9a1/server/internal/pvf"
	"s9a1/server/internal/questwire"
)

// Schema 4 names the upstream rule assemblies that defined these catalogs.
// Their frozen identities allow the native compiler to preserve that format.
const ruleInfrastructureSHA256 = "A6E5F57DECFD74829364250867826BCFD14632ED57689A72E0AC9136720D7124"
const ruleGameSHA256 = "D1764CA63D9845D70E6B676B6F77EFECBF579AEF635E05A3471EE8BAE95F81D8"

func buildAll(ctx context.Context, a *pvf.Archive, identity gm.Metadata) (map[string]any, error) {
	items, err := commerce.NewItems(a)
	if err != nil {
		return nil, err
	}
	q, err := quest.Build(a)
	if err != nil {
		return nil, err
	}
	book, err := questwire.New(q.Data)
	if err != nil {
		return nil, err
	}
	jobs, err := catalogprep.CharacterGrowthTables(a)
	if err != nil {
		return nil, err
	}
	skills, err := catalogprep.Skills(a)
	if err != nil {
		return nil, err
	}
	levels, replacement, err := catalogprep.Progression(a)
	if err != nil {
		return nil, err
	}
	tutorials, err := dungeons.BuildOpenings(a)
	if err != nil {
		return nil, err
	}
	xp, err := dungeons.BuildExperience(a)
	if err != nil {
		return nil, err
	}
	characters, err := catalogprep.CharacterTemplates(jobs, skills, levels, tutorials, book)
	if err != nil {
		return nil, err
	}
	combat, err := catalogprep.OpeningCombatTables(items, levels, replacement, jobs, skills, tutorials, xp)
	if err != nil {
		return nil, err
	}
	town, err := towns.Build(a)
	if err != nil {
		return nil, err
	}
	town.Ranges = q.Ranges
	daily, err := catalogprep.DailyLimits(a)
	if err != nil {
		return nil, err
	}
	quests, err := catalogprep.PrepareQuestTables(a, items, &q, skills)
	if err != nil {
		return nil, fmt.Errorf("quest resources: %w", err)
	}
	arc, err := arcade.Build(a)
	if err != nil {
		return nil, err
	}
	event, err := fate.Build(a)
	if err != nil {
		return nil, err
	}
	if err = ctx.Err(); err != nil {
		return nil, err
	}
	fmt.Println("Prepared characters, towns, quests and activities")
	bossItems := make(map[uint32]*gm.ItemTemplate)
	boss, err := dungeons.PrepareBossEquipment(a, bossItems)
	if err != nil {
		return nil, err
	}
	dungeonCatalog, err := buildDungeonCatalog(ctx, a, items, q, town, xp, boss)
	if err != nil {
		return nil, fmt.Errorf("dungeon resources: %w", err)
	}
	fmt.Println("Prepared dungeon layouts, drops and cards")
	if err = ctx.Err(); err != nil {
		return nil, err
	}
	myth, err := dungeons.BuildMythologyResolver(a)
	if err != nil {
		return nil, err
	}
	opt := commerce.Options{
		QuestTemplateIDs: slices.Concat(q.RewardTemplateIDs, q.MaterialTemplateIDs), Fate: event,
		Mythology: myth, AdditionalPrepared: bossItems, QuestTicket: quest.GiftQuestTicket,
		GrowthPreparedTemplateIDs: quests.GrowthItems.PreparedTemplateIDs,
	}
	if pass := quests.BattlePass; pass != nil {
		d := pass.Definition
		opt.BattlePass = &commerce.BattlePassCashSource{ResourceHash: d.ResourceHash, Certificate: d.Certificate, Package: d.Package, BuyCode: d.BuyCode}
		opt.BattlePassRewards = pass.Templates
	}
	trade, err := commerce.Build(items, town, opt)
	if err != nil {
		return nil, fmt.Errorf("commerce resources: %w", err)
	}
	return map[string]any{
		"Schema": 4, "PvfSha256": identity.PvfSha256, "SidecarSha256": identity.SidecarSha256, "ProfileSha256": identity.ProfileSha256,
		"InfrastructureSha256": ruleInfrastructureSHA256, "GameSha256": ruleGameSHA256,
		"Characters": characters, "Towns": town, "Openings": tutorials, "DailyLimits": daily,
		"Quests": quests, "OpeningCombat": combat, "Arcade": arc, "Fate": event, "Dungeons": dungeonCatalog,
		"Weights": trade.Weights, "CashPurchasePolicies": trade.CashPurchasePolicies, "CharacterCashPurchasePolicies": trade.CharacterCashPurchasePolicies,
		"Rename": trade.Rename, "Commerce": trade.Commerce, "GoldCarryLimits": trade.GoldCarryLimits, "MailAttachments": trade.MailAttachments,
	}, nil
}
