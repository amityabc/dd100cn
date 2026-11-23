package host

import (
	"fmt"
	"slices"
	"time"

	"s9a1/server/internal/dungeon"
	"s9a1/server/internal/storage"
)

func (g *game) oculusMerchant(x *storage.CommerceTx, npc, shop uint32) (*storage.OculusRepairSource, error) {
	o, run := &g.oculus, g.ordinary.Run
	mode, found := g.oculusMode()
	now := time.Now()
	if g.channel != 63 || !found || !o.active || o.phase != oculusRunning || o.parked != nil || o.selected ||
		g.ordinary.Returning() || !now.Before(o.ritualUntil) || run == nil || run.OculusEntry == nil ||
		run.ID != o.id || run.Character != x.Character.ID || o.character != x.Character.ID ||
		run.OculusEntry.SelectionGeneration != o.generation {
		return nil, storage.ErrPhase
	}
	p, err := readGameplay(x.Character.Gameplay)
	if err != nil || !p.Initialized || p.Progression.Hp == 0 {
		return nil, storage.ErrPhase
	}
	if npc == 0 && shop == 0 {
		npc, shop = mode.Rules.ShopNpcId, 100000075
		if mode.Mode == 2 {
			shop = 400000105
		}
	}
	def := run.Definition.Dungeon
	peer := storage.OculusMerchantPeer{Dungeon: def.ID, NPC: npc, Shop: shop}
	if npc == 0 || npc != mode.Rules.ShopNpcId || !slices.Contains(g.catalog().Commerce.OculusPeers, peer) {
		return nil, storage.ErrPhase
	}
	rescue, key, ok := run.OculusRescue(mode)
	if !ok {
		return nil, storage.ErrPhase
	}
	receipts, err := x.OculusSceneReceipts(*run.OculusEntry)
	if err != nil {
		return nil, err
	}
	if _, err := run.OculusHistory(receipts); err != nil {
		return nil, err
	}
	source := storage.OculusRepairSource{RunId: run.ID, Dungeon: def.ID, Mode: mode.Mode, AdmissionHash: mode.SourceHash,
		SceneHash: def.ResourceHash, Generation: run.OculusGeneration, X: run.Room.X, Y: run.Room.Y, Merchant: npc, RescueDeathKey: key}
	for _, reduction := range o.reductions {
		if !slices.ContainsFunc(mode.Rules.KillTimeoverBonuses, func(v dungeon.OculusValue) bool {
			return v.Id == reduction.Monster && v.RawValue == reduction.Percent
		}) {
			return nil, storage.ErrPhase
		}
	}
	if err = x.RequireOculusRescue(*run.OculusEntry, source, mode.Rules.ShopOpenMonsterId, rescue.X, rescue.Y, run.OculusDeathResourceHash(key), o.ritualProof(now, mode)); err != nil {
		return nil, err
	}
	return &source, nil
}

func (g *game) dungeonMerchantScene() string {
	r := g.ordinary.Run
	if r == nil || r.Room == nil {
		return ""
	}
	d := r.Definition.Dungeon
	return fmt.Sprintf("dungeon:%s:%d:%d:%d:%d:%d:%s:%d", r.ID, d.ID, d.MazeIndex, r.Room.X, r.Room.Y, r.Room.MapID, d.ResourceHash, r.OculusGeneration)
}
