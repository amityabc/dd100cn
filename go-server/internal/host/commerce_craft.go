package host

import (
	"context"
	"encoding/binary"
	"math"
	"time"

	"s9a1/server/internal/commercewire"
	"s9a1/server/internal/inventorywire"
	"s9a1/server/internal/mailwire"
	"s9a1/server/internal/protocol"
	"s9a1/server/internal/storage"
)

func (g *game) craftEquipment(ctx context.Context, op uint16, body []byte) error {
	reject := func() error {
		code := uint16(19)
		if op == 205 {
			code = 17
		}
		return g.send(1, op, mailwire.Rejected(code))
	}
	q, err := commercewire.ReadCraft(op, body)
	if err != nil || !g.commerceInTown() || g.ordinary.Selecting() {
		return reject()
	}
	q.Sequence = g.seq
	c := g.server.Characters
	out, err := g.server.DB.Commerce(ctx, g.lease, op, g.seq, body, g.commerceState, func(x *storage.CommerceTx) error {
		if err := x.Craft(q, c.Commerce.Craft, time.Now(), func(method storage.CraftMethod, npc int16) bool {
			return c.TownCraft(x.Character, method, npc)
		}, c.GoldCarryLimits, c.Weights); err != nil {
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
	r := out.Replay.Craft
	if r == nil {
		return storage.ErrInvalid
	}
	reply, err := commercewire.CraftAccepted(q, *r, out.Grants)
	if err != nil {
		return err
	}
	var parts []inventorywire.Packet
	switch op {
	case 80, 205, 1734:
		delta, err := inventorywire.CommerceBagDelta(out.Character, r.Changes, r.Cleared, &out.Inventory.Gold)
		if err != nil {
			return err
		}
		parts = append(parts, delta)
		if q.List == 3 {
			worn, err := craftWorn(out, q.Slot)
			if err != nil {
				return err
			}
			parts = append(parts, inventorywire.Packet{Opcode: 14, Body: worn})
		}
	default:
		parts, err = inventorywire.BagAndWear(out.Inventory, out.Character)
		if err != nil {
			return err
		}
	}
	if q.List == 3 || op == 430 || op == 272 {
		a, _, err := readActor(out.Character, g.channel)
		if err != nil {
			return err
		}
		stats, err := protocol.Mode1(a, true)
		if err != nil {
			return err
		}
		parts = append(parts, inventorywire.Packet{Opcode: 2, Body: stats})
		if q.List == 3 && q.Slot == 12 {
			appearance, err := g.actorMode0(a)
			if err != nil {
				return err
			}
			parts = append(parts, inventorywire.Packet{Opcode: 2, Body: appearance})
		}
	}
	g.commerceState = out.Replay
	if op == 272 {
		if err = g.projected(parts); err != nil {
			return err
		}
		return g.send(1, op, reply)
	}
	if err = g.send(1, op, reply); err != nil {
		return err
	}
	return g.projected(parts)
}

func craftWorn(out storage.CommerceResult, slot uint16) ([]byte, error) {
	for _, a := range out.Inventory.Items {
		if a.Location == 3 && a.CharacterID == out.Character.ID && a.Slot == slot {
			return inventorywire.OrdinaryWornUpdate(out.Character, a)
		}
	}
	if slot < 12 || slot > 25 {
		return nil, storage.ErrInvalid
	}
	body := make([]byte, 122)
	body[0], body[1] = 3, 1
	binary.LittleEndian.PutUint16(body[3:], slot)
	binary.LittleEndian.PutUint32(body[5:], math.MaxUint32)
	return body, nil
}
