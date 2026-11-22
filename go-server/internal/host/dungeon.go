package host

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"time"

	"s9a1/server/internal/dungeon"
	"s9a1/server/internal/inventorywire"
	"s9a1/server/internal/mailwire"
	"s9a1/server/internal/opening"
	"s9a1/server/internal/protocol"
	"s9a1/server/internal/questwire"
	"s9a1/server/internal/ruby"
	"s9a1/server/internal/storage"
)

func (g *game) dungeonRequest(ctx context.Context, f protocol.Frame) error {
	db := g.server.DB
	switch f.Opcode {
	case 38, 47:
		if len(f.Body) > 4096 {
			return storage.ErrInvalid
		}
		// The old owner observes these reports without returning a frame or
		// deriving any combat, asset, or quest eligibility from them.
		return db.ValidateLease(ctx, g.lease)
	case 1645:
		if len(f.Body) != 1 || f.Body[0] != 1 {
			return storage.ErrInvalid
		}
		return storage.ErrPhase
	case 1701, 1702, 1703:
		if f.Opcode == 1703 && len(f.Body) != 18 || f.Opcode != 1703 && len(f.Body) != 0 {
			return storage.ErrInvalid
		}
		return g.abaddonControl(ctx, f)
	}
	roster, err := db.Current(ctx, g.lease)
	if err != nil {
		return err
	}
	c, err := selected(roster)
	if err != nil {
		return err
	}
	a, p, err := readActor(c, g.channel)
	if err != nil {
		return err
	}
	ss := &g.ordinary
	run := ss.Run
	catalog := g.server.Characters.Dungeons
	if f.Opcode == 15 || f.Opcode == 16 {
		if err := g.specialCounters(ctx, c); err != nil {
			return err
		}
	}
	if g.channel == 63 && (f.Opcode == 15 || f.Opcode == 16) {
		if roster.Phase != storage.TownReady {
			return storage.ErrPhase
		}
		return g.oculusSelector(ctx, c, a, f)
	}
	if g.channel == 63 && roster.Phase == storage.TownReady && run == nil {
		switch f.Opcode {
		case 40:
			if len(f.Body) != 6 {
				return storage.ErrInvalid
			}
			related := uint16(f.Body[4]) | uint16(f.Body[5])<<8
			if g.oculus.phase == oculusCleared && g.oculus.character == c.ID && (related == 0 || related == c.ID) {
				return db.ValidateLease(ctx, g.lease)
			}
			return storage.ErrPhase
		case 41:
			if len(f.Body) != 2 {
				return storage.ErrInvalid
			}
			return g.send(1, 41, []byte{0, 0, 0})
		case 117:
			return g.send(0, 115, []byte{0, 0})
		case 72:
			return g.dungeonExit(ctx, nil, c, a, f.Body)
		case 69, 70:
			if ss.CardWindowRetired && len(f.Body) == 0 {
				return db.ValidateDungeonProof(ctx, g.lease, c, nil)
			}
		case 42:
			if len(f.Body) != 0 {
				return storage.ErrInvalid
			}
			if g.oculus.phase == oculusRunning && g.oculus.character == c.ID && g.oculus.parked != nil {
				mode, ok := g.oculusMode()
				if !ok {
					return storage.ErrPhase
				}
				if err := db.ValidateLease(ctx, g.lease); err != nil {
					return err
				}
				return g.oculusWaitingPackets(c, mode)
			}
			if ss.Returning() {
				a = g.oculusTownActor(a)
				a.AreaState = 0
				return g.packets([]opening.Packet{{Opcode: 23, Body: protocol.Area(a)}, {Opcode: 24, Body: protocol.Town(a)}})
			}
		}
	}
	if roster.Phase != storage.TownReady || g.channel != 19 && (g.channel != 63 || run == nil || run.OculusEntry == nil) ||
		g.opening != nil || run != nil && run.Character != c.ID {
		return storage.ErrPhase
	}
	gate := catalog.Gate(c.Position.Town, c.Position.Area)
	switch f.Opcode {
	case 15:
		target, err := dungeon.ReadSelector(f.Body)
		if err != nil {
			return err
		}
		if run != nil && run.RubyEntry != nil && run.Cleared {
			l, err := g.validateRubyClear(ctx, c, run)
			if errors.Is(err, storage.ErrPhase) || errors.Is(err, ruby.ErrInvalid) {
				return g.send(1, 15, protocol.Failure(4))
			}
			if err != nil {
				return err
			}
			if target == 0 {
				target = 9700
			}
			if _, err = l.Next(target); err != nil {
				return g.send(1, 15, protocol.Failure(4))
			}
			return g.send(1, 15, protocol.Ack())
		}
		access, err := g.dungeonAccess(c)
		if err != nil {
			return err
		}
		available := catalog.AvailableFor(gate, access)
		frames, ok := ss.Select(a, gate, target, available)
		if !ok {
			return g.send(1, 15, protocol.Failure(4))
		}
		if gate == ruby.Gate && len(frames) > 1 && catalog.Ruby != nil {
			l, err := g.readRubyLedger(ctx, c)
			if err != nil {
				return err
			}
			view, err := g.rubyProgressFrames(c, l, nil)
			if err != nil {
				return err
			}
			frames = append(view, frames...)
		} else if gate != ruby.Gate {
			g.rubyView = nil
		}
		if gate == 100 && len(frames) > 1 {
			view, err := g.abaddonRosterPackets(ctx)
			if err != nil {
				return err
			}
			frames = append(append(frames[:1:1], view...), frames[1:]...)
		}
		if gate == dungeon.WisdomGate && slices.Contains(available, dungeon.WisdomDungeon) {
			preview, ok := catalog.Choose(gate, access, dungeon.Entry{Dungeon: dungeon.WisdomDungeon}, nil, 0)
			if ok {
				err := db.PreviewWisdom(ctx, g.lease, c, preview.Dungeon.EntryTicketCount, preview.Dungeon.WisdomPasses, time.Now())
				if errors.Is(err, storage.ErrWisdomTickets) {
					body, err := wisdomTicketNotice(preview.Dungeon.EntryTicketCount)
					if err != nil {
						return err
					}
					frames = append(frames, opening.Packet{Opcode: 488, Body: body})
				} else if err != nil {
					return err
				}
			}
		}
		return g.packets(frames)
	case 16:
		return g.enterDungeon(ctx, f.Body, c, a, catalog, gate)
	case 72:
		return g.dungeonExit(ctx, run, c, a, f.Body)
	case 42, 132:
		if len(f.Body) != 0 {
			return storage.ErrInvalid
		}
		if run != nil && run.OculusEntry != nil && g.oculus.phase == oculusRunning {
			if f.Opcode != 42 || run.PlayerDeath == nil {
				return storage.ErrPhase
			}
			return g.parkOculus(ctx, run, c, nil)
		}
		if run != nil && (f.Opcode != 42 || !run.Ready) || run == nil && f.Opcode == 42 && !ss.Returning() {
			return storage.ErrPhase
		}
		if !ss.Returning() {
			if run != nil && run.WisdomEntry != nil {
				if err := db.ReturnWisdomDungeon(ctx, g.lease, c, *run.WisdomEntry); err != nil {
					return err
				}
				ss.BeginReturn()
			} else {
				if err := db.ReturnDungeon(ctx, g.lease, c, run != nil); err != nil {
					return err
				}
				ss.BeginReturn()
			}
			g.rubyView = nil
		}
		a.AreaState = 0
		if g.channel == 63 {
			a = g.oculusTownActor(a)
		}
		return g.packets([]opening.Packet{{Opcode: 23, Body: protocol.Area(a)}, {Opcode: 24, Body: protocol.Town(a)}})
	}
	if run == nil {
		if f.Opcode == 41 && len(f.Body) == 2 {
			return g.send(1, 41, []byte{0, 0, 0})
		}
		if ss.CardWindowRetired && (f.Opcode == 69 || f.Opcode == 70) && len(f.Body) == 0 {
			if _, selecting := ss.GateSelection(c.ID); !selecting {
				return db.ValidateDungeonProof(ctx, g.lease, c, nil)
			}
		}
		if f.Opcode == 449 || f.Opcode == 450 {
			return g.dungeonTournamentCards(ctx, nil, c, f)
		}
		return storage.ErrPhase
	}
	switch f.Opcode {
	case 37:
		if len(f.Body) != 8 && len(f.Body) != 16 {
			return storage.ErrInvalid
		}
		wasReady := run.Ready
		run.PrepareLoadedStage()
		next, changed, err := g.dungeonEmptyClear(ctx, run, c)
		if err != nil {
			return err
		}
		if err := g.packets(run.Load(a, p.Progression.Sp, p.Progression.Tp)); err != nil {
			return err
		}
		if err := g.projectGameMasterFollowers(run); err != nil {
			return err
		}
		if changed {
			if err := g.dungeonQuestProjection(ctx, next); err != nil {
				return err
			}
		}
		if err := g.dungeonTryConfirm(ctx, run); err != nil {
			return err
		}
		if err := g.dungeonTryMovieSettlement(ctx, run, false); err != nil || wasReady {
			return err
		}
		current, err := db.Current(ctx, g.lease)
		if err != nil {
			return err
		}
		c, err = selected(current)
		if err != nil {
			return err
		}
		return g.specialCounters(ctx, c)
	case 45:
		x, y, layer, err := dungeon.ReadMove(f.Body)
		if err != nil {
			return err
		}
		return g.dungeonMove(ctx, run, c, a, x, y, layer, f.Sequence)
	case 191:
		frames, err := run.SceneControl(f.Body)
		if err != nil {
			return err
		}
		return g.packets(frames)
	case 120:
		if len(f.Body) != 0 {
			return storage.ErrInvalid
		}
		return g.send(1, 120, []byte{0, 0})
	case 143:
		if len(f.Body) != 6 {
			return storage.ErrInvalid
		}
		return g.send(1, 143, []byte{1, 0})
	case 39:
		return g.dungeonMonsterDeath(ctx, run, c, a, f.Body)
	case 40, 41:
		return g.dungeonRevival(ctx, run, c, f)
	case 43:
		return g.dungeonPickup(ctx, run, c, f.Body)
	case 117:
		if run.RubyEntry != nil || run.WisdomEntry != nil {
			return g.specialDungeonConfirmation(ctx, run, c, f.Body)
		}
		return g.dungeonConfirmation(ctx, run, c, f.Body, false)
	case 46:
		if run.WisdomEntry != nil {
			return g.wisdomResult(ctx, run, c, a, p.Progression.Sp, p.Progression.Tp, f.Body)
		}
		if run.RubyEntry != nil {
			if _, err := dungeon.ReadResult(f.Body); err != nil {
				return err
			}
			_, err := g.validateRubyClear(ctx, c, run)
			return err
		}
		return g.dungeonResult(ctx, run, c, f.Body, a)
	case 69, 70, 71:
		if run.WisdomEntry != nil {
			return g.wisdomCards(ctx, run, c, f, p.Progression.Fatigue)
		}
		return g.dungeonCards(ctx, run, c, f, p.Progression.Fatigue)
	case 449, 450:
		return g.dungeonTournamentCards(ctx, run, c, f)
	default:
		return storage.ErrPhase
	}
}

