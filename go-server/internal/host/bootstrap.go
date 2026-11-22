package host

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/binary"
	"math"
	"time"

	"s9a1/server/internal/duellobby"
	"s9a1/server/internal/inventorywire"
	"s9a1/server/internal/mailwire"
	"s9a1/server/internal/opening"
	"s9a1/server/internal/protocol"
	"s9a1/server/internal/rewardcount"
	"s9a1/server/internal/storage"
)

func (g *game) packets(frames []opening.Packet) error {
	for _, p := range frames {
		if err := g.send(p.Class, p.Opcode, p.Body); err != nil {
			return err
		}
	}
	return nil
}
func (g *game) projected(frames []inventorywire.Packet) error {
	for _, p := range frames {
		if err := g.send(0, p.Opcode, p.Body); err != nil {
			return err
		}
	}
	return nil
}
func (g *game) questMenu(ctx context.Context, c storage.Character, in storage.Inventory) ([]inventorywire.Packet, error) {
	receipts, err := g.server.DB.Receipts(ctx, g.lease)
	if err != nil {
		return nil, err
	}
	bodies, err := g.server.Characters.Quests.Project(c, in, receipts)
	if err != nil {
		return nil, err
	}
	out := make([]inventorywire.Packet, 0, len(bodies))
	for _, b := range bodies {
		out = append(out, inventorywire.Packet{Opcode: b.Opcode, Body: b.Body})
	}
	return out, nil
}
func (g *game) specialCounters(ctx context.Context, c storage.Character) error {
	if !g.server.delegated || duellobby.IsChannel(g.channel) {
		return nil
	}
	now := time.Now()
	receipts, err := g.server.DB.DailyRewardReceipts(ctx, g.lease, now)
	if err != nil {
		return err
	}
	body, err := rewardcount.Body(c, receipts, g.server.Characters.DailyLimits, now)
	if err != nil || len(body) == 0 {
		return err
	}
	return g.send(0, 1734, body)
}
func definitionProof(d opening.Definition) storage.TutorialDefinition {
	p := d.ReturnPoint
	return storage.TutorialDefinition{Job: d.Job, Dungeon: d.Dungeon.ID, ResourceHash: d.Hash, ReturnPoint: storage.Position{Town: uint16(p.Town), Area: uint16(p.Area), X: p.X, Y: p.Y, Direction: p.Direction}}
}
func (g *game) completeOpening(ctx context.Context, c storage.Character, d opening.Definition) error {
	c, err := g.server.DB.CompleteTutorial(ctx, g.lease, c.ID, c.Revision, definitionProof(d))
	if err != nil {
		return err
	}
	a, _, err := readActor(c, g.channel)
	if err != nil {
		return err
	}
	b, err := protocol.Mode1(a, true)
	if err != nil {
		return err
	}
	g.opening = nil
	return g.packets([]opening.Packet{{Class: 1, Opcode: 143, Body: []byte{1, 0}}, {Opcode: 2, Body: b}, {Opcode: 23, Body: protocol.Area(a)}, {Opcode: 24, Body: protocol.Town(a)}})
}
func (g *game) syncTown(ctx context.Context, op uint16, body []byte) error {
	if op == 143 && len(body) != 6 || op == 120 && len(body) != 0 {
		return storage.ErrInvalid
	}
	r, err := g.server.DB.Current(ctx, g.lease)
	if err != nil {
		return err
	}
	c, err := selected(r)
	if err != nil {
		return err
	}
	if duellobby.IsChannel(g.channel) {
		return g.syncDuel(ctx, op, r, c)
	}
	profile, err := readGameplay(c.Gameplay)
	if err != nil {
		return err
	}
	pending := profile.Tutorial != nil && !profile.Tutorial.Completed
	var def opening.Definition
	if pending {
		var exists bool
		def, exists = g.server.Characters.Openings[c.Job]
		if !exists {
			return storage.ErrInvalid
		}
		if op == 143 && bytes.Equal(body, []byte{0, 31, 0, 0, 0, 1}) {
			return g.completeOpening(ctx, c, def)
		}
	}
	if g.opening != nil {
		return g.openingRequest(ctx, op, body)
	}
	if g.channel != 19 {
		return storage.ErrPhase
	}
	ack := []byte{0, 0}
	if op == 143 {
		ack = []byte{1, 0}
	}
	if r.Phase == storage.TownReady || op == 143 && r.Phase == storage.TownLoading {
		return g.send(1, op, ack)
	}
	var in storage.Inventory
	switch r.Phase {
	case storage.Selected:
		c, in, err = g.server.DB.PrepareTownInventory(ctx, g.lease, c.ID, c.Revision)
	case storage.TownLoading:
		in, err = g.server.DB.Inventory(ctx, g.lease)
	default:
		return storage.ErrPhase
	}
	if err != nil {
		return err
	}
	a, p, err := readActor(c, g.channel)
	if err != nil {
		return err
	}
	if r.Phase == storage.TownLoading {
		full, err := protocol.Mode1(a, true)
		if err != nil {
			return err
		}
		buff, err := buffReset(p, in)
		if err != nil {
			return err
		}
		if _, err = g.server.DB.AdvanceTown(ctx, g.lease, false); err != nil {
			return err
		}
		if err = g.send(1, op, ack); err != nil {
			return err
		}
		if err = g.send(0, 2, full); err != nil {
			return err
		}
		if len(p.CommandLocks) == 512 {
			word := func(i int) bool { v := binary.LittleEndian.Uint16(p.CommandLocks[i:]); return v == 1 || v == 2 }
			if word(0) || word(2) {
				if err = g.send(0, 376, append(binary.LittleEndian.AppendUint32(nil, 512), p.CommandLocks...)); err != nil {
					return err
				}
			}
		}
		if err = g.specialCounters(ctx, c); err != nil {
			return err
		}
		if err = g.send(0, 1361, buff); err != nil {
			return err
		}
		if a.BuffSkill != 0 {
			binary.LittleEndian.PutUint16(buff, a.BuffSkill)
			if err = g.send(0, 1361, buff); err != nil {
				return err
			}
		}
		g.gaugePending = true
		return nil
	}
	load, err := g.loadingPackets(ctx, r, c, in, a, p, op, ack, pending)
	if err != nil {
		return err
	}
	if err = g.packets(load.beforeCounts); err != nil {
		return err
	}
	if err = g.specialCounters(ctx, c); err != nil {
		return err
	}
	if err = g.packets(load.afterCounts); err != nil {
		return err
	}
	g.battlePassSent = true
	if !pending {
		return nil
	}
	first := g.nextActor
	if first == 0 {
		first = 402
	}
	base, next, err := def.Reserve(first)
	if err != nil {
		return err
	}
	var random [4]byte
	if _, err = rand.Read(random[:]); err != nil {
		return err
	}
	c, err = g.server.DB.EnterOpening(ctx, g.lease, c.ID, c.Revision, definitionProof(def), time.Now(), def.Dungeon.EntryFatigueCost)
	if err != nil {
		return err
	}
	a, _, err = readActor(c, g.channel)
	if err != nil {
		return err
	}
	run, frames, err := opening.Start(def, a, binary.LittleEndian.Uint32(random[:]), base, g.server.native140, g.server.nativeParty)
	if err != nil {
		return err
	}
	g.nextActor, g.opening = next, run
	return g.packets(frames)
}

