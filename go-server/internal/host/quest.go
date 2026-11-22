package host

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"s9a1/server/internal/inventorywire"
	"s9a1/server/internal/protocol"
	"s9a1/server/internal/questwire"
	"s9a1/server/internal/resources"
	"s9a1/server/internal/storage"
)

func (g *game) questArrival(c storage.Character, id uint16) (json.RawMessage, error) {
	state, err := resources.ReadTownQuests(c.Gameplay)
	if err != nil {
		return nil, err
	}
	raw, err := g.server.Characters.Towns.Arrival(c.Gameplay, c.Level, state, point(c.Position))
	if err != nil || len(raw) == 0 {
		return nil, err
	}
	var p struct {
		Quests struct{ Active map[uint16]json.RawMessage }
	}
	if err = json.Unmarshal(raw, &p); err != nil {
		return nil, err
	}
	return p.Quests.Active[id], nil
}

func (g *game) questChange(ctx context.Context, f protocol.Frame) error {
	id, err := questwire.ReadID(f.Opcode, f.Body)
	if err != nil {
		return err
	}
	rejected := []byte{0, 7, 0}
	if g.channel != 19 || g.opening != nil {
		return g.send(1, f.Opcode, rejected)
	}
	var action questwire.Action
	opts := questwire.ChangeContext{Now: time.Now(), Delegated: g.server.delegated, Arrival: g.questArrival}
	out, err := g.server.DB.ChangeTownQuest(ctx, g.lease, g.server.Characters.Weights, func(c storage.Character, in storage.Inventory, receipts map[string]json.RawMessage) (storage.TownQuestMutation, error) {
		var err error
		action, err = g.server.Characters.Quests.Change(c, in, receipts, f.Opcode, id, opts)
		return action.Mutation, err
	})
	if err != nil {
		if errors.Is(err, storage.ErrInvalid) || errors.Is(err, storage.ErrPhase) || errors.Is(err, storage.ErrCapacity) {
			return g.send(1, f.Opcode, rejected)
		}
		return err
	}
	if !action.Full {
		if g.questAcceptSeen[id] {
			return nil
		}
		if g.questAcceptSeen == nil {
			g.questAcceptSeen = make(map[uint16]bool)
		}
		g.questAcceptSeen[id] = true
		return g.send(1, f.Opcode, action.Ack)
	}
	delete(g.questAcceptSeen, id)
	packets, err := g.server.Characters.Quests.Project(out.Character, out.Inventory, out.Receipts)
	if err != nil {
		return err
	}
	var assets []inventorywire.Packet
	if action.AssetProjection {
		assets, _, err = inventorywire.Town(out.Inventory, out.Character, inventorywire.Options{CashPolicies: g.server.Characters.CashPolicies, Now: opts.Now})
		if err != nil {
			return err
		}
		if item := action.Mutation.NewItem; item != nil {
			assets[0], err = inventorywire.NewBagItem(out.Inventory, out.Character, *item)
			if err != nil {
				return err
			}
		}
	}
	if err = g.send(1, f.Opcode, action.Ack); err != nil {
		return err
	}
	for _, p := range packets {
		if err = g.send(p.Class, p.Opcode, p.Body); err != nil {
			return err
		}
	}
	return g.projected(assets)
}
