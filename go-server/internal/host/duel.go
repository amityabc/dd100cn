package host

import (
	"context"
	"encoding/binary"
	"math"

	"s9a1/server/internal/duellobby"
	"s9a1/server/internal/protocol"
	"s9a1/server/internal/resources"
	"s9a1/server/internal/storage"
)

type duelRoad struct{ from, to storage.Position }

func (g *game) syncDuel(ctx context.Context, op uint16, r storage.Roster, c storage.Character) error {
	if r.Phase != storage.Selected {
		return storage.ErrPhase
	}
	if g.duelPosition == nil {
		var err error
		c, err = g.server.DB.EnsureDuelSkills(ctx, g.lease, c.ID, duellobby.InitialSkills)
		if err != nil {
			return err
		}
	}
	display, err := duellobby.Display(c)
	if err != nil {
		return err
	}
	ready := g.duelPosition != nil
	pos := duellobby.Spawn()
	if ready {
		pos = *g.duelPosition
	}
	display.Position = pos
	a, p, err := readActor(display, g.channel)
	if err != nil {
		return err
	}
	packets, err := duellobby.Sync(ready, op, a, r.Cera, g.server.native140, g.server.nativeParty)
	if err != nil {
		return err
	}
	locks, skills, err := skillPackets(p)
	if err != nil {
		return err
	}
	if op == 120 && g.server.Characters.Fate != nil {
		if err = g.send(0, 108, fateEvents()); err != nil {
			return err
		}
	}
	g.duelPosition = &pos
	if err = g.packets(packets); err != nil {
		return err
	}
	if ready {
		if len(p.CommandLocks) == 512 {
			first, second := binary.LittleEndian.Uint16(p.CommandLocks), binary.LittleEndian.Uint16(p.CommandLocks[2:])
			if first == 1 || first == 2 || second == 1 || second == 2 {
				return g.send(0, 376, locks)
			}
		}
		return nil
	}
	in, err := g.server.DB.Inventory(ctx, g.lease)
	if err != nil {
		return err
	}
	quests, err := g.questMenu(ctx, c, in)
	if err != nil {
		return err
	}
	if err = g.projected(quests); err != nil {
		return err
	}
	if err = g.send(0, 376, locks); err != nil {
		return err
	}
	return g.send(0, 19, skills)
}

func (g *game) moveDuel(r storage.Roster, x, y uint16) error {
	if r.Phase != storage.Selected || g.duelPosition == nil {
		return storage.ErrPhase
	}
	c, err := selected(r)
	if err != nil {
		return err
	}
	q, err := resources.ReadTownQuests(c.Gameplay)
	if err != nil {
		return err
	}
	dest := *g.duelPosition
	dest.X, dest.Y = x, y
	if !g.server.Characters.AllowsTownPosition(c.Level, q, point(dest)) {
		return storage.ErrInvalid
	}
	g.duelPosition = &dest
	return nil
}

func (g *game) travelDuel(r storage.Roster, req protocol.TownChange, reject func() error) error {
	if g.channel != 252 || r.Phase != storage.Selected || g.duelPosition == nil || req.NativeNPC || req.Kind != 0 || req.TargetNPC != 0 ||
		req.Town != 10 || req.PreviousTown != 10 || req.Direction > 7 || req.X > math.MaxInt16 || req.Y > math.MaxInt16 {
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
	src := *g.duelPosition
	to := storage.Position{Town: 10, Area: uint16(req.Area), X: req.X, Y: req.Y, Direction: req.Direction}
	replay := g.duelLast != nil && g.duelLast.to == to && src == to && uint16(req.PreviousArea) == g.duelLast.from.Area
	if replay {
		src = g.duelLast.from
	} else if src.Town != 10 || src.Area != uint16(req.PreviousArea) {
		return reject()
	}
	catalog := g.server.Characters
	allowed := false
	if to == (storage.Position{Town: 10, Area: 4, X: 550, Y: 300, Direction: 5}) {
		allowed = catalog.AllowsTownPosition(c.Level, q, point(src)) && catalog.AllowsTownPosition(c.Level, q, point(to))
	} else {
		allowed = catalog.AllowsTravel(c.Level, q, point(src), point(to), 0, 0, false)
	}
	if !allowed {
		return reject()
	}
	g.duelPosition = &to
	if !replay {
		g.duelLast = &duelRoad{src, to}
	}
	a := protocol.Actor{RosterCharacter: protocol.RosterCharacter{ID: c.ID}, Town: 10, Area: req.Area, X: req.X, Y: req.Y, Direction: req.Direction}
	if err = g.send(0, 23, protocol.Area(a)); err != nil {
		return err
	}
	return g.send(0, 24, protocol.Town(a))
}
