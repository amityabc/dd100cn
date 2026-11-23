package host

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"

	"s9a1/server/internal/duellobby"
	"s9a1/server/internal/protocol"
	"s9a1/server/internal/questwire"
	"s9a1/server/internal/storage"
)

func (g *game) arcadeRequest(ctx context.Context, f protocol.Frame) error {
	var create protocol.ArcadeCreate
	var report protocol.ArcadeRound
	var err error
	switch f.Opcode {
	case 50:
		create, err = protocol.ReadArcadeCreate(f.Body)
	case 52:
		if len(f.Body) != 2 {
			return errors.New("invalid arcade seat request")
		}
	case 1279:
		report, err = protocol.ReadArcadeRound(f.Body)
	}
	if err != nil {
		return err
	}
	r, err := g.server.DB.Current(ctx, g.lease)
	if err != nil {
		return err
	}
	p := g.duelPosition
	entrance := p != nil && p.Town == 10 && p.Area == 4 && g.channel == 252
	lobby := p != nil && p.Town == 10 && p.Area == 0 && duellobby.IsChannel(g.channel)
	c, err := selected(r)
	if err != nil || r.Phase != storage.Selected || c.Level < duellobby.MinimumLevel || !g.server.delegated || !(entrance || lobby) || g.opening != nil || g.ordinary.Active() {
		if f.Opcode == 50 {
			return g.send(1, 50, []byte{0, 0, 0})
		}
		return nil
	}
	room := g.arcade
	if !room.Owned(g.lease, c.ID) {
		room = nil
	}
	switch f.Opcode {
	case 50:
		return g.createArcade(c, room, entrance, create, f.Body)
	case 52:
		if room == nil || f.Body[0] != 0 || f.Body[1] != 254 {
			return nil
		}
		if !room.Closed {
			room.Closed, room.Start = true, nil
			if err := g.send(0, 42, protocol.ArcadeRoomState(room.ID, room.Mode, 1)); err != nil {
				return err
			}
		}
		if err := g.send(0, 43, protocol.ArcadeLeave(room.ID, room.Mode)); err != nil {
			return err
		}
		if err := g.send(0, 42, protocol.ArcadeRoomState(room.ID, room.Mode, 0)); err != nil {
			return err
		}
		a := protocol.Actor{RosterCharacter: protocol.RosterCharacter{ID: c.ID}, Town: byte(p.Town), Area: byte(p.Area), X: p.X, Y: p.Y, Direction: p.Direction}
		if err := g.send(0, 23, protocol.Area(a)); err != nil {
			return err
		}
		return g.send(0, 24, protocol.Town(a))
	case 1279:
		room.Report(report, func() bool {
			d, err := g.catalog().Quests.DuelObjective(c, room.Training.ID)
			return err == nil && entrance && d.ID == room.Training.ID && d.Hash == room.Training.Hash &&
				d.Acceptance == room.Training.Acceptance && d.StageReady(room.Training.Stage)
		})
	}
	return nil
}

func (g *game) createArcade(c storage.Character, previous *duellobby.Room, entrance bool, request protocol.ArcadeCreate, body []byte) error {
	reject := func() error { return g.send(1, 50, []byte{0, 0, 0}) }
	practice := request.Kind == 1
	if !request.Supported || !entrance && !practice {
		return reject()
	}
	quests := g.catalog().Quests
	advanced := previous != nil && previous.Mode == protocol.PracticeMode && previous.Training.Stage == 1 && !practice &&
		quests.PracticeAdvanced(c, previous.Training.ID, previous.Training.Hash, previous.Training.Acceptance)
	if reuse, blocked := previous.Existing(body, advanced); blocked {
		if reuse {
			return g.send(1, 50, protocol.Ack())
		}
		return reject()
	}
	profile, err := readGameplay(c.Gameplay)
	if err != nil {
		return err
	}
	next := &duellobby.Room{Owner: g.lease, Character: c.ID, ID: 1, Mode: protocol.ArcadeMode, CreateBody: bytes.Clone(body)}
	arcade := g.catalog().Arcade
	var objective questwire.DuelObjective
	if entrance {
		objective, err = quests.DuelObjective(c, 0)
		if err != nil {
			return reject()
		}
	}
	if practice {
		next.Mode = protocol.PracticeMode
	}
	if objective.ID != 0 {
		stage := byte(2)
		rounds := byte(2)
		if objective.ID == 2674 {
			rounds = 4
		}
		if practice {
			if objective.ID == 2673 {
				return reject()
			}
			stage, next.PracticeIndex, next.Qualified = 1, 12, true
			if objective.ID == 2674 {
				next.PracticeIndex = 13
			}
		} else if objective.ID == 2673 {
			stage = 0
		}
		next.Training = duellobby.TrainingStage{ID: objective.ID, Hash: objective.Hash, Acceptance: objective.Acceptance, Stage: stage}
		d := arcade.Get(c.Job, profile.Progression.GrowType, byte(80+objective.ID-2672))
		if !objective.StageReady(stage) || d == nil || d.Rounds != rounds || !practice && d.Difficulty != request.Difficulty {
			return reject()
		}
		next.Definition = d
	} else if practice {
		if entrance {
			return reject()
		}
	} else {
		next.Definition = arcade.Get(c.Job, profile.Progression.GrowType, request.Difficulty)
		if next.Definition == nil {
			return reject()
		}
	}
	if previous != nil {
		if previous.ID == 65535 {
			return reject()
		}
		next.ID = previous.ID + 1
	}
	row, err := protocol.ArcadeRoom(next.ID, c.ID, next.Mode)
	if err != nil {
		return err
	}
	g.arcade = next
	if err = g.send(1, 50, protocol.Ack()); err != nil {
		return err
	}
	if err = g.send(0, 41, row); err != nil {
		return err
	}
	if previous != nil && !previous.Closed {
		return g.send(0, 42, protocol.ArcadeRoomState(previous.ID, previous.Mode, 0))
	}
	return nil
}

func (g *game) duelTrainingTrigger(c storage.Character, in storage.Inventory, q *questwire.Quest, req questwire.TriggerRequest) (json.RawMessage, error) {
	pos := g.duelPosition
	if !g.server.delegated || g.channel != 252 || pos == nil || pos.Town != 10 || pos.Area != 4 || c.Level < duellobby.MinimumLevel {
		return nil, storage.ErrInvalid
	}
	return questwire.AdvanceDuelStage(c, in, q, req.Trigger, req.Increment, req.HasExplicitTrigger, func(d questwire.DuelObjective, stage byte) bool {
		return g.arcade.Qualifies(g.lease, c.ID, duellobby.TrainingStage{ID: d.ID, Hash: d.Hash, Acceptance: d.Acceptance, Stage: stage})
	})
}
