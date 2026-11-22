package host

import (
	"context"
	"time"

	"s9a1/server/internal/inventorymove"
	"s9a1/server/internal/inventorywire"
	"s9a1/server/internal/protocol"
	"s9a1/server/internal/storage"
)

func (g *game) moveContainerItem(ctx context.Context, q storage.MoveRequest, body []byte) error {
	reject := func() error { return g.send(1, 19, inventorymove.Rejected(q)) }
	if !g.commerceInTown() {
		return reject()
	}
	c := g.server.Characters
	out, err := g.server.DB.Commerce(ctx, g.lease, 19, g.seq, body, g.commerceState, func(x *storage.CommerceTx) error {
		if err := x.MoveContainer(q, time.Now()); err != nil {
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
	regular, err := inventorywire.BagAndWear(out.Inventory, out.Character)
	if err != nil {
		return err
	}
	notices := regular[:1:1]
	touches := func(list byte) bool { return q.SourceList == list || q.DestinationList == list }
	if touches(7) {
		special, err := inventorywire.CommerceSpecial(out.Inventory, out.Character)
		if err != nil {
			return err
		}
		notices = append(notices, special[1])
	}
	if touches(38) {
		medals, err := inventorywire.CommerceMedal(out.Inventory, out.Character)
		if err != nil {
			return err
		}
		notices = append(notices, medals)
	}
	seen := map[byte]bool{}
	for _, list := range []byte{q.SourceList, q.DestinationList} {
		if list != 2 && list != 12 || seen[list] {
			continue
		}
		seen[list] = true
		cargo, err := inventorywire.CommerceCargo(out.Inventory, out.Character, list)
		if err != nil {
			return err
		}
		notices = append(notices, cargo)
	}
	notices = append(notices, regular[1])
	actor, _, err := readActor(out.Character, g.channel)
	if err != nil {
		return err
	}
	body, err = protocol.Mode1(actor, true)
	if err != nil {
		return err
	}
	notices = append(notices, inventorywire.Packet{Opcode: 2, Body: body})
	if q.SourceList == 3 && q.SourceSlot == 12 || q.DestinationList == 3 && out.Replay.MoveDestination == 12 {
		body, err = g.actorMode0(actor)
		if err != nil {
			return err
		}
		notices = append(notices, inventorywire.Packet{Opcode: 2, Body: body})
	}
	move := storage.MoveOutcome{SourceList: q.SourceList, SourceSlot: q.SourceSlot, DestinationList: q.DestinationList,
		DestinationSlot: out.Replay.MoveDestination, Count: out.Replay.MoveCount}
	g.commerceState = out.Replay
	if err = g.send(1, 19, inventorymove.Accepted(move)); err != nil {
		return err
	}
	return g.projected(notices)
}
