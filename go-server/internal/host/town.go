package host

import (
	"context"
	"encoding/binary"

	"s9a1/server/internal/duellobby"
	"s9a1/server/internal/resources"
	"s9a1/server/internal/storage"
)

func point(p storage.Position) resources.TownPoint {
	return resources.TownPoint{Town: p.Town, Area: p.Area, X: p.X, Y: p.Y, Direction: p.Direction}
}
func storedPoint(p resources.TownPoint) storage.Position {
	return storage.Position{Town: p.Town, Area: p.Area, X: p.X, Y: p.Y, Direction: p.Direction}
}

func (g *game) loginPlacement(c storage.Character, p savedGameplay) (*storage.SelectionPlacement, error) {
	if p.Tutorial != nil && !p.Tutorial.Completed {
		return nil, nil
	}
	q, err := resources.ReadTownQuests(c.Gameplay)
	if err != nil {
		return nil, err
	}
	location := point(c.Position)
	var previous *resources.TownPoint
	if c.RoomReturn != nil {
		p := point(*c.RoomReturn)
		previous = &p
	}
	t := g.catalog().Towns
	home, exit := t.LoginPlacement(c.Level, q, location, previous)
	if home == nil {
		return nil, nil
	}
	plan := &storage.SelectionPlacement{CharacterID: c.ID, Revision: c.Revision, Home: storedPoint(*home), InRoom: t.IsRoom(location)}
	if exit != nil {
		if (previous == nil || *previous != *exit) && (exit.Direction > 7 || t.IsRoom(*exit)) {
			return nil, storage.ErrInvalid
		}
		ret := storedPoint(*exit)
		plan.ReturnPoint = &ret
	}
	return plan, nil
}

func selected(r storage.Roster) (storage.Character, error) {
	for _, c := range r.Characters {
		if c.ID == r.SelectedID {
			return c, nil
		}
	}
	return storage.Character{}, storage.ErrPhase
}

func (g *game) move(ctx context.Context, x, y, speed uint16) error {
	r, err := g.server.DB.Current(ctx, g.lease)
	if err != nil {
		return err
	}
	if duellobby.IsChannel(g.channel) {
		return g.moveDuel(r, x, y)
	}
	if g.channel == 63 {
		return g.moveOculus(r, x, y)
	}
	if r.Phase != storage.TownReady || g.channel != 19 {
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
	next := c.Position
	next.X, next.Y, next.WireTail = x, y, speed
	t := g.catalog().Towns
	if !g.catalog().AllowsTownMovement(c.Level, q, point(c.Position), point(next)) {
		return storage.ErrInvalid
	}
	gameplay, err := t.Arrival(c.Gameplay, c.Level, q, point(next))
	if err != nil {
		return err
	}
	_, err = g.server.DB.SaveTown(ctx, g.lease, c.ID, c.Revision, next, gameplay)
	if err != nil {
		return err
	}
	g.gaugePending = false
	if err = g.arrivalNotices(t, q, gameplay); err != nil || len(q.Active) == 0 {
		return err
	}
	return g.autoCompleteDeliveryChildren(ctx)
}

func (g *game) arrivalNotices(t *resources.TownCatalog, q resources.TownQuests, gameplay []byte) error {
	if gameplay != nil {
		newQuests, err := resources.ReadTownQuests(gameplay)
		if err != nil {
			return err
		}
		for _, id := range t.NewArrivals(q, newQuests) {
			b := []byte{1, 0, 0, 0, 0, 0, 0}
			binary.LittleEndian.PutUint16(b[1:], id)
			if err = g.send(1, 33, b); err != nil {
				return err
			}
		}
	}
	return nil
}
