package host

import (
	"context"
	"time"

	"s9a1/server/internal/dungeon"
	"s9a1/server/internal/inventorywire"
	"s9a1/server/internal/questwire"
	"s9a1/server/internal/storage"
)

type dungeonQuestGrantPlan struct {
	receipt  *questwire.QuestDropReceipt
	grants   []storage.QuestGrant
	revision int64
}

func (g *game) dungeonQuestDrops(ctx context.Context, run *dungeon.Run, c storage.Character, key string, code uint32, kind byte, repeated bool) (dungeonQuestGrantPlan, error) {
	d := run.Definition.Dungeon
	if d.DisableDrops || !d.UsesOrdinaryMonsterDrops || !d.UsesOrdinaryRewards && d.Difficulty == 0 {
		return dungeonQuestGrantPlan{}, nil
	}
	source := questwire.QuestDropSource{EventKey: key, RunID: run.ID, Dungeon: d.ID, Seed: run.Seed,
		Code: code, Kind: kind, Difficulty: d.Difficulty, RepeatedPassive: repeated}
	p, grants, rev, err := g.catalog().Quests.RollQuestDrops(c, source, func() (storage.Inventory, error) {
		return g.server.DB.Inventory(ctx, g.lease)
	})
	return dungeonQuestGrantPlan{p, grants, rev}, err
}

func (p dungeonQuestGrantPlan) attach(change *storage.DungeonChange) {
	if !p.receipt.HasGrants() {
		return
	}
	change.GrantTime = time.Now()
	change.ImmediateGrants = func(in storage.Inventory) ([]storage.QuestGrant, error) {
		if in.AssetRevision != p.revision || len(p.grants) == 0 {
			return nil, storage.ErrConflict
		}
		return p.grants, nil
	}
}

func (g *game) dungeonQuestBag(ctx context.Context, c storage.Character) error {
	in, err := g.server.DB.Inventory(ctx, g.lease)
	if err != nil {
		return err
	}
	frames, err := inventorywire.BagAndWear(in, c)
	if err != nil {
		return err
	}
	return g.send(0, frames[0].Opcode, frames[0].Body)
}