func dungeonLuck(catalog *dungeon.Catalog, run *dungeon.Run) int64 {
	d := run.Definition.Dungeon
	if !d.DisableDrops && d.UsesOrdinaryMonsterDrops && (d.UsesOrdinaryRewards || d.Difficulty != 0) {
		return catalog.Luck.PointsPerFatigue
	}
	return 0
}

func (g *game) enterDungeon(ctx context.Context, body []byte, c storage.Character, a protocol.Actor, catalog *dungeon.Catalog, gate uint16) error {
	req, err := dungeon.ReadEntry(body)
	if err != nil {
		return err
	}
	reject := func(reason byte) error { return g.send(1, 16, []byte{0, reason, 0, 0}) }
	if g.ordinary.WisdomExit && len(g.ordinary.ExitBody) >= 2 && g.ordinary.ExitBody[1] == 0 &&
		req.Dungeon != dungeon.WisdomDungeon {
		return reject(4)
	}
	access, err := g.dungeonAccess(c)
	if err != nil {
		return err
	}
	if !g.ordinary.Permits(c.ID, gate, req.Dungeon) || !req.Supported() {
		if minimum := catalog.MinimumLevel(gate, req.Dungeon, access); minimum > c.Level {
			return reject(14)
		}
		return reject(4)
	}
	if catalog.IsChallenge(gate, req.Dungeon) {
		fame, err := g.dungeonFame(ctx, c)
		if err != nil {
			return err
		}
		if fame < 9885 {
			if err := reject(4); err != nil {
				return err
			}
			body, err := protocol.Announcement("名望不足，挑战模式需要名望9885。")
			if err != nil {
				return err
			}
			return g.send(0, 488, body)
		}
	}
	rubyContinue := g.ordinary.Run != nil && g.ordinary.Run.RubyEntry != nil && g.ordinary.Run.Cleared && ruby.Supported(req.Dungeon)
	if r := g.ordinary.Run; r != nil && !rubyContinue {
		if r.EntryReplay(req) {
			if r.WisdomEntry != nil {
				if err := g.server.DB.ValidateWisdomEntry(ctx, g.lease, c, *r.WisdomEntry); err != nil {
					if wisdomRejected(err) {
						return reject(4)
					}
					return err
				}
			}
			if key := r.StormEntryKey(); key != "" {
				if err := g.server.DB.ValidateDungeonProof(ctx, g.lease, c, []string{key}); err != nil {
					return err
				}
			}
			if r.RubyEntry != nil {
				if err := g.server.DB.ValidateDungeonProof(ctx, g.lease, c, []string{ruby.EntryKey(r.ID)}); err != nil {
					return err
				}
			}
			return g.send(1, 16, dungeon.EntryAccepted(c.ID))
		}
		return reject(4)
	}
	if !slices.Contains(catalog.AvailableFor(gate, access), req.Dungeon) {
		return reject(4)
	}
	eligible, err := g.server.Characters.Quests.DungeonCandidatesFor(c, req.Dungeon, catalog.QuestCandidates(gate, req.Dungeon))
	if err != nil {
		return err
	}
	seed, err := dungeon.NewSeed()
	if err != nil {
		return err
	}
	selected := req
	var rubyLedger *ruby.Ledger
	if gate == ruby.Gate {
		if catalog.Ruby == nil || req.Difficulty != 2 {
			return reject(4)
		}
		var l ruby.Ledger
		if rubyContinue {
			l, err = g.validateRubyClear(ctx, c, g.ordinary.Run)
		} else {
			l, err = g.readRubyLedger(ctx, c)
		}
		if err != nil {
			return err
		}
		selected.Dungeon, err = l.Next(req.Dungeon)
		if err != nil {
			return reject(4)
		}
		rubyLedger = &l
	}
	if dungeon.IsStormDungeon(req.Dungeon) {
		if gate != dungeon.StormGate || req.Difficulty != 4 || req.ChallengeStage != 0 {
			return reject(4)
		}
		recent, err := g.server.DB.StormRecent(ctx, g.lease, c)
		if err != nil {
			return err
		}
		selected.Dungeon, err = g.ordinary.ChooseStorm(seed, catalog.AvailableFor(gate, access), recent)
		if err != nil {
			return reject(4)
		}
	}
	def, ok := catalog.Choose(gate, access, selected, eligible, seed)
	if !ok {
		return reject(4)
	}
	first := g.nextActor
	if first == 0 {
		first = 402
	}
	run, next, err := dungeon.Prepare(def, req, a, first, seed)
	if err != nil {
		return reject(4)
	}
	if rubyLedger != nil {
		run.RubyEntry, err = rubyEntryGrant(c, *rubyLedger, run)
		if err != nil {
			return reject(4)
		}
	}
	frames, err := run.EntryFrames(a, g.server.native140, g.server.nativeParty)
	if err != nil {
		return err
	}
	if run.TournamentReward() != 0 {
		if err := g.server.DB.ValidateTournamentAdmission(ctx, g.lease, c); err != nil {
			if errors.Is(err, storage.ErrPhase) {
				return reject(4)
			}
			return err
		}
		g.ordinary.Run, g.nextActor = run, next
		return g.packets(frames)
	}
	var storm *storage.StormAdmission
	if def.Admission == "storm" {
		storm = &storage.StormAdmission{Policy: "LocalStormLaneRoutes/v2", Character: c.ID, RunId: run.ID,
			Requested: req.Dungeon, Dungeon: def.Dungeon.ID, Seed: seed, ResourceHash: def.Dungeon.ResourceHash}
	}
	var fatigue storage.DungeonFatigue
	if def.Admission == "wisdom" {
		at := time.Now()
		saved, bag, err := g.admitWisdom(ctx, c, run, at)
		if errors.Is(err, storage.ErrWisdomTickets) {
			body, err := wisdomTicketNotice(def.Dungeon.EntryTicketCount)
			if err != nil {
				return err
			}
			return g.packets([]opening.Packet{
				{Class: 1, Opcode: 16, Body: []byte{0, 4, 0, 0}},
				{Opcode: 488, Body: body},
			})
		}
		if errors.Is(err, storage.ErrFunds) {
			return reject(22)
		}
		if wisdomRejected(err) {
			return reject(4)
		}
		if err != nil {
			return err
		}
		fatigue = saved.Fatigue
		run.WisdomEntry = &saved.Entry
		for i, frame := range frames {
			if frame.Class == 1 && frame.Opcode == 16 {
				frames = slices.Insert(frames, i+1, opening.Packet{Opcode: 13, Body: bag})
				break
			}
		}
	} else if def.Dungeon.Abaddon {
		entry := storage.AbaddonEntry{Policy: storage.AbaddonPolicy, Character: c.ID, Run: run.ID, Dungeon: def.Dungeon.ID, Seed: seed, Hash: def.Dungeon.ResourceHash}
		entry, fatigue, err = g.server.DB.AdmitAbaddon(ctx, g.lease, c, entry, time.Now())
		if err == nil {
			run.AbaddonEntry = &entry
			view, viewErr := g.abaddonRosterPackets(ctx)
			if viewErr != nil {
				return viewErr
			}
			for i, frame := range frames {
				if frame.Class == 1 && frame.Opcode == 16 {
					frames = append(append(frames[:i+1:i+1], view...), frames[i+1:]...)
					break
				}
			}
		}
	} else {
		fatigue, err = g.server.DB.ChargeDungeonFatigue(ctx, g.lease, c, time.Now(), def.Dungeon.EntryFatigueCost, true, dungeonLuck(catalog, run), storm, run.RubyEntry)
	}
	if errors.Is(err, storage.ErrFunds) {
		return reject(22)
	}
	if errors.Is(err, storage.ErrPhase) {
		return reject(4)
	}
	if err != nil {
		return err
	}
	a.FatigueLimit, a.FatigueUsed = fatigue.Limit, fatigue.Limit-fatigue.Remaining
	for i, frame := range frames {
		if frame.Class == 0 && frame.Opcode == 36 {
			frames[i].Body = protocol.Fatigue(a)
		}
	}
	g.ordinary.Run, g.nextActor = run, next
	g.ordinary.WisdomExit, g.ordinary.CardWindowRetired = false, false
	if rubyLedger != nil {
		view, err := g.rubyProgressFrames(c, *rubyLedger, run)
		if err != nil {
			return err
		}
		frames = append(view, frames...)
	}
	return g.packets(frames)
}

