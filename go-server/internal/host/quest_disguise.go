package host

import (
	"context"
	"encoding/json"
	"errors"
	"s9a1/server/internal/inventorywire"
	"s9a1/server/internal/protocol"
	"s9a1/server/internal/questwire"
	"s9a1/server/internal/resources"
	"s9a1/server/internal/storage"
	"time"
)

func (g *game) actorMode0(a protocol.Actor) ([]byte, error) {
	if g.commerceInTown() {
		now := time.Now()
		if g.questDisguise.Visible(g.lease, a.ID, uint16(a.Town), uint16(a.Area), now) {
			a.Disguise = &protocol.ActorDisguise{Kind: g.questDisguise.Kind, TemplateID: g.questDisguise.Form}
		}
		if g.server.native140 && g.questAvatarAction.Current(a.ID, uint16(a.Town), uint16(a.Area), a.AvatarActionProof, now) {
			a.UniqueAvatarAction = &protocol.ActorAvatarAction{Action: g.questAvatarAction.Action, Kind: g.questAvatarAction.Kind}
		}
	}
	return protocol.Mode0(a, g.server.native140)
}

func (g *game) disguiseUse(ctx context.Context, body []byte) error {
	req, err := questwire.ReadGrowth(44, g.seq, body)
	if err != nil {
		return err
	}
	reject := func() error { return g.send(1, 44, req.Reply(false, 0, 0, 0)) }
	if g.channel != 19 || g.opening != nil || g.ordinary.Active() || g.ordinary.Returning() {
		return reject()
	}
	cat := g.catalog()
	now := time.Now()
	var next questwire.DisguiseState
	var replay bool
	out, err := g.server.DB.ChangeTownQuest(ctx, g.lease, cat.Weights, func(c storage.Character, in storage.Inventory, _ map[string]json.RawMessage) (storage.TownQuestMutation, error) {
		q, err := resources.ReadTownQuests(c.Gameplay)
		if err != nil {
			return storage.TownQuestMutation{}, err
		}
		if !cat.AllowsTownPosition(c.Level, q, point(c.Position)) {
			return storage.TownQuestMutation{}, storage.ErrInvalid
		}
		m, state, repeated, err := cat.Quests.Disguise.Consume(g.lease, c, in, req, g.questDisguise, now)
		next, replay = state, repeated
		return m, err
	})
	if errors.Is(err, storage.ErrInvalid) || errors.Is(err, storage.ErrPhase) {
		return reject()
	}
	if err != nil {
		return err
	}
	g.questDisguise = next
	bag, err := inventorywire.BagAndWear(out.Inventory, out.Character)
	if err != nil {
		return err
	}
	if err = g.send(1, 44, req.Reply(!replay, 0, 0, 0)); err != nil {
		return err
	}
	return g.send(0, bag[0].Opcode, bag[0].Body)
}

func (g *game) disguiseRequest(ctx context.Context, f protocol.Frame) error {
	cat := g.catalog()
	if f.Opcode == 212 {
		if len(f.Body) != 0 {
			return storage.ErrInvalid
		}
		r, err := g.server.DB.Snapshot(ctx, g.lease)
		if err != nil {
			return err
		}
		if !g.questDisguise.Owns(g.lease, r.SelectedID) {
			return nil
		}
		g.questDisguise.Kind, g.questDisguise.Form, g.questDisguise.UsedAt, g.questDisguise.EndsAt = 0, 0, 0, 0
		if r.Phase != storage.TownReady || g.opening != nil || g.ordinary.Active() || g.ordinary.Returning() {
			return nil
		}
		c, err := selected(r)
		if err != nil {
			return err
		}
		return g.sendDisguiseActor(c)
	}
	req, err := questwire.ReadDisguise(f.Body)
	if err != nil {
		return err
	}
	if g.channel != 19 || g.opening != nil || g.ordinary.Active() || g.ordinary.Returning() {
		return nil
	}
	var next questwire.DisguiseState
	out, err := g.server.DB.ChangeTownQuest(ctx, g.lease, cat.Weights, func(c storage.Character, in storage.Inventory, _ map[string]json.RawMessage) (storage.TownQuestMutation, error) {
		q, err := resources.ReadTownQuests(c.Gameplay)
		if err != nil {
			return storage.TownQuestMutation{}, err
		}
		if !cat.AllowsTownPosition(c.Level, q, point(c.Position)) {
			return storage.TownQuestMutation{}, storage.ErrInvalid
		}
		next, err = cat.Quests.Disguise.Apply(g.lease, c, in, req, g.questDisguise, time.Now())
		return storage.TownQuestMutation{}, err
	})
	if errors.Is(err, storage.ErrInvalid) || errors.Is(err, storage.ErrPhase) {
		return nil
	}
	if err != nil {
		return err
	}
	g.questDisguise = next
	c, err := questActorInventory(out)
	if err != nil {
		return err
	}
	return g.sendDisguiseActor(c)
}

func (g *game) sendDisguiseActor(c storage.Character) error {
	a, _, err := readActor(c, g.channel)
	if err != nil {
		return err
	}
	body, err := g.actorMode0(a)
	if err != nil {
		return err
	}
	return g.send(0, 2, body)
}
