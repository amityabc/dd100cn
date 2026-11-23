package host

import (
	"context"
	"encoding/binary"
	"math"
	"time"

	"s9a1/server/internal/inventorywire"
	"s9a1/server/internal/mailwire"
	"s9a1/server/internal/storage"
)

func (g *game) useContainerCoupon(ctx context.Context, op uint16, b []byte) error {
	if len(b) != 2 {
		return storage.ErrInvalid
	}
	slot := binary.LittleEndian.Uint16(b)
	if (slot < 3 || slot > 8) && (slot < 65 || slot > 120) {
		return storage.ErrInvalid
	}
	cargo := op == 400
	catalog := g.catalog()
	reject := func() error {
		if cargo {
			return g.send(1, 400, mailwire.Rejected(0))
		}
		return nil // C422 has no native response consumer.
	}
	if !g.commerceInTown() {
		return reject()
	}
	out, err := g.server.DB.Commerce(ctx, g.lease, op, g.seq, b, g.commerceState, func(x *storage.CommerceTx) error {
		place, err := catalog.ConsumableTown(x.Character)
		if err != nil {
			return err
		}
		return x.ContainerCoupon(slot, cargo, place, catalog.Commerce.ContainerCoupons, time.Now())
	})
	if expectedCommerceError(err) {
		if err = reject(); err != nil {
			return err
		}
		r, err := g.server.DB.Snapshot(ctx, g.lease)
		if err != nil {
			return err
		}
		if r.Phase != storage.TownReady || r.SelectedID == 0 {
			return nil
		}
		c, err := selected(r)
		if err != nil {
			return err
		}
		in, err := g.server.DB.Inventory(ctx, g.lease)
		if err != nil {
			return err
		}
		return g.projectCoupon(c, in, cargo)
	}
	if err != nil {
		return err
	}
	g.commerceState = out.Replay
	if cargo {
		if err = g.send(1, 400, []byte{1}); err != nil {
			return err
		}
	}
	return g.projectCoupon(out.Character, out.Inventory, cargo)
}

func (g *game) projectCoupon(c storage.Character, in storage.Inventory, cargo bool) error {
	bags, err := inventorywire.CommerceBags(in, c, false)
	if err != nil {
		return err
	}
	if cargo {
		p, err := inventorywire.CommerceCargo(in, c, 2)
		if err != nil {
			return err
		}
		bags = append(bags, p)
	}
	return g.projected(bags)
}

func (g *game) unsealEquipment(ctx context.Context, b []byte) error {
	reject := func() error { return g.send(1, 393, mailwire.Rejected(0)) }
	if len(b) != 4 || binary.LittleEndian.Uint16(b[2:]) != math.MaxUint16 || !g.commerceInTown() {
		return reject()
	}
	slot := binary.LittleEndian.Uint16(b)
	catalog := g.catalog().Commerce.MagicSeal
	out, err := g.server.DB.Commerce(ctx, g.lease, 393, g.seq, b, g.commerceState, func(x *storage.CommerceTx) error {
		return x.UnsealEquipment(slot, catalog)
	})
	if expectedCommerceError(err) {
		return reject()
	}
	if err != nil {
		return err
	}
	update, err := inventorywire.CommerceEquipmentDelta(out.Inventory.Gold, out.Character, out.Granted)
	if err != nil {
		return err
	}
	g.commerceState = out.Replay
	if err = g.send(0, update.Opcode, update.Body); err != nil {
		return err
	}
	return g.send(1, 393, []byte{1})
}
