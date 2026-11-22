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
	r, err := g.server.DB.Snapshot(ctx, g.lease)
	if err != nil {
		return err
	}
	if r.Phase != storage.TownReady || r.SelectedID == 0 || g.channel != 19 || g.opening != nil && !g.opening.Ready() {
		return g.send(1, 19, inventorymove.Rejected(req))
	}
	c, err := selected(r)
	if err != nil {
		return err
	}
	if g.opening != nil {
		p, err := readGameplay(c.Gameplay)
		if err != nil {
			return err
		}
		swap := req.SourceList == 0 && req.DestinationList == 3 || req.SourceList == 3 && req.DestinationList == 0
		if !swap || !p.Initialized || p.Progression.Hp == 0 {
			return g.send(1, 19, inventorymove.Rejected(req))
		}
	}
	out, err := g.server.DB.Move(ctx, g.lease, c.ID, c.Revision, req, g.server.Characters.Weights)
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
	if g.opening != nil {
		return nil
	}
	a, p, err := readActor(out.Character, g.channel)
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
		b, err = protocol.Mode0(a, g.server.native140)
		if err != nil {
			return err
		}
		if err = g.send(0, 2, b); err != nil {
			return err
		}
	}
	if p.BuffRegistration != nil && len(p.BuffRegistration.Items) > 0 {
		buff, err := buffReset(p, out.Inventory)
		if err != nil {
			return err
		}
		if err = g.send(0, 1360, buff[2:]); err != nil {
			return err
		}
	}
	g.gaugePending = true
	return nil
}
