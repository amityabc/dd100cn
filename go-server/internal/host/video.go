package host

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"s9a1/server/internal/questwire"
	"s9a1/server/internal/storage"
)

// The two native menu variants run the regular meet trigger and hand-in.
// The original publishes the resulting quest book without an opcode-specific ACK.
func (g *game) videoCall(ctx context.Context) error {
	if g.opening != nil || g.ordinary.Active() || g.ordinary.Returning() {
		return nil
	}
	cat := g.server.Characters
	now := time.Now()
	opts := questwire.ChangeContext{Now: now, Lease: g.lease, HasNPC: g.questHasNPC}
	var id uint16
	out, err := g.server.DB.ChangeTownQuest(ctx, g.lease, cat.Weights, func(c storage.Character, in storage.Inventory, r map[string]json.RawMessage) (storage.TownQuestMutation, error) {
		var err error
		id, err = cat.Quests.VideoCall(c)
		if err != nil || id == 0 {
			return storage.TownQuestMutation{}, err
		}
		a, _, err := cat.Quests.Trigger(c, in, r, questwire.TriggerRequest{Id: id}, opts)
		return a.Mutation, err
	})
	if errors.Is(err, storage.ErrPhase) || errors.Is(err, storage.ErrInvalid) {
		return nil
	}
	if err != nil {
		return err
	}
	if id == 0 || !questwire.VideoMet(out.Character, id) {
		return nil
	}
	out, err = g.server.DB.ChangeTownQuest(ctx, g.lease, cat.Weights, func(c storage.Character, in storage.Inventory, r map[string]json.RawMessage) (storage.TownQuestMutation, error) {
		req := questwire.CompleteRequest{Id: id, Selection: 65535, Multiplier: 1, Tail: 65535}
		a, err := cat.Quests.Complete(c, in, r, req, questwire.CompleteContext{ChangeContext: opts, GoldCarryLimits: cat.GoldCarryLimits, Growth: cat.OpeningCombat.ApplyQuestGrowth, FreeSkills: cat.OpeningCombat.QuestFreeSkills, SkillMaster: g.contracts[1] > now.Unix()})
		return a.Mutation, err
	})
	if errors.Is(err, storage.ErrInvalid) || errors.Is(err, storage.ErrPhase) || errors.Is(err, storage.ErrFunds) || errors.Is(err, storage.ErrCapacity) || errors.Is(err, questwire.ErrRewardResource) {
		return nil
	}
	if err != nil {
		return err
	}
	delete(g.questConfirm, id)
	delete(g.questAcceptSeen, id)
	return g.sendQuestBook(out)
}
