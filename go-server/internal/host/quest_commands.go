package host

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"time"

	"s9a1/server/internal/duellobby"
	"s9a1/server/internal/inventorywire"
	"s9a1/server/internal/protocol"
	"s9a1/server/internal/questwire"
	"s9a1/server/internal/storage"
)

func (g *game) questPast(ctx context.Context, f protocol.Frame) error {
	preview := f.Opcode == 1422
	if preview && len(f.Body) != 4 || !preview && len(f.Body) != 0 {
		return storage.ErrInvalid
	}
	if preview {
		if template := binary.LittleEndian.Uint32(f.Body); template != 0 {
			return g.previewQuestTicket(ctx, template)
		}
	}
	reject := func() error {
		if preview {
			return g.send(1, 1422, []byte{0, 7, 0})
		}
		return nil
	}
	if g.channel != 19 || g.opening != nil || g.ordinary.Active() || g.ordinary.Returning() {
		return reject()
	}
	var removed []uint16
	cat := g.server.Characters
	out, err := g.server.DB.ChangeTownQuest(ctx, g.lease, cat.Weights, func(c storage.Character, _ storage.Inventory, _ map[string]json.RawMessage) (storage.TownQuestMutation, error) {
		m, ids, err := cat.Quests.Past(ctx, c, preview)
		removed = ids
		return m, err
	})
	if errors.Is(err, storage.ErrInvalid) || errors.Is(err, storage.ErrPhase) {
		return reject()
	}
	if err != nil {
		return err
	}
	if preview {
		return g.send(1, 1422, []byte{1})
	}
	for _, id := range removed {
		delete(g.questAcceptSeen, id)
		delete(g.questConfirm, id)
	}
	packets, err := cat.Quests.Project(out.Character, out.Inventory, out.Receipts)
	if err != nil {
		return err
	}
	for _, p := range packets {
		if p.Opcode == 356 {
			if err = g.send(p.Class, p.Opcode, p.Body); err != nil {
				return err
			}
		}
	}
	for _, p := range packets {
		if p.Opcode != 356 {
			if err = g.send(p.Class, p.Opcode, p.Body); err != nil {
				return err
			}
		}
	}
	return nil
}

func (g *game) questTrainingManual(ctx context.Context, f protocol.Frame) error {
	if len(f.Body) != 0 {
		return storage.ErrInvalid
	}
	phase, ok := g.questInteractionPhase(2674)
	if !ok || phase != storage.Selected {
		return nil
	}
	cat := g.server.Characters
	now := time.Now()
	out, err := g.server.DB.ChangeQuest(ctx, g.lease, phase, cat.Weights, func(c storage.Character, in storage.Inventory, rs map[string]json.RawMessage) (storage.TownQuestMutation, error) {
		return cat.Quests.TrainingManual(c, in, rs, now, func(d questwire.DuelObjective) bool {
			return g.arcade.Qualifies(g.lease, c.ID, duellobby.TrainingStage{ID: d.ID, Hash: d.Hash, Acceptance: d.Acceptance, Stage: 2})
		})
	})
	if errors.Is(err, storage.ErrInvalid) || errors.Is(err, storage.ErrPhase) || errors.Is(err, storage.ErrCapacity) {
		return nil
	}
	if err != nil {
		return err
	}
	assets, _, err := inventorywire.Town(out.Inventory, out.Character, inventorywire.Options{Now: now, CashPolicies: cat.CashPolicies})
	if err != nil {
		return err
	}
	if err = g.projected(assets); err != nil {
		return err
	}
	return g.sendQuestBook(out)
}

func (g *game) autoCompleteDeliveryChildren(ctx context.Context) error {
	if g.channel != 19 || g.opening != nil || g.ordinary.Active() || g.ordinary.Returning() {
		return nil
	}
	cat := g.server.Characters
	now := time.Now()
	opts := questwire.CompleteContext{ChangeContext: questwire.ChangeContext{Now: now, Lease: g.lease, HasNPC: g.questHasNPC},
		GoldCarryLimits: cat.GoldCarryLimits, Growth: cat.OpeningCombat.ApplyQuestGrowth, FreeSkills: cat.OpeningCombat.QuestFreeSkills, SkillMaster: g.contracts[1] > now.Unix()}
	var parent, id uint16
	for pass := 0; pass < 2; pass++ {
		var action questwire.CompleteAction
		out, err := g.server.DB.ChangeTownQuest(ctx, g.lease, cat.Weights, func(c storage.Character, in storage.Inventory, rs map[string]json.RawMessage) (storage.TownQuestMutation, error) {
			if pass == 0 {
				child, main, err := cat.Quests.SatisfiedDelivery(c, in, rs)
				if err != nil || child == 0 {
					return storage.TownQuestMutation{}, err
				}
				id, parent = child, main
			} else {
				id = parent
			}
			var err error
			action, err = cat.Quests.Complete(c, in, rs, questwire.CompleteRequest{Id: id, Selection: 65535, Multiplier: 1, Tail: 65535}, opts)
			return action.Mutation, err
		})
		if errors.Is(err, storage.ErrInvalid) || errors.Is(err, storage.ErrPhase) || errors.Is(err, storage.ErrFunds) || errors.Is(err, storage.ErrCapacity) || errors.Is(err, questwire.ErrRewardResource) {
			return nil
		}
		if err != nil {
			return err
		}
		if len(action.Ack) == 0 {
			return nil
		}
		delete(g.questAcceptSeen, id)
		delete(g.questConfirm, id)
		if err = g.send(1, 34, action.Ack); err != nil {
			return err
		}
		if err = g.sendQuestBook(out); err != nil {
			return err
		}
		assets, _, err := inventorywire.Town(out.Inventory, out.Character, inventorywire.Options{Now: now, CashPolicies: cat.CashPolicies})
		if err != nil {
			return err
		}
		if err = g.projected(assets); err != nil {
			return err
		}
	}
	return nil
}
