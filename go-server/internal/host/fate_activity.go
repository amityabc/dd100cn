package host

import (
	"context"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"math"
	"slices"
	"time"

	"s9a1/server/internal/dungeon"
	"s9a1/server/internal/fate"
	"s9a1/server/internal/opening"
	"s9a1/server/internal/protocol"
	"s9a1/server/internal/resources"
	"s9a1/server/internal/storage"
)

type fateSession struct {
	Character    uint16
	Position     resources.TownPoint
	Presets      []int
	Run          string
	ResultSent   bool
	ExitBody     string
	ExitRevision int64
}

func (g *game) fateIntercept(f protocol.Frame) bool {
	s := g.fateSession
	active := s.Run != "" && g.ordinary.Run != nil && g.ordinary.Run.ID == s.Run
	if f.Opcode == 72 && s.ExitBody != "" && !g.ordinary.Active() {
		return true
	}
	if active {
		switch f.Opcode {
		case 18:
			return len(f.Body) <= 16
		case 46, 69, 70, 71, 117, 449, 681, 1894, 1895, 1896, 1929, 1930:
			return true
		}
	}
	if len(s.Presets) == 0 {
		return false
	}
	switch f.Opcode {
	case 15, 16, 37, 39, 40, 41, 42, 43, 45, 72, 132:
		return true
	}
	return false
}

type fateAction struct {
	Session                                  fateSession
	NewRun                                   *dungeon.Run
	NextActor                                uint16
	Load, Return, RemoveBuff, Mail, Replayed bool
	NextScene                                uint32
	Drop                                     *fate.Drop
	Slot                                     uint16
	Entry                                    []opening.Packet
}
type fateEdit struct {
	Character       storage.Character
	Inventory       storage.Inventory
	Profile         fate.Profile
	Prior, Receipts map[string]json.RawMessage
	Changed         bool
}

func (e *fateEdit) save() error {
	p := e.Profile
	b, err := fate.PatchFate(e.Character.Gameplay, map[string]any{"Run": p.Run, "ArchangelPoints": p.ArchangelPoints, "StoryWeek": p.StoryWeek, "StoryCount": p.StoryCount, "DailyClaims": p.DailyClaims, "FullClears": p.FullClears})
	if err == nil {
		e.Character.Gameplay = b
		e.Changed = true
	}
	return err
}
func (e *fateEdit) health(restore bool) error {
	c, err := storage.ActivityHealth(e.Character, restore)
	if err == nil {
		e.Character = c
		e.Changed = true
	}
	return err
}