type loadPackets struct{ beforeCounts, afterCounts []opening.Packet }

func (g *game) loadingPackets(ctx context.Context, r storage.Roster, c storage.Character, in storage.Inventory, a protocol.Actor, p actorProfile, op uint16, ack []byte, pending bool) (loadPackets, error) {
	var result loadPackets
	ordinary, special, err := inventorywire.Town(in, c, inventorywire.Options{Now: time.Now(), CashPolicies: g.server.Characters.CashPolicies})
	if err != nil {
		return result, err
	}
	eventContainers, err := inventorywire.FateContainers(in, c)
	if err != nil {
		return result, err
	}
	locks, skills, err := skillPackets(p)
	if err != nil {
		return result, err
	}
	m0, err := protocol.Mode0(a, g.server.native140)
	if err != nil {
		return result, err
	}
	m1, err := protocol.Mode1(a, false)
	if err != nil {
		return result, err
	}
	quests, err := g.questMenu(ctx, c, in)
	if err != nil {
		return result, err
	}
	mail, err := mailwire.Town(c)
	if err != nil {
		return result, err
	}
	if p.ClassReplacementCount > 127 || p.CrystalContractSelection > 5 && p.CrystalContractSelection != 255 {
		return result, storage.ErrInvalid
	}
	dst := &result.beforeCounts
	notice := func(id uint16, b []byte) { *dst = append(*dst, opening.Packet{Opcode: id, Body: b}) }
	group := func(packets []inventorywire.Packet) {
		for _, p := range packets {
			notice(p.Opcode, p.Body)
		}
	}
	if op == 120 && g.server.Characters.Fate != nil {
		notice(108, fateEvents())
	}
	*dst = append(*dst, opening.Packet{Class: 1, Opcode: op, Body: ack})
	notice(376, []byte{0, 0, 0})
	notice(2, m0)
	notice(2, m1)
	notice(124, nil)
	if !g.server.nativeParty {
		notice(9, protocol.PartyRemove(a.ID, a.Group, a.Channel, false))
	}
	if !pending {
		notice(120, []byte{0, 0})
		notice(22, protocol.Position(a))
		notice(23, protocol.Area(a))
	}
	notice(53, protocol.AccountWallet(r.Cera))
	if !pending {
		notice(24, protocol.Town(a))
	}
	group(ordinary)
	group(special)
	for _, packet := range mail {
		notice(packet.Opcode, packet.Body)
	}
	group(quests)
	notice(376, locks)
	notice(19, skills)
	replacement := make([]byte, 6)
	replacement[0] = p.ClassReplacementCount
	if a.GrowType > 5 {
		replacement[1] = 50
	}
	notice(1141, replacement)
	notice(938, []byte{p.GoldExpansion})
	notice(1929, make([]byte, 4))
	dst = &result.afterCounts
	if !g.battlePassSent {
		info := make([]byte, 21)
		info[4] = 1
		notice(1850, info)
		notice(1852, make([]byte, 28))
	}
	notice(889, []byte{0, p.CrystalContractSelection})
	if f := g.server.Characters.Fate; f != nil {
		group(eventContainers)
		b, err := fateState(p, f.StoryCapacity, f.SinCosts, time.Now())
		if err != nil {
			return result, err
		}
		notice(2055, b)
	}
	return result, nil
}

