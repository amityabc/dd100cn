package host

import (
	"context"
	"time"

	"s9a1/server/internal/inventorymove"
	"s9a1/server/internal/inventorywire"
	"s9a1/server/internal/protocol"
	"s9a1/server/internal/storage"
)

func (g *game) moveSpecialItem(ctx context.Context, q storage.MoveRequest, body []byte) error {
	reject := func() error { return g.send(1, 19, inventorymove.Rejected(q)) }
	if !g.commerceInTown() {
		return reject()
	}
	c := g.server.Characters
	out, err := g.server.DB.Commerce(ctx, g.lease, 19, g.seq, body, g.commerceState, func(x *storage.CommerceTx) error {
		return x.MoveSpecial(q, c.Commerce.AvatarClones, time.Now())
	})
	if expectedCommerceError(err) {
		return reject()
	}
	if err != nil {
		return err
	}
	bags, err := inventorywire.CommerceBags(out.Inventory, out.Character, false)
	if err != nil {
		return err
	}
	special, err := g.petInventory(out.Inventory, out.Character)
	if err != nil {
		return err
	}
	a, _, err := readActor(out.Character, g.channel)
	if err != nil {
		return err
	}
	m1, err := protocol.Mode1(a, true)
	if err != nil {
		return err
	}
	bags = append(bags, inventorywire.Packet{Opcode: 2, Body: m1})
	pet := q.SourceList == 7 || q.DestinationList == 7 || q.SourceList == 3 && (q.SourceSlot == 26 || q.SourceSlot == 33)
	avatar := q.SourceList == 1 || q.DestinationList == 1
	if pet || avatar {
		if pet {
			bags = append(bags, special[1], special[3], special[4], special[2])
		}
		if avatar {
			bags = append(bags, special[0], special[5])
		}
		m0, err := g.actorMode0(a)
		if err != nil {
			return err
		}
		bags = append(bags, inventorywire.Packet{Opcode: 2, Body: m0})
	}
	move := storage.MoveOutcome{SourceList: q.SourceList, SourceSlot: q.SourceSlot, DestinationList: q.DestinationList,
		DestinationSlot: out.Replay.MoveDestination, Count: q.Count}
	g.commerceState = out.Replay
	if err = g.send(1, 19, inventorymove.Accepted(move)); err != nil {
		return err
	}
	return g.projected(bags)
}
