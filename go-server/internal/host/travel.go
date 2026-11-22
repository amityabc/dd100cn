package host

import (
	"context"
	"errors"
	"math"

	"s9a1/server/internal/duellobby"
	"s9a1/server/internal/protocol"
	"s9a1/server/internal/resources"
	"s9a1/server/internal/storage"
)

func (g *game) travel(ctx context.Context, op uint16, body []byte) error {
	catalog := g.server.Characters
	roomReturn := op == 1418
	var req protocol.TownChange
	var err error
	if roomReturn {
		if len(body) != 0 {
			return errors.New("room return body must be empty")
		}
	} else {
		var camp, npc uint32
		if f := catalog.Fate; f != nil {
			camp, npc = uint32(f.Town.Camp.Town), f.Town.CampNpc
		}
		req, err = protocol.ReadTownChange(body, catalog.Fate != nil, camp, npc)
		if err != nil {
			return err
		}
		if req.ReturnSentinel {
			p := catalog.Fate.Town.Seria
			req.Town, req.Area, req.X, req.Y, req.Direction = uint32(p.Town), byte(p.Area), p.X, p.Y, p.Direction
		}
	}
	reject := func() error {
		if roomReturn {
			return g.send(1, 1418, protocol.Failure(0))
		}
		return g.send(1, 36, protocol.TownChangeRejected(req.Town, req.Area))
	}
	r, err := g.server.DB.Current(ctx, g.lease)
	if err != nil {
		return err
	}
	if duellobby.IsChannel(g.channel) && !roomReturn {
		return g.travelDuel(r, req, reject)
	}
	if r.Phase != storage.TownReady || r.SelectedID == 0 || g.channel != 19 || g.opening != nil || req.Town > 255 || req.PreviousTown > 255 || req.Direction > 7 || req.X > math.MaxInt16 || req.Y > math.MaxInt16 {
		return reject()
	}
	c, err := selected(r)
	if err != nil {
		return err
	}
	q, err := resources.ReadTownQuests(c.Gameplay)
	if err != nil {
		return err
	}
	from := point(c.Position)
	t := storage.TownTransition{RoomReturn: roomReturn, NativeNPC: req.NativeNPC, TransitionKind: req.Kind, TargetNPCID: req.TargetNPC}
	var to resources.TownPoint
	if roomReturn {
		t.From = c.Position
		t.ReplayOnly = !catalog.Towns.IsRoom(from)
		if !t.ReplayOnly {
			var saved *resources.TownPoint
			if c.RoomReturn != nil {
				p := point(*c.RoomReturn)
				saved = &p
			}
			exit, ok := catalog.RoomExit(c.Level, q, from, saved)
			if !ok {
				return reject()
			}
			to = *exit
		}
	} else {
		to = resources.TownPoint{Town: uint16(req.Town), Area: uint16(req.Area), X: req.X, Y: req.Y, Direction: req.Direction}
		t.From = storage.Position{Town: uint16(req.PreviousTown), Area: uint16(req.PreviousArea)}
		t.ReplayOnly = from.Town != t.From.Town || from.Area != t.From.Area
		if !t.ReplayOnly && !catalog.AllowsTravel(c.Level, q, from, to, req.Kind, req.TargetNPC, req.NativeNPC) {
			return reject()
		}
		t.RecordOrigin = catalog.Towns.IsRoom(to) && !(from.Town == 117 && from.Area == 1)
	}
	t.To = storedPoint(to)
	t.To.WireTail = c.Position.WireTail
	if !t.ReplayOnly {
		t.Gameplay, err = catalog.Towns.Arrival(c.Gameplay, c.Level, q, to)
		if err != nil {
			return err
		}
	}
	change, err := g.server.DB.TransitionTown(ctx, g.lease, c.ID, c.Revision, t)
	if errors.Is(err, storage.ErrInvalid) || errors.Is(err, storage.ErrPhase) {
		return reject()
	}
	if err != nil {
		return err
	}
	if change.Replayed && req.NativeNPC && !catalog.AllowsTravel(c.Level, q, point(change.From), to, req.Kind, req.TargetNPC, true) {
		return reject()
	}
	actor, _, err := readActor(change.Character, g.channel)
	if err != nil {
		return err
	}
	if roomReturn {
		if err = g.send(1, 1418, []byte{1, 255, 255, 255, 255}); err != nil {
			return err
		}
	} else if req.NativeNPC {
		if err = g.send(1, 36, protocol.Ack()); err != nil {
			return err
		}
	}
	if !req.NativeNPC {
		if err = g.send(0, 23, protocol.Area(actor)); err != nil {
			return err
		}
	}
	if err = g.send(0, 24, protocol.Town(actor)); err != nil {
		return err
	}
	if change.Replayed {
		return nil
	}
	g.gaugePending = false
	if req.NativeNPC {
		return nil
	}
	return g.arrivalNotices(catalog.Towns, q, t.Gameplay)
}