func (g *game) fateActivity(ctx context.Context, f protocol.Frame) error {
	def := g.catalog().Fate
	if def == nil || g.opening != nil || g.ordinary.Returning() && !(f.Opcode == 72 && g.fateSession.ExitBody != "") {
		return g.rejectFate(f)
	}
	next := fateAction{Session: g.fateSession}
	now := time.Now()
	out, err := g.server.DB.ChangeFateActivity(ctx, g.lease, g.catalog().Weights, true, func(c storage.Character, in storage.Inventory, prior map[string]json.RawMessage) (storage.TownQuestMutation, error) {
		var m storage.TownQuestMutation
		profile, err := fate.Read(c.Gameplay)
		if err != nil {
			return m, err
		}
		if err = def.Validate(profile); err != nil {
			return m, err
		}
		e := fateEdit{Character: c, Inventory: in, Profile: profile, Prior: prior}
		p, err := readGameplay(c.Gameplay)
		if err != nil {
			return m, err
		}
		s := next.Session
		r := g.ordinary.Run
		authorized := s.Character == c.ID && len(s.Presets) == 10
		active := authorized && r != nil && r.ID == s.Run && r.Character == c.ID && profile.Run != nil && profile.Run.RunId == s.Run
		loaded := active && r.Ready
		camp := c.Level >= def.MinimumLevel && c.Position.Town == def.Town.Camp.Town && c.Position.Area == def.Town.Camp.Area
		switch f.Opcode {
		case 1918:
			if len(f.Body) != 0 {
				return m, fate.ErrPacket
			}
			if !p.Initialized || !camp || r != nil {
				return m, fate.ErrInvalid
			}
			if profile.Run != nil && !profile.Run.Settled {
				if err = e.settle(&def.Definition, now, true, "reconnect-forfeit", &next); err != nil {
					return m, err
				}
				if err = e.health(true); err != nil {
					return m, err
				}
			} else if profile.Run != nil && profile.Run.Settled && profile.Run.Dead && p.Progression.Hp == 0 {
				if err = e.health(true); err != nil {
					return m, err
				}
			} else if p.Progression.Hp == 0 {
				return m, fate.ErrInvalid
			}
			if !authorized || s.Position != point(c.Position) {
				presets, err := def.ChoosePresets()
				if err != nil {
					return m, err
				}
				next.Session = fateSession{Character: c.ID, Position: point(c.Position), Presets: presets}
			}
		case 15:
			target, err := dungeon.ReadSelector(f.Body)
			if err != nil {
				return m, err
			}
			if !camp || !authorized || active || target != 0 && target != def.DungeonId {
				return m, fate.ErrInvalid
			}
		case 16:
			req, err := dungeon.ReadEntry(f.Body)
			if err != nil {
				return m, err
			}
			if !camp || !authorized || s.Position != point(c.Position) || req.Dungeon != def.DungeonId || !req.Supported() || req.Difficulty != 0 || req.ChallengeStage != 0 {
				return m, fate.ErrInvalid
			}
			if active {
				if r.Ready {
					return m, fate.ErrInvalid
				}
				next.Replayed = true
				break
			}
			if r != nil || profile.Run != nil && !profile.Run.Settled || !p.Initialized || p.Progression.Hp == 0 {
				return m, fate.ErrInvalid
			}
			a, _, err := readActor(c, g.channel)
			if err != nil {
				return m, err
			}
			seed, err := fate.Random(math.MaxInt32)
			if err != nil {
				return m, err
			}
			d := dungeon.Definition{Gate: def.Town.Gate}
			d.Dungeon.Dungeon = def.LoadingDungeon
			d.Dungeon.DisableExperience = true
			d.Dungeon.DisableDrops = true
			d.Dungeon.DisableClearReward = true
			first := g.nextActor
			if first == 0 {
				first = 402
			}
			base, _, err := (opening.Definition{Dungeon: def.LoadingDungeon}).Reserve(first)
			if err != nil {
				return m, err
			}
			next.NewRun, next.NextActor, err = dungeon.Prepare(d, req, a, first, uint32(seed))
			if err != nil {
				return m, err
			}
			next.Entry, err = def.EntryPackets(a, uint32(seed), base)
			if err != nil {
				return m, err
			}
			e.Profile.Run, err = def.Start(next.NewRun.ID, s.Presets)
			if err != nil {
				return m, err
			}
			next.Session.Run = next.NewRun.ID
			next.Session.ResultSent = false
			next.Session.ExitBody = ""
			if err = e.save(); err != nil {
				return m, err
			}
		case 37:
			if len(f.Body) != 8 && len(f.Body) != 16 {
				return m, fate.ErrPacket
			}
			if !active {
				return m, fate.ErrInvalid
			}
			next.Load, next.Replayed = true, loaded
		case 132:
			if len(f.Body) != 0 {
				return m, fate.ErrPacket
			}
			if !authorized || active {
				return m, fate.ErrInvalid
			}
			next.Return = true
			next.Session = fateSession{}
			e.Changed = c.Position.State != 0
		case 45:
			if _, _, _, err = dungeon.ReadMove(f.Body); err != nil {
				return m, err
			}
			return m, fate.ErrInvalid
		case 41:
			if len(f.Body) != 2 {
				return m, fate.ErrPacket
			}
			return m, fate.ErrInvalid
		case 39:
			if len(f.Body) < 74 || len(f.Body) != 74+14*int(f.Body[30]) {
				return m, fate.ErrPacket
			}
			// Client-created waves report native deaths without ordinary combat or clear credit.
			// Only C1919 advances this activity's recorded node.
		case 46, 69, 70, 71, 117, 449, 1929, 1930:
			if !active || len(f.Body) > 1<<20 {
				return m, fate.ErrInvalid
			}
		default:
			if err = g.advanceFate(f, &e, &next, active, loaded, now); err != nil {
				return m, err
			}
		}
		if e.Changed {
			m.Reward = &storage.QuestRewardMutation{Character: e.Character, Inventory: e.Inventory, Receipts: e.Receipts, ClearScene: next.Return}
		}
		return m, nil
	})
	if errors.Is(err, fate.ErrInvalid) || errors.Is(err, storage.ErrPhase) || errors.Is(err, storage.ErrCapacity) || errors.Is(err, storage.ErrInvalid) || errors.Is(err, storage.ErrConflict) {
		return g.rejectFate(f)
	}
	if err != nil {
		return err
	}
	if next.NewRun != nil {
		g.ordinary.Run = next.NewRun
		g.nextActor = next.NextActor
		g.fateMutation = fateMutation{}
	}
	if next.Load {
		g.ordinary.Run.Ready = true
	}
	if f.Opcode == 40 && g.ordinary.Run != nil && g.ordinary.Run.PlayerDeath == nil {
		g.ordinary.Run.ApplyPlayerDeath(f.Body)
	}
	if next.Return {
		g.ordinary.BeginReturn()
		if next.Session.ExitBody != "" {
			next.Session.ExitRevision = out.Character.Revision
		}
	}
	if next.NextScene != 0 {
		g.fateScene = next.NextScene
	}
	g.fateSession = next.Session
	return g.projectFate(f, out, next, now)
}
func (g *game) rejectFate(f protocol.Frame) error {
	b := []byte{0, 0, 0}
	switch f.Opcode {
	case 15, 43, 45:
		b = []byte{0, 4, 0}
	case 16:
		b = []byte{0, 4, 0, 0}
	case 18:
		b = []byte{0, 0, 0, 4, 0, 0, 0, 8, 0, 24, 0}
	case 681:
		q, err := fate.ReadControl(f.Body)
		if err != nil {
			return err
		}
		b = fate.ControlAck(false, q.Action == 8)
	case 72:
		if len(f.Body) != 3 {
			return fate.ErrPacket
		}
		b = append(b, f.Body[1])
	case 1895:
		b = []byte{0, 21, 0}
	case 39, 46, 69, 70, 71, 117, 449, 1896, 1929, 1930:
		return nil
	case 132:
		return storage.ErrPhase
	}
	return g.send(1, f.Opcode, b)
}

