package host

import (
	"context"
	"errors"

	"s9a1/server/internal/inventorymove"
	"s9a1/server/internal/inventorywire"
	"s9a1/server/internal/protocol"
	"s9a1/server/internal/storage"
)

func (g *game) moveItem(ctx context.Context, f protocol.Frame) error {
	req, err := inventorymove.Parse(f.Sequence, f.Body)
	if err != nil {
		return err
	}
	if storage.SpecialMoveRoute(req) {
		return g.moveSpecialItem(ctx, req, f.Body)
	}
	container := func(list byte) bool { return list == 2 || list == 7 || list == 12 || list == 38 }
	if container(req.SourceList) || container(req.DestinationList) ||
		req.SourceList == 0 && req.SourceSlot >= 363 && req.SourceSlot <= 374 ||
		req.DestinationList == 0 && req.DestinationSlot >= 363 && req.DestinationSlot <= 374 {
		return g.moveContainerItem(ctx, req, f.Body)
	}
	r, err := g.server.DB.Snapshot(ctx, g.lease)
	if err != nil {
		return err
	}
	run := g.ordinary.Run
	if r.Phase != storage.TownReady || r.SelectedID == 0 || g.channel != 19 || g.ordinary.Returning() ||
		g.opening != nil && !g.opening.Ready() || run != nil && (run.Character != r.SelectedID || !run.CanChangeEquipment()) {
		return g.send(1, 19, inventorymove.Rejected(req))
	}
	c, err := selected(r)
	if err != nil {
		return err
	}
	if g.opening != nil || run != nil {
		p, err := readGameplay(c.Gameplay)
		if err != nil {
			return err
		}
		swap := req.SourceList == 0 && req.DestinationList == 3 || req.SourceList == 3 && req.DestinationList == 0
		if !swap || !p.Initialized || p.Progression.Hp == 0 {
			return g.send(1, 19, inventorymove.Rejected(req))
		}
	}
	out, err := g.server.DB.Move(ctx, g.lease, c.ID, c.Revision, req, g.catalog().Weights)
	if err != nil {
		if errors.Is(err, storage.ErrInvalid) || errors.Is(err, storage.ErrPhase) || errors.Is(err, storage.ErrCapacity) {
			return g.send(1, 19, inventorymove.Rejected(req))
		}
		return err
	}
	ordinary, err := inventorywire.BagAndWear(out.Inventory, out.Character)
	if err != nil {
		return err
	}
	if err = g.send(1, 19, inventorymove.Accepted(out)); err != nil {
		return err
	}
	if err = g.projected(ordinary); err != nil {
		return err
	}
	if g.opening != nil || run != nil {
		return nil
	}
	a, _, err := readActor(out.Character, g.channel)
	if err != nil {
		return err
	}
	b, err := protocol.Mode1(a, true)
	if err != nil {
		return err
	}
	if err = g.send(0, 2, b); err != nil {
		return err
	}
	if out.SourceList == 3 && out.SourceSlot == 12 || out.DestinationList == 3 && out.DestinationSlot == 12 {
		b, err = g.actorMode0(a)
		if err != nil {
			return err
		}
		if err = g.send(0, 2, b); err != nil {
			return err
		}
	}
	g.gaugePending = true
	return nil
}
