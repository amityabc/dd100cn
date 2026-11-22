package host

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"s9a1/server/internal/dungeon"
	"s9a1/server/internal/inventorywire"
	"s9a1/server/internal/opening"
	"s9a1/server/internal/storage"
)

func (g *game) claimOculusCard(ctx context.Context, run *dungeon.Run, c storage.Character, paid, automatic bool) error {
	s := &run.Settlement
	reject := func() error { return g.send(1, 71, dungeon.CardOwners(false, s.FreeClaimed, false)) }
	if paid || s.Result == nil || !s.LayoutSent || run.PlayerDeath != nil || !run.Cleared || g.oculus.phase != oculusCleared || !g.oculus.movie {
		return reject()
	}
	plan := s.Result.OculusReward
	if plan == nil {
		return storage.ErrConflict
	}
	resultKey := g.settlementKey(run, "dungeon-settlement") + ":result"
	var saved storage.DungeonLootResult
	if plan.Ordinal != 0 {
		raw, err := json.Marshal(plan)
		if err != nil {
			return err
		}
		input, err := json.Marshal(struct {
			Version, Owner   string
			Epoch            int64
			CharacterId      uint16
			RunId, ResultKey string
			Member           byte
			Reward           *dungeon.OculusCardPlan
		}{"local100-oculus-free-special/v1", g.lease.Owner, g.lease.Epoch, c.ID, run.ID, resultKey, 0, plan})
		if err != nil {
			return err
		}
		saved, err = g.server.DB.ClaimDungeonCard(ctx, g.lease, c, storage.DungeonCardClaim{
			Key: g.settlementKey(run, "dungeon-free-card"), ResultKey: resultKey, Input: string(input), Oculus: raw,
			Now: time.Now(), Create: plan.Grants,
		})
		if errors.Is(err, storage.ErrFunds) || errors.Is(err, storage.ErrCapacity) || errors.Is(err, storage.ErrConflict) {
			if automatic {
				s.FreeTimeoutBlocked = true
				return nil
			}
			return reject()
		}
		if err != nil {
			return err
		}
	} else if err := g.server.DB.ValidateDungeonProof(ctx, g.lease, c, []string{resultKey}); err != nil {
		return err
	}
	first := !s.FreeClaimed
	s.FreeClaimed = true
	frames := []opening.Packet{{Class: 1, Opcode: 71, Body: dungeon.CardOwners(true, true, false)}}
	if plan.Ordinal != 0 {
		delta, err := inventorywire.DungeonBagDelta(saved.Inventory, c, saved.Grants, true)
		if err != nil {
			return err
		}
		bag, err := inventorywire.BagAndWear(saved.Inventory, c)
		if err != nil {
			return err
		}
		frames = append(frames, opening.Packet{Opcode: 14, Body: delta}, opening.Packet{Opcode: bag[0].Opcode, Body: bag[0].Body})
	}
	if err := g.packets(append(frames, opening.Packet{Opcode: 1771, Body: g.oculus.state(5, 1, run.Elapsed)})); err != nil {
		return err
	}
	if saved.Character.ID != 0 {
		c = saved.Character
	}
	var announcement uint32
	if first {
		announcement = run.Definition.Dungeon.ID
	}
	return g.projectSpecialCounters(ctx, c, announcement)
}
