package host

import (
	"context"

	"s9a1/server/internal/dungeon"
	"s9a1/server/internal/storage"
)

func (g *game) oculusClearCoins(ctx context.Context, run *dungeon.Run, c storage.Character,
	mode dungeon.OculusMode, monsterReceipts map[string]storage.DungeonSceneReceipt) (storage.OculusClearCoins, error) {
	if run == nil || run.OculusEntry == nil {
		return storage.OculusClearCoins{}, storage.ErrConflict
	}
	ledger, err := g.server.DB.OculusPlayerReceipts(ctx, g.lease, c, *run.OculusEntry)
	if err != nil {
		return storage.OculusClearCoins{}, err
	}
	return run.OculusCoins(mode, monsterReceipts, ledger)
}

func (g *game) currentOculusCoins(ctx context.Context, run *dungeon.Run, c storage.Character) (storage.OculusClearCoins, error) {
	mode, ok := g.oculusMode()
	if !ok || run == nil || run.OculusEntry == nil {
		return storage.OculusClearCoins{}, storage.ErrConflict
	}
	monsters, err := g.server.DB.OculusSceneReceipts(ctx, g.lease, c, *run.OculusEntry)
	if err != nil {
		return storage.OculusClearCoins{}, err
	}
	budget, err := g.oculusClearCoins(ctx, run, c, mode, monsters)
	if err == nil && (budget.Total != g.oculus.coins || budget.Used != g.oculus.used) {
		return storage.OculusClearCoins{}, storage.ErrConflict
	}
	return budget, err
}
