package host

import (
	"context"
	"encoding/binary"
	"fmt"
	"time"

	"s9a1/server/internal/commercewire"
	"s9a1/server/internal/inventorywire"
	"s9a1/server/internal/mailwire"
	"s9a1/server/internal/storage"
)

func (g *game) buyFateExchange(ctx context.Context, buy commercewire.Buy, body []byte) error {
	reject := func() error { return g.send(1, 21, mailwire.Rejected(0)) }
	if !g.commerceInTown() {
		return reject()
	}
	c := g.server.Characters
	d := c.Commerce.FateExchange
	now := time.Now()
	q := storage.FateExchangeRequest{NPC: buy.NPC, Shop: buy.Shop, Template: buy.TemplateID, Count: buy.Count, Sequence: g.seq}
	// Quota remains durable across hosts, reconnects and returning to the
	// selector. The immediate replay additionally binds the requested period.
	scene := fmt.Sprintf("fate-exchange:%d:%d", buy.NPC, now.UTC().Add(2*time.Hour).YearDay())
	replay := g.commerceState
	if replay.Scene != scene {
		replay = storage.CommerceReplay{}
	}
	out, err := g.server.DB.Commerce(ctx, g.lease, 21, g.seq, body, replay, func(x *storage.CommerceTx) error {
		if err := x.BuyFateExchange(q, d, now); err != nil {
			return err
		}
		return x.CheckCarry(c.GoldCarryLimits, c.Weights)
	})
	if expectedCommerceError(err) {
		return reject()
	}
	if err != nil {
		return err
	}
	counts, err := g.server.DB.FateExchangeCounts(ctx, g.lease, d, buy.NPC, now)
	if err != nil {
		return err
	}
	core, err := inventorywire.MailItem119(out.Granted)
	if err != nil {
		return err
	}
	var medal uint32
	for _, a := range out.Inventory.Items {
		if a.Location == 0 && a.Slot == 2 && a.TemplateID == 2 {
			medal += a.Count
		}
	}
	bags, err := inventorywire.CommerceBags(out.Inventory, out.Character, false)
	if err != nil {
		return err
	}
	common, err := inventorywire.CommerceCommon(out.Inventory, out.Character)
	if err != nil {
		return err
	}
	g.commerceState = out.Replay
	g.commerceState.Scene = scene
	if err = g.send(1, 21, commercewire.Bought(uint32(out.Inventory.Gold), medal, uint32(out.Inventory.Cera), core)); err != nil {
		return err
	}
	if err = g.send(1, 707, fateExchangeCountsPacket(counts)); err != nil {
		return err
	}
	return g.projected(append([]inventorywire.Packet{common}, bags...))
}

func (g *game) fateExchangeCounts(ctx context.Context, npc uint32) error {
	reject := func() error { return g.send(1, 707, mailwire.Rejected(0)) }
	if !g.commerceInTown() {
		return reject()
	}
	counts, err := g.server.DB.FateExchangeCounts(ctx, g.lease, g.server.Characters.Commerce.FateExchange, npc, time.Now())
	if expectedCommerceError(err) {
		return reject()
	}
	if err != nil {
		return err
	}
	return g.send(1, 707, fateExchangeCountsPacket(counts))
}

func fateExchangeCountsPacket(rows map[uint32]uint32) []byte {
	le := binary.LittleEndian
	b := make([]byte, 9, 17+8*len(rows))
	b[0] = 1
	b = le.AppendUint32(b, uint32(len(rows)))
	for _, id := range storage.FateExchangeProducts() {
		b = le.AppendUint32(b, id)
		b = le.AppendUint32(b, rows[id])
	}
	return le.AppendUint32(b, 0)
}
