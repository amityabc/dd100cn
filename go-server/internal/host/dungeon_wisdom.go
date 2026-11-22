package host

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"s9a1/server/internal/dungeon"
	"s9a1/server/internal/inventorywire"
	"s9a1/server/internal/opening"
	"s9a1/server/internal/protocol"
	"s9a1/server/internal/storage"
)

func wisdomTicketNotice(n uint32) ([]byte, error) {
	return protocol.Announcement(fmt.Sprintf("时间引导石不足，进入智慧的引导需要%d个，请补足材料或使用有效通行证。", n))
}

func wisdomRejected(err error) bool {
	return errors.Is(err, storage.ErrPhase) || errors.Is(err, storage.ErrInvalid) ||
		errors.Is(err, storage.ErrConflict) || errors.Is(err, sql.ErrNoRows)
}

func (g *game) admitWisdom(ctx context.Context, c storage.Character, run *dungeon.Run, now time.Time) (storage.WisdomAdmissionResult, []byte, error) {
	def := run.Definition.Dungeon
	if run.Definition.WisdomReward == nil {
		return storage.WisdomAdmissionResult{}, nil, storage.ErrInvalid
	}
	reward, err := json.Marshal(run.Definition.WisdomReward)
	if err != nil {
		return storage.WisdomAdmissionResult{}, nil, err
	}
	entry := storage.WisdomAdmission{
		Policy: dungeon.WisdomPolicy, Owner: g.lease.Owner, Epoch: g.lease.Epoch, Character: c.ID,
		Run: run.ID, Dungeon: def.ID, ResourceHash: def.ResourceHash, Variant: def.ResourceVariant,
		Difficulty: def.Difficulty, TicketCount: def.EntryTicketCount, Seed: run.Seed, Reward: reward, Passes: def.WisdomPasses,
	}
	var bag []byte
	result, err := g.server.DB.AdmitWisdom(ctx, g.lease, c, entry, now, def.EntryFatigueCost, func(in storage.Inventory) error {
		var err error
		bag, err = inventorywire.OrdinaryBag(in, c)
		return err
	})
	return result, bag, err
}

func (g *game) specialDungeonConfirmation(ctx context.Context, run *dungeon.Run, c storage.Character, body []byte) error {
	actor, owner, err := dungeon.ReadConfirmation(body)
	if err != nil {
		return err
	}
	reject := func() error { return g.send(0, 115, []byte{0, 0}) }
	proof, ok := run.SpecialConfirmationTarget(actor)
	if owner == 0 || owner == ^uint16(0) || owner != c.ID || !ok {
		return reject()
	}
	if err := g.server.DB.ValidateDungeonObject(ctx, g.lease, c, run.ID, run.WisdomEntry, run.RubyEntry, proof); err != nil {
		if wisdomRejected(err) {
			return reject()
		}
		return err
	}
	return g.send(0, 115, dungeon.BossNotice(actor, false))
}

func (g *game) checkWisdomClear(ctx context.Context, run *dungeon.Run, c storage.Character, retry, leave bool) error {
	proof, ok := run.WisdomClearProof()
	if !ok {
		return storage.ErrPhase
	}
	_, err := g.server.DB.WisdomClear(ctx, g.lease, c, *run.WisdomEntry, proof, time.Now(), retry, leave)
	return err
}

func (g *game) wisdomResult(ctx context.Context, run *dungeon.Run, c storage.Character,
	a protocol.Actor, sp, tp uint16, body []byte) error {
	rank, err := dungeon.ReadResult(body)
	if err != nil {
		return err
	}
	if err := g.checkWisdomClear(ctx, run, c, false, false); err != nil {
		return err
	}
	if run.WisdomResultSent {
		return nil
	}
	z := dungeon.ClearResult{ElapsedMilliseconds: run.Elapsed, RankPoint: rank}
	if err := g.packets([]opening.Packet{
		{Opcode: 34, Body: z.PlayNotice(c.ID)},
		{Opcode: 37, Body: dungeon.ExperienceNotice(a, sp, tp)},
		{Opcode: 35, Body: dungeon.NoCardRewardNotice()},
		{Opcode: 36, Body: protocol.Fatigue(a)},
	}); err != nil {
		return err
	}
	run.WisdomResultSent = true
	return nil
}

func (g *game) wisdomCards(ctx context.Context, run *dungeon.Run, c storage.Character, f protocol.Frame, fatigue uint16) error {
	if f.Opcode == 71 {
		b := f.Body
		if !(len(b) == 1 && b[0] == 0 || len(b) == 2 && b[0] <= 1 && b[1] == 0) {
			return storage.ErrInvalid
		}
		return g.send(1, 71, dungeon.CardOwners(false, false, false))
	}
	if len(f.Body) != 0 {
		return storage.ErrInvalid
	}
	if !run.WisdomResultSent {
		return storage.ErrPhase
	}
	if err := g.checkWisdomClear(ctx, run, c, false, false); err != nil {
		return err
	}
	if f.Opcode == 69 {
		return g.send(1, 69, []byte{1})
	}
	return g.send(1, 70, dungeon.CardLayout(fatigue))
}