func (g *game) dungeonAccess(c storage.Character) (dungeon.Qualification, error) {
	initialized, active, cleared, err := g.server.Characters.Quests.DungeonQualification(c)
	return dungeon.Qualification{Level: c.Level, Initialized: initialized, Active: active, Cleared: cleared}, err
}

func (g *game) dungeonFame(ctx context.Context, c storage.Character) (int64, error) {
	var stats struct{ Values map[string]int64 }
	if err := json.Unmarshal(c.Stats, &stats); err != nil {
		return 0, err
	}
	if fame, ok := stats.Values["fame"]; ok {
		return max(fame, 0), nil
	}
	if c.Level < 100 {
		return 0, nil
	}
	in, err := g.server.DB.Inventory(ctx, g.lease)
	if err != nil {
		return 0, err
	}
	var fame int64
	for _, row := range in.Items {
		if row.Location != 3 || row.CharacterID != c.ID {
			continue
		}
		var t struct{ MinimumLevel byte }
		var a struct {
			Reinforcement, Forge byte
			AmplificationValue   uint16
		}
		if json.Unmarshal(row.Template, &t) != nil || json.Unmarshal(row.Attributes, &a) != nil {
			return 0, storage.ErrInvalid
		}
		if t.MinimumLevel >= 100 {
			fame += 650
		}
		fame += (int64(a.Reinforcement) + int64(a.Forge) + int64(a.AmplificationValue)) * 40
	}
	return fame, nil
}

