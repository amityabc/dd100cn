package host

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"s9a1/server/internal/duellobby"
	"s9a1/server/internal/inventorywire"
	"s9a1/server/internal/protocol"
	"s9a1/server/internal/questwire"
	"s9a1/server/internal/storage"
)

type questTicketReplay struct {
	request                 questwire.QuestTicketRequest
	character, town, area   uint16
	revision, assetRevision int64
}

func (g *game) questTicket(ctx context.Context, body []byte) error {
	r, err := questwire.ReadQuestTicket(g.seq, body)
	if err != nil {
		return err
	}
	return g.runQuestTicket(ctx, r, 0)
}

func (g *game) previewQuestTicket(ctx context.Context, template uint32) error {
	return g.runQuestTicket(ctx, questwire.QuestTicketRequest{}, template)
}

func (g *game) runQuestTicket(ctx context.Context, req questwire.QuestTicketRequest, preview uint32) error {
	reply := func(ok bool) error {
		if preview == 0 {
			return g.send(1, 507, req.Reply(ok))
		}
		if ok {
			return g.send(1, 1422, []byte{1})
		}
		return g.send(1, 1422, []byte{0, 0, 0})
	}
	duel := duellobby.IsChannel(g.channel)
	if g.opening != nil || g.ordinary.Active() || g.ordinary.Returning() ||
		duel && (g.duelPosition == nil || g.arcade != nil && !g.arcade.Closed) {
		return reply(false)
	}
	phase := storage.TownReady
	if duel {
		phase = storage.Selected
	}
	cat := g.server.Characters
	now := time.Now()
	opts := questwire.TicketContext{SupportsBranch: cat.OpeningCombat.SupportsClassBranch,
		CompleteContext: questwire.CompleteContext{ChangeContext: questwire.ChangeContext{Now: now}, Growth: cat.OpeningCombat.ApplyQuestGrowth,
			FreeSkills: cat.OpeningCombat.QuestFreeSkills, SkillMaster: !duel && g.contracts[1] > now.Unix(), GoldCarryLimits: cat.GoldCarryLimits}}
	var completed []uint16
	out, err := g.server.DB.ChangeQuest(ctx, g.lease, phase, cat.Weights, func(c storage.Character, in storage.Inventory, _ map[string]json.RawMessage) (storage.TownQuestMutation, error) {
		if preview == 0 && g.questTicketReplay == (questTicketReplay{req, c.ID, c.Position.Town, c.Position.Area, in.Revision, in.AssetRevision}) {
			completed = nil
			return storage.TownQuestMutation{}, nil
		}
		m, done, err := cat.Quests.TicketUse(ctx, c, in, req, preview, cat.Towns.IsRoom(point(c.Position)), opts)
		completed = done
		return m, err
	})
	if expectedCommerceError(err) || errors.Is(err, questwire.ErrRewardResource) {
		return reply(false)
	}
	if err != nil {
		return err
	}
	if preview != 0 {
		return reply(true)
	}
	c, err := questActorInventory(out)
	if err != nil {
		return err
	}
	a, _, err := readActor(c, g.channel)
	if err != nil {
		return err
	}
	mode0, err := g.actorMode0(a)
	if err != nil {
		return err
	}
	mode1, err := protocol.Mode1(a, true)
	if err != nil {
		return err
	}
	assets, _, err := inventorywire.Town(out.Inventory, c, inventorywire.Options{Now: now, CashPolicies: cat.CashPolicies, AccountPurchases: out.Inventory.AccountPurchases})
	if err != nil {
		return err
	}
	g.questTicketReplay = questTicketReplay{req, c.ID, c.Position.Town, c.Position.Area, out.Inventory.Revision, out.Inventory.AssetRevision}
	for _, id := range completed {
		delete(g.questAcceptSeen, id)
		delete(g.questConfirm, id)
	}
	if err = reply(true); err != nil {
		return err
	}
	if err = g.send(0, 2, mode0); err != nil {
		return err
	}
	if err = g.send(0, 2, mode1); err != nil {
		return err
	}
	if err = g.sendSkillBuild(c, false, nil); err != nil {
		return err
	}
	if err = g.sendQuestBookOrdered(out, true); err != nil {
		return err
	}
	return g.projected(assets)
}
