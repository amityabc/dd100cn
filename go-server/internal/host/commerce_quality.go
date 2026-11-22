package host

import (
	"context"
	"time"

	"s9a1/server/internal/commercewire"
	"s9a1/server/internal/inventorywire"
	"s9a1/server/internal/mailwire"
	"s9a1/server/internal/storage"
)

func (g *game) changeQuality(ctx context.Context, op uint16, body []byte) error {
	q, err := commercewire.ReadQuality(op, body)
	if err != nil {
		return err
	}
	reject := func() error { return g.send(1, op, mailwire.Rejected(19)) }
	if !g.commerceInTown() || g.ordinary.Selecting() {
		return reject()
	}
	c := g.server.Characters.Commerce.Quality
	out, err := g.server.DB.Commerce(ctx, g.lease, op, g.seq, body, g.commerceState, func(x *storage.CommerceTx) error {
		return x.ChangeQuality(q, c, time.Now())
	})
	if expectedCommerceError(err) {
		return reject()
	}
	if err != nil {
		return err
	}
	target, err := inventorywire.CommerceBagDelta(out.Character, []storage.Asset{out.Granted}, nil, nil)
	if err != nil {
		return err
	}
	var remaining []storage.Asset
	var cleared []uint16
	for _, a := range out.Inventory.Items {
		if a.Location == 0 && a.Slot == q.SourceSlot {
			remaining = append(remaining, a)
			break
		}
	}
	if len(remaining) == 0 {
		cleared = append(cleared, q.SourceSlot)
	}
	source, err := inventorywire.CommerceBagDelta(out.Character, remaining, cleared, nil)
	if err != nil {
		return err
	}
	g.commerceState = out.Replay
	ack := commercewire.QualityAccepted(op, q.SourceSlot, out.Replay.MoveCount)
	if err = g.projected([]inventorywire.Packet{target}); err != nil {
		return err
	}
	if op == 81 {
		if err = g.send(1, op, ack); err != nil {
			return err
		}
		return g.projected([]inventorywire.Packet{source})
	}
	if err = g.projected([]inventorywire.Packet{source}); err != nil {
		return err
	}
	return g.send(1, op, ack)
}