func (g *game) dungeonTownMotion(ctx context.Context, body []byte) error {
	if !g.ordinary.Returning() {
		return storage.ErrPhase
	}
	p, err := protocol.ReadPosition(body)
	if err != nil {
		return err
	}
	if err := g.move(ctx, p.X, p.Y, p.Speed); err != nil {
		return err
	}
	roster, err := g.server.DB.Current(ctx, g.lease)
	if err != nil {
		return err
	}
	c, err := selected(roster)
	if err != nil {
		return err
	}
	in, err := g.server.DB.Inventory(ctx, g.lease)
	if err != nil {
		return err
	}
	a, profile, err := readActor(c, g.channel)
	if err != nil {
		return err
	}
	m0, err := g.actorMode0(a)
	if err != nil {
		return err
	}
	m1, err := protocol.Mode1(a, false)
	if err != nil {
		return err
	}
	full, err := protocol.Mode1(a, true)
	if err != nil {
		return err
	}
	ordinary, special, err := inventorywire.Town(in, c, inventorywire.Options{
		Now: time.Now(), CashPolicies: g.server.Characters.CashPolicies, AccountPurchases: in.AccountPurchases,
	})
	if err != nil {
		return err
	}
	mail, err := mailwire.Town(c)
	if err != nil {
		return err
	}
	_, skills, err := skillPackets(profile)
	if err != nil {
		return err
	}
	quests, err := g.questMenu(ctx, c, in)
	if err != nil {
		return err
	}
	frames := []opening.Packet{{Opcode: 2, Body: m0}, {Opcode: 2, Body: m1}}
	for _, p := range ordinary {
		frames = append(frames, opening.Packet{Opcode: p.Opcode, Body: p.Body})
	}
	frames = append(frames, opening.Packet{Opcode: 2, Body: full})
	for _, p := range special {
		frames = append(frames, opening.Packet{Opcode: p.Opcode, Body: p.Body})
	}
	for _, p := range mail {
		frames = append(frames, opening.Packet{Opcode: p.Opcode, Body: p.Body})
	}
	frames = append(frames, opening.Packet{Opcode: 19, Body: skills})
	for _, p := range quests {
		frames = append(frames, opening.Packet{Opcode: p.Opcode, Body: p.Body})
	}
	frames = append(frames, opening.Packet{Opcode: 120, Body: []byte{0, 0}})
	g.ordinary.FinishReturn()
	g.questCommunicatorReplay = questwire.CommunicatorReplay{}
	if err := g.packets(frames); err != nil {
		return err
	}
	return g.autoCompleteDeliveryChildren(ctx)
}
