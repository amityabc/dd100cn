package host

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"s9a1/server/internal/duellobby"
	"s9a1/server/internal/dungeon"
	"s9a1/server/internal/inventorywire"
	"s9a1/server/internal/opening"
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
	raw, err := g.catalog().Towns.Arrival(c.Gameplay, c.Level, state, point(c.Position))
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
	phase := storage.TownReady
	ranked := g.channel == 252 && g.duelPosition != nil && g.duelPosition.Town == 10 && (g.duelPosition.Area == 0 || g.duelPosition.Area == 4)
	training := id >= 2672 && id <= 2674
	dialogue := id == 2670 || id == 2671 || id == 2675
	if ranked && (training && (f.Opcode == 31 || g.duelPosition.Area == 4) || dialogue && f.Opcode == 31 && (g.arcade == nil || g.arcade.Closed)) {
		phase = storage.Selected
	}
	if (g.channel != 19 && phase != storage.Selected) || g.opening != nil || g.ordinary.Returning() || g.ordinary.Active() && g.questDungeon() == nil {
		return g.send(1, f.Opcode, rejected)
	}
	var action questwire.Action
	opts := questwire.ChangeContext{Now: time.Now(), Delegated: g.server.delegated, Arrival: g.questArrival, RankedDuel: phase == storage.Selected, Dungeon: g.questDungeon(), Lease: g.lease}
	out, err := g.server.DB.ChangeQuest(ctx, g.lease, phase, g.catalog().Weights, func(c storage.Character, in storage.Inventory, receipts map[string]json.RawMessage) (storage.TownQuestMutation, error) {
		if phase == storage.Selected && (c.Level < 50 || training && !g.arcade.CanCompleteQuest(g.lease, c.ID)) {
			return storage.TownQuestMutation{}, storage.ErrInvalid
		}
		var err error
		action, err = g.catalog().Quests.Change(c, in, receipts, f.Opcode, id, opts)
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
	delete(g.questConfirm, id)
	packets, err := g.catalog().Quests.ProjectScene(out.Character, out.Inventory, out.Receipts, opts.Dungeon != nil)
	if err != nil {
		return err
	}
	var assets []inventorywire.Packet
	if action.AssetProjection {
		assets, _, err = inventorywire.Town(out.Inventory, out.Character, inventorywire.Options{CashPolicies: g.catalog().CashPolicies, Now: opts.Now})
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

func (g *game) questHasNPC(c storage.Character, n questwire.NpcTarget) (bool, error) {
	state, err := resources.ReadTownQuests(c.Gameplay)
	if err != nil {
		return false, err
	}
	return g.catalog().Towns.HasQuestNPC(c.Level, state, point(c.Position), n.NpcId, n.Town, n.Area, n.X, n.Y), nil
}

func (g *game) questTrigger(ctx context.Context, f protocol.Frame) error {
	catalog := g.catalog().Quests
	req, isQuestion, err := catalog.ReadTrigger(f.Body)
	if err != nil {
		return err
	}
	reject := func() error { return g.send(1, f.Opcode, []byte{0, 7, 0}) }
	phase, ok := g.questInteractionPhase(req.Id)
	if req.Id == questwire.CommunicatorQuest && req.Trigger == 0x10 && g.channel == 19 && g.opening == nil && !g.ordinary.Active() {
		phase, ok = storage.TownReady, true
	}
	if !ok {
		return reject()
	}
	opts := questwire.ChangeContext{Now: time.Now(), Delegated: g.server.delegated, Arrival: g.questArrival, HasNPC: g.questHasNPC, Dungeon: g.questDungeon(), Lease: g.lease}
	if phase == storage.Selected && req.Id >= 2672 && req.Id <= 2674 {
		opts.Training = g.duelTrainingTrigger
	}
	var action questwire.Action
	var fact *questwire.Confirmation
	out, err := g.server.DB.ChangeQuest(ctx, g.lease, phase, g.catalog().Weights, func(c storage.Character, in storage.Inventory, receipts map[string]json.RawMessage) (storage.TownQuestMutation, error) {
		if phase == storage.Selected && c.Level < 50 {
			return storage.TownQuestMutation{}, storage.ErrInvalid
		}
		action, fact, err = catalog.Trigger(c, in, receipts, req, opts)
		return action.Mutation, err
	})
	if errors.Is(err, storage.ErrInvalid) || errors.Is(err, storage.ErrPhase) {
		return reject()
	}
	if err != nil {
		return err
	}
	if fact != nil {
		for id, prev := range g.questConfirm {
			if prev.CharacterID != fact.CharacterID || prev.Revision != fact.Revision || prev.AssetRevision != fact.AssetRevision {
				delete(g.questConfirm, id)
			}
		}
		if g.questConfirm[req.Id] == *fact && !action.Full {
			return nil
		}
		if g.questConfirm == nil {
			g.questConfirm = make(map[uint16]questwire.Confirmation)
		}
		g.questConfirm[req.Id] = *fact
	} else {
		delete(g.questConfirm, req.Id)
	}
	if err = g.send(1, f.Opcode, action.Ack); err != nil || isQuestion || !action.Full {
		return err
	}
	if err = g.sendQuestBook(out); err != nil {
		return err
	}
	if len(action.Mutation.Quests) > 0 && catalog.IsCinematic(req.Id) {
		return g.dungeonTryMovieSettlement(ctx, g.ordinary.Run, true)
	}
	return nil
}

func (g *game) sendQuestBook(out storage.TownQuestResult) error {
	return g.sendQuestBookOrdered(out, false)
}

func (g *game) sendQuestBookOrdered(out storage.TownQuestResult, clearedFirst bool) error {
	packets, err := g.catalog().Quests.ProjectScene(out.Character, out.Inventory, out.Receipts, g.ordinary.Active())
	if err != nil {
		return err
	}
	if clearedFirst {
		for _, p := range packets {
			if p.Opcode == 356 {
				if err = g.send(p.Class, p.Opcode, p.Body); err != nil {
					return err
				}
			}
		}
	}
	for _, p := range packets {
		if clearedFirst && p.Opcode == 356 {
			continue
		}
		if err = g.send(p.Class, p.Opcode, p.Body); err != nil {
			return err
		}
	}
	return nil
}

func (g *game) questInteractionPhase(id uint16) (storage.Phase, bool) {
	if g.opening != nil || g.ordinary.Returning() || g.ordinary.Active() && g.questDungeon() == nil {
		return 0, false
	}
	if id < 2670 || id > 2675 {
		return storage.TownReady, g.channel == 19
	}
	if g.channel != 252 || g.duelPosition == nil || g.duelPosition.Town != 10 {
		return 0, false
	}
	if id >= 2672 && id <= 2674 {
		return storage.Selected, g.server.delegated && g.duelPosition.Area == 4
	}
	area := uint16(4)
	if id == 2670 {
		area = 0
	}
	return storage.Selected, g.duelPosition.Area == area && (g.arcade == nil || g.arcade.Closed)
}

func (g *game) questDungeon() *questwire.DungeonContext {
	r := g.ordinary.Run
	if r == nil || !r.Ready || r.PlayerDeath != nil || r.Room == nil {
		return nil
	}
	def := r.Definition.Dungeon
	room := r.Room
	if r.InLayer {
		room = r.Room.CinematicLayer
		if room == nil {
			return nil
		}
	}
	d := &questwire.DungeonContext{Lease: g.lease, RunID: r.ID, Hash: def.ResourceHash, Character: r.Character, Dungeon: def.ID, Maze: def.MazeIndex,
		Map: room.MapID, X: room.X, Y: room.Y, Difficulty: def.Difficulty, Connections: def.QuestConnections}
	addMap := func(room opening.Room) {
		d.Maps = append(d.Maps, room.MapID)
		d.Maps = append(d.Maps, room.MapAliases...)
		if layer := room.CinematicLayer; layer != nil {
			d.Maps = append(d.Maps, layer.MapID)
			d.Maps = append(d.Maps, layer.MapAliases...)
		}
	}
	addMap(def.Start)
	for _, room := range def.Rooms {
		addMap(room)
	}
	return d
}

func (g *game) questComplete(ctx context.Context, f protocol.Frame) error {
	r, err := questwire.ReadComplete(f.Body)
	if err != nil {
		return err
	}
	reject := func(code uint16) error {
		return g.send(1, f.Opcode, []byte{0, byte(code), byte(code >> 8)})
	}
	phase, ok := g.questInteractionPhase(r.Id)
	if !ok {
		return reject(7)
	}
	var action questwire.CompleteAction
	now := time.Now()
	cat := g.catalog()
	opts := questwire.CompleteContext{
		ChangeContext:   questwire.ChangeContext{Now: now, HasNPC: g.questHasNPC, Dungeon: g.questDungeon(), Lease: g.lease},
		GoldCarryLimits: cat.GoldCarryLimits, Growth: cat.OpeningCombat.ApplyQuestGrowth,
		FreeSkills: cat.OpeningCombat.QuestFreeSkills, SkillMaster: phase != storage.Selected && g.contracts[1] > now.Unix(),
	}
	out, err := g.server.DB.ChangeQuest(ctx, g.lease, phase, cat.Weights, func(c storage.Character, in storage.Inventory, receipts map[string]json.RawMessage) (storage.TownQuestMutation, error) {
		if phase == storage.Selected && (c.Level < 50 || r.Id >= 2672 && r.Id <= 2674 && !g.arcade.CanCompleteQuest(g.lease, c.ID)) {
			return storage.TownQuestMutation{}, storage.ErrInvalid
		}
		action, err = cat.Quests.Complete(c, in, receipts, r, opts)
		return action.Mutation, err
	})
	if err != nil {
		switch {
		case errors.Is(err, storage.ErrCapacity):
			return reject(4)
		case errors.Is(err, questwire.ErrRewardResource):
			return reject(0x7f01)
		case errors.Is(err, storage.ErrInvalid), errors.Is(err, storage.ErrFunds), errors.Is(err, storage.ErrPhase):
			return reject(7)
		default:
			return err
		}
	}
	delete(g.questConfirm, r.Id)
	delete(g.questAcceptSeen, r.Id)
	c, err := questActorInventory(out)
	if err != nil {
		return err
	}
	base, profile, err := readActor(c, g.channel)
	if err != nil {
		return err
	}
	if phase == storage.Selected {
		display, err := duellobby.Display(c)
		if err != nil {
			return err
		}
		_, profile, err = readActor(display, g.channel)
		if err != nil {
			return err
		}
	}
	_, skills, err := skillPackets(profile)
	if err != nil {
		return err
	}
	actor, err := g.actorMode0(base)
	if err != nil {
		return err
	}
	assets, _, err := inventorywire.Town(out.Inventory, c, inventorywire.Options{CashPolicies: cat.CashPolicies, Now: now})
	if err != nil {
		return err
	}
	if action.Awakening != 0 {
		if err = g.send(0, 2, actor); err != nil {
			return err
		}
		if err = g.send(0, 19, skills); err != nil {
			return err
		}
	}
	if err = g.send(1, f.Opcode, action.Ack); err != nil {
		return err
	}
	if err = g.sendQuestBook(out); err != nil {
		return err
	}
	if action.Awakening == 0 {
		if opts.Dungeon == nil {
			if err = g.send(0, 19, skills); err != nil {
				return err
			}
			if err = g.send(0, 2, actor); err != nil {
				return err
			}
		}
	}
	if opts.Dungeon != nil {
		if err = g.send(0, 37, dungeon.ExperienceNotice(base, profile.Progression.Sp, profile.Progression.Tp)); err != nil {
			return err
		}
	}
	if err = g.projected(assets); err != nil {
		return err
	}
	if action.Mutation.Reward != nil && cat.Quests.IsCinematic(r.Id) {
		return g.dungeonTryMovieSettlement(ctx, g.ordinary.Run, true)
	}
	return nil
}

func questActorInventory(out storage.TownQuestResult) (storage.Character, error) {
	c := out.Character
	if c.Worn != nil {
		return c, nil
	}
	for _, row := range out.Inventory.Items {
		if row.Location != 3 {
			continue
		}
		worn, err := row.WornEquipment(c)
		if err != nil {
			return c, err
		}
		c.Worn = append(c.Worn, worn)
	}
	return c, nil
}
