package host

import (
	"context"

	"s9a1/server/internal/commercewire"
	"s9a1/server/internal/inventorywire"
	"s9a1/server/internal/storage"
)

func (g *game) sortInventory(ctx context.Context, body []byte) error {
	q, err := commercewire.ReadSort(body)
	if err != nil {
		return err
	}
	reject := func() error { return g.send(1, 20, commercewire.Sorted(q.List, false)) }
	if q.List == 46 {
		if err = reject(); err != nil {
			return err
		}
		return g.commerceAnnouncement("当前服务端尚未接入护石、符文道具整理，物品未变更。")
	}
	if !g.commerceInTown() {
		return reject()
	}
	catalog := g.catalog().Commerce.SortMetadata
	out, err := g.server.DB.Commerce(ctx, g.lease, 20, g.seq, body, g.commerceState, func(x *storage.CommerceTx) error {
		return x.Sort(q, catalog)
	})
	if expectedCommerceError(err) {
		return reject()
	}
	if err != nil {
		return err
	}
	var view []inventorywire.Packet
	switch q.List {
	case 1, 7:
		special, err := inventorywire.CommerceSpecial(out.Inventory, out.Character)
		if err != nil {
			return err
		}
		view = special[:1]
		if q.List == 7 {
			view = special[1:3]
		}
	case 2:
		p, err := inventorywire.CommerceCargo(out.Inventory, out.Character, 2)
		if err != nil {
			return err
		}
		view = []inventorywire.Packet{p}
	default:
		view, err = inventorywire.CommerceBags(out.Inventory, out.Character, false)
		if err != nil {
			return err
		}
	}
	g.commerceState = out.Replay
	if err = g.projected(view); err != nil {
		return err
	}
	return g.send(1, 20, commercewire.Sorted(q.List, true))
}
