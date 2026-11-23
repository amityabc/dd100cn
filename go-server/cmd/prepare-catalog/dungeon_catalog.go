package main

import (
	"context"
	"fmt"

	"s9a1/server/internal/catalogprep/commerce"
	"s9a1/server/internal/catalogprep/dungeons"
	"s9a1/server/internal/catalogprep/quest"
	"s9a1/server/internal/catalogprep/towns"
	"s9a1/server/internal/dungeon"
	"s9a1/server/internal/pvf"
)

func buildDungeons(ctx context.Context, a *pvf.Archive) (map[string]any, error) {
	items, err := commerce.NewItems(a)
	if err != nil {
		return nil, err
	}
	q, err := quest.Build(a)
	if err != nil {
		return nil, err
	}
	t, err := towns.Build(a)
	if err != nil {
		return nil, err
	}
	xp, err := dungeons.BuildExperience(a)
	if err != nil {
		return nil, err
	}
	boss, err := dungeons.BuildBossEquipment(a)
	if err != nil {
		return nil, err
	}
	return buildDungeonCatalog(ctx, a, items, q, t, xp, boss)
}

func buildDungeonCatalog(ctx context.Context, a *pvf.Archive, items *commerce.Items, q quest.Result, town *towns.Catalog, xp *dungeons.Experience, boss *dungeon.BossEquipmentCatalog) (map[string]any, error) {
	wisdom, err := dungeons.BuildWisdomResources(a, boss)
	if err != nil {
		return nil, err
	}
	layouts, err := dungeons.BuildLayouts(a, xp, q.Data.Ordinary, q.OrdinaryIssues, wisdom)
	if err != nil {
		return nil, err
	}
	layouts.BindEntrances(town.Areas)
	definitions := make([]dungeons.Definition, 0, len(layouts.Definitions))
	for _, l := range layouts.Definitions {
		definitions = append(definitions, l.Dungeon)
	}
	luck, err := dungeons.BuildLuck(a)
	if err != nil {
		return nil, err
	}
	oculus, err := dungeons.BuildOculus(a, xp)
	if err != nil {
		return nil, err
	}
	tournament, err := dungeons.BuildTournamentRewards(a)
	if err != nil {
		return nil, err
	}
	special, err := dungeons.BuildSpecialRewards(a, layouts.PreparedDefinitions)
	if err != nil {
		return nil, err
	}
	world, err := dungeons.BuildWorldDrops(a, items, layouts.PreparedDefinitions, oculus.SourceJSON)
	if err != nil {
		return nil, err
	}
	candidates, err := dungeons.BuildDropCandidates(ctx, a, items)
	if err != nil {
		return nil, fmt.Errorf("drop candidates: %w", err)
	}
	generation, err := dungeons.BuildEquipmentGeneration(a)
	if err != nil {
		return nil, err
	}
	drops, err := dungeons.BuildDrops(a, items, definitions, candidates, generation, luck)
	if err != nil {
		return nil, fmt.Errorf("ordinary drops: %w", err)
	}
	cards, err := dungeons.BuildCards(a, definitions, items, candidates, generation)
	if err != nil {
		return nil, fmt.Errorf("clear cards: %w", err)
	}
	return map[string]any{
		"Entrances": layouts.Entrances, "Definitions": layouts.Definitions, "ExperienceHash": xp.Hash,
		"Luck": luck, "Ruby": layouts.Ruby, "ResourceIssues": layouts.ResourceIssues,
		"BossEquipment": boss, "Wisdom": wisdom, "ClearExperience": xp.Clear, "RankExperience": xp.Rank,
		"Drops": drops, "Cards": cards, "TournamentRewards": tournament, "Oculus": oculus.Modes,
		"OculusCards": oculus.Cards, "WorldDrops": world, "SpecialRewards": special, "OculusStone": oculus.Stone,
	}, nil
}