func fateIdentity(body []byte) string { return base64.StdEncoding.EncodeToString(body) }
func (g *game) advanceFate(f protocol.Frame, e *fateEdit, next *fateAction, active, loaded bool, now time.Time) error {
	d := &g.catalog().Fate.Definition
	r := e.Profile.Run
	if f.Opcode == 72 && next.Session.ExitBody != "" && !g.ordinary.Active() {
		if !slices.Equal(f.Body, []byte{1, 2, 1}) || next.Session.Character != e.Character.ID || next.Session.ExitRevision != e.Character.Revision || next.Session.ExitBody != fateIdentity(f.Body) {
			return fate.ErrInvalid
		}
		next.Replayed = true
		return nil
	}
	if !loaded || !active || r == nil {
		return fate.ErrInvalid
	}
	switch f.Opcode {
	case 1919:
		return g.visitFate(f, e, next, d, now)
	case 43:
		return g.pickupFate(f, e, next, d)
	case 1894:
		if err := fate.ReadResult(f.Body); err != nil {
			return err
		}
		body := fateIdentity(f.Body)
		if r.Settled && r.ResultBody != body && r.ResultBody != "reconnect-forfeit" {
			return fate.ErrInvalid
		}
		next.Session.ResultSent = true
		return e.settle(d, now, false, body, next)
	case 40:
		if len(f.Body) != 6 {
			return fate.ErrPacket
		}
		actor := uint16(f.Body[4]) | uint16(f.Body[5])<<8
		body := fateIdentity(f.Body)
		if actor != 0 && actor != e.Character.ID || r.Settled || r.Dead && r.DeathBody != body {
			return fate.ErrInvalid
		}
		if r.Dead {
			return nil
		}
		r.Dead, r.DeathBody = true, body
		if err := e.health(false); err != nil {
			return err
		}
		return e.save()
	case 42, 72:
		if f.Opcode == 42 {
			if len(f.Body) != 0 {
				return fate.ErrPacket
			}
			if err := e.settle(d, now, true, "return-forfeit", next); err != nil {
				return err
			}
			next.Session = fateSession{}
		} else {
			if len(f.Body) != 3 || f.Body[0] != 1 && f.Body[0] != 2 || f.Body[1] > 3 || f.Body[2] != 1 {
				return fate.ErrPacket
			}
			if f.Body[1] != 2 || !r.Settled || !next.Session.ResultSent || !r.Dead && len(r.CompletedPhases) != 5 {
				return fate.ErrInvalid
			}
			if f.Body[0] == 2 {
				return nil
			}
			next.Session = fateSession{Character: e.Character.ID, ExitBody: fateIdentity(f.Body)}
		}
		next.Return, next.RemoveBuff = true, true
		return e.health(true)
	case 18:
		if err := fate.ReadConsumption(f.Body); err != nil {
			return err
		}
	case 1896:
		if err := fate.ReadAbility(f.Body); err != nil {
			return err
		}
		if binary.LittleEndian.Uint32(f.Body[4:]) > 2 {
			return fate.ErrInvalid
		}
	case 1895:
		if _, _, err := fate.ReadEquip(f.Body); err != nil {
			return err
		}
		return fate.ErrInvalid
	case 681:
		q, err := fate.ReadControl(f.Body)
		if err != nil {
			return err
		}
		switch q.Action {
		case 8:
			if q.Slot != 0 || r.LastNode == nil || !slices.Contains(r.ShopProducts, int(q.ID)) || !d.ShopNode(r) {
				return fate.ErrInvalid
			}
		case 9:
			if q.ID < 0 || q.ID > 1 {
				return fate.ErrInvalid
			}
		default:
			return fate.ErrInvalid
		}
	default:
		return fate.ErrInvalid
	}
	if r.Dead || r.Settled {
		return fate.ErrInvalid
	}
	return nil
}
