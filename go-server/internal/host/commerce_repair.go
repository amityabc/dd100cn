package host

import (
	"bytes"
	"context"
	"encoding/binary"
	"math"
	"time"

	"s9a1/server/internal/inventorywire"
	"s9a1/server/internal/mailwire"
	"s9a1/server/internal/storage"
)

func (g *game) repairEquipment(ctx context.Context, op uint16, body []byte) error {
	reject := func() error { return g.send(1, op, mailwire.Rejected(0)) }
	auto := op == 23 && len(body) == 7
	oculus := g.channel == 63
	run := g.ordinary.Run
	if auto {
		if !bytes.Equal(body, []byte{0, 255, 255, 255, 255, 1, 0}) {
			return storage.ErrInvalid
		}
		if g.channel != 19 || g.opening != nil || g.ordinary.Returning() || run != nil && !run.Ready {
			return reject()
		}
	} else if !oculus && (!g.commerceInTown() || g.ordinary.Selecting()) {
		return reject()
	}
	le := binary.LittleEndian
	q := storage.RepairRequest{Seq: g.seq, Slot: math.MaxUint16, NPC: math.MaxUint16}
	switch op {
	case 23:
		if auto {
			break
		}
		if len(body) != 8 {
			return reject()
		}
		q.List, q.Slot, q.NPC, q.Mode, q.Quick = body[0], le.Uint16(body[1:]), le.Uint16(body[3:]), le.Uint16(body[5:]), body[7]
		if q.List != 0 && q.List != 3 || q.NPC != math.MaxUint16 || q.Mode != 0 || q.Quick != 0 ||
			q.List != 0 && q.Slot == math.MaxUint16 {
			return reject()
		}
	case 217:
		if len(body) != 3 {
			return g.send(1, op, mailwire.Rejected(4))
		}
	default:
		return reject()
	}
	catalog := g.catalog()
	var source *storage.OculusRepairSource
	verify := func(x *storage.CommerceTx) error {
		if oculus {
			var err error
			source, err = g.oculusMerchant(x, 0, 0)
			return err
		}
		if auto {
			if run != nil && run.Character != x.Character.ID {
				return storage.ErrPhase
			}
			return nil
		}
		if !catalog.TownRepair(x.Character) {
			return storage.ErrPhase
		}
		return nil
	}
	out, err := g.server.DB.CommerceScene(ctx, g.lease, op, g.seq, body, g.commerceState, g.dungeonMerchantScene(), verify, func(x *storage.CommerceTx) error {
		if auto {
			return x.RepairWithService(catalog.Commerce.RepairDefinitions, time.Now())
		}
		return x.Repair(q, catalog.Commerce.RepairDefinitions, source)
	})
	if expectedCommerceError(err) {
		return reject()
	}
	if err != nil {
		return err
	}
	projection, err := inventorywire.BagAndWear(out.Inventory, out.Character)
	if err != nil {
		return err
	}
	if !auto {
		bags, err := inventorywire.CommerceBags(out.Inventory, out.Character, true)
		if err != nil {
			return err
		}
		projection = append(bags, projection[1])
	}
	body = le.AppendUint32([]byte{1}, uint32(out.Inventory.Gold))
	body = le.AppendUint16(append(body, q.List), q.Slot)
	body = append(body, 0, 0)
	g.commerceState = out.Replay
	if err = g.send(1, op, body); err != nil {
		return err
	}
	if err = g.projected(projection); err != nil {
		return err
	}
	if auto {
		return g.refreshCharacterServices(ctx, false)
	}
	return nil
}
