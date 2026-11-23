package host

import (
	"context"
	"encoding/binary"
	"s9a1/server/internal/inventorywire"
	"s9a1/server/internal/questwire"
	"s9a1/server/internal/storage"
	"slices"
	"time"
)

func (g *game) avatarCompound(ctx context.Context, op uint16, body []byte) error {
	cat := g.catalog().Quests.AvatarEdits().Compound
	now := time.Now()
	var key []byte
	var plan func(*storage.CommerceTx) error
	var result []byte
	if op == 1787 {
		r, replay, err := questwire.ReadUniqueAvatar(body)
		if err != nil {
			return err
		}
		key = replay
		plan = func(x *storage.CommerceTx) error {
			p, err := x.UniqueAvatar(r, cat, now)
			if err != nil {
				return err
			}
			result = questwire.UniqueAvatarAccepted(p)
			return nil
		}
	} else {
		r, replay, err := questwire.ReadAvatarCompound(op, body)
		if err != nil {
			return err
		}
		key = replay
		plan = func(x *storage.CommerceTx) error {
			p, err := x.CompoundAvatars(r, cat, now)
			if err != nil {
				return err
			}
			result = questwire.AvatarCompoundAccepted(r, p)
			return nil
		}
	}
	rejected := []byte{0, 0, 0}
	if !g.commerceInTown() {
		return g.send(1, op, rejected)
	}
	var before storage.Inventory
	out, err := g.server.DB.Commerce(ctx, g.lease, op, g.seq, key, g.questEmblems[op], func(x *storage.CommerceTx) error {
		before = x.Inventory
		before.Items = slices.Clone(before.Items)
		return plan(x)
	})
	if expectedCommerceError(err) {
		return g.send(1, op, rejected)
	}
	if err != nil {
		return err
	}
	ack := rejected
	if !out.Replayed {
		ack = result
		if g.questEmblems == nil {
			g.questEmblems = map[uint16]storage.CommerceReplay{}
		}
		g.questEmblems[op] = out.Replay
	}
	bags, err := g.questRewardBag(before, out)
	if err != nil {
		return err
	}
	special, err := inventorywire.CommerceSpecial(out.Inventory, out.Character)
	if err != nil {
		return err
	}
	if err = g.send(1, op, ack); err != nil {
		return err
	}
	if op == 1787 {
		if err = g.sendAvatarGauge(out.Inventory.UniqueAvatarGauge); err != nil {
			return err
		}
	}
	if err = g.projected(bags); err != nil {
		return err
	}
	return g.projected(special[:1])
}

func (g *game) sendAvatarGauge(gauge uint16) error {
	return g.send(0, 1929, binary.LittleEndian.AppendUint32(nil, uint32(gauge)))
}