func fateEvents() []byte {
	b := make([]byte, 33)
	binary.LittleEndian.PutUint16(b, 2)
	binary.LittleEndian.PutUint16(b[2:], 4171)
	binary.LittleEndian.PutUint16(b[17:], 2554)
	return b
}

func fateState(p actorProfile, capacity int, costs []int32, now time.Time) ([]byte, error) {
	f := p.Fate
	if len(f.Sins) != 7 || len(f.Blessings) != 7 || len(f.EquippedEffects) != 3 || len(costs) != 7 || f.StoryCount < 0 || f.StoryCount > 6 || f.ArchangelPoints < 0 || f.FullClears < 0 {
		return nil, storage.ErrInvalid
	}
	b := make([]byte, 178)
	put := func(offset int, value int32) { binary.LittleEndian.PutUint32(b[offset:], uint32(value)) }
	put(1, f.ArchangelPoints)
	var spent int64
	for i, rank := range f.Sins {
		if rank < 0 || f.Blessings[i] < 0 {
			return nil, storage.ErrInvalid
		}
		spent += int64(rank) * int64(costs[i])
		put(17+8*i, int32(i+1))
		put(21+8*i, f.Blessings[i])
		put(77+8*i, int32(i+1))
		put(81+8*i, rank)
	}
	if spent > math.MaxInt32 || spent < 0 {
		return nil, storage.ErrInvalid
	}
	put(73, int32(spent))
	for i, v := range f.EquippedEffects {
		if v < 0 {
			return nil, storage.ErrInvalid
		}
		put(134+4*i, v)
	}
	day := now.UTC().Add(2 * time.Hour)
	week := day.AddDate(0, 0, -((int(day.Weekday()) - int(time.Thursday) + 7) % 7)).Format("20060102")
	if int(f.StoryCount) >= capacity || f.StoryWeek == week {
		b[151] = 1
	}
	put(152, f.StoryCount)
	for i := int32(0); i < f.StoryCount; i++ {
		b[156+i] = 1
	}
	put(170, f.FullClears)
	return b, nil
}
func (g *game) inDungeonPosition(op uint16, body []byte) error {
	if op == 35 {
		_, err := protocol.ReadPosition(body)
		return err
	}
	_, err := protocol.ReadMotion(body)
	return err
}
func (g *game) openingRequest(ctx context.Context, op uint16, body []byte) error {
	if g.opening == nil {
		return storage.ErrPhase
	}
	r, err := g.server.DB.Current(ctx, g.lease)
	if err != nil {
		return err
	}
	c, err := selected(r)
	if err != nil {
		return err
	}
	a, p, err := readActor(c, g.channel)
	if err != nil {
		return err
	}
	if op == 39 {
		project := func(v storage.Character) (protocol.Actor, error) { a, _, err := readActor(v, g.channel); return a, err }
		frames, complete, err := g.opening.Death(ctx, g.server.DB, g.lease, c, body, &g.server.Characters.OpeningCombat, project)
		if sendErr := g.packets(frames); sendErr != nil {
			return sendErr
		}
		if complete {
			g.opening = nil
		}
		return err
	}
	wasReady := g.opening.Ready()
	frames, err := g.opening.Handle(op, body, a, p.Progression.Sp, p.Progression.Tp)
	if err != nil {
		return err
	}
	if err = g.packets(frames); err != nil {
		return err
	}
	if op == 37 && !wasReady && g.opening.Ready() {
		return g.specialCounters(ctx, c)
	}
	return nil
}
