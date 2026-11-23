package host

import (
	"context"
	"encoding/binary"
	"errors"
	"math"
	"s9a1/server/internal/inventorywire"
	"s9a1/server/internal/protocol"
	"s9a1/server/internal/storage"
	"time"
)

func (g *game) avatarSkinSlot(ctx context.Context, b []byte) error {
	if len(b) != 4 {
		return storage.ErrInvalid
	}
	source, target := binary.LittleEndian.Uint16(b), binary.LittleEndian.Uint16(b[2:])
	if source != math.MaxUint16 && !(source >= 3 && source <= 8 || source >= 65 && source <= 344) || target != 11 && target != 33 {
		return storage.ErrInvalid
	}
	return g.unlockSkinSlot(ctx, 857, source, target, 0)
}

func (g *game) petSkinSlot(ctx context.Context, b []byte) error {
	if len(b) != 19 {
		return storage.ErrInvalid
	}
	source, list := binary.LittleEndian.Uint16(b), b[2]
	if list != 0 || binary.LittleEndian.Uint32(b[3:]) != 0 || binary.LittleEndian.Uint32(b[7:]) != 230 || binary.LittleEndian.Uint64(b[11:]) != 0 {
		return g.sendSkinReply(507, source, 33, list, false, false)
	}
	return g.unlockSkinSlot(ctx, 507, source, 33, list)
}

func (g *game) sendSkinReply(op, source, target uint16, list byte, ok, missing bool) error {
	b := []byte{1}
	if !ok {
		b = []byte{0, 0, 0}
		if missing && op == 857 {
			b[1] = 1
		}
	}
	if op == 857 {
		b = binary.LittleEndian.AppendUint16(b, target)
	} else {
		b = binary.LittleEndian.AppendUint16(b, source)
		b = append(b, list)
		b = binary.LittleEndian.AppendUint32(b, 230)
	}
	return g.send(1, op, b)
}

func (g *game) unlockSkinSlot(ctx context.Context, op, source, target uint16, list byte) error {
	reject := func(missing bool) error { return g.sendSkinReply(op, source, target, list, false, missing) }
	if op == 857 && target == 33 && source == math.MaxUint16 {
		return reject(true)
	}
	if !g.commerceInTown() {
		return reject(false)
	}
	cat := g.catalog()
	now := time.Now()
	out, err := g.server.DB.Commerce(ctx, g.lease, op, g.seq, nil, storage.CommerceReplay{}, func(x *storage.CommerceTx) error {
		return x.UnlockSkinSlot(source, target, cat.Quests.AvatarEdits(), now)
	})
	if errors.Is(err, storage.ErrSkinTicketMissing) {
		return reject(true)
	}
	if expectedCommerceError(err) {
		return reject(false)
	}
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
	m0, err := g.actorMode0(a)
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
	if op == 507 {
		if err = g.sendSkinReply(op, source, target, list, true, false); err != nil {
			return err
		}
	}
	if err = g.send(0, 2, m1); err != nil {
		return err
	}
	if op == 857 {
		if err = g.sendSkinReply(op, source, target, list, true, false); err != nil {
			return err
		}
	}
	if err = g.projected(bags); err != nil {
		return err
	}
	if err = g.projected(special); err != nil {
		return err
	}
	return g.send(0, 2, m0)
}
