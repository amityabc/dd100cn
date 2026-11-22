package host

import (
	"context"
	"s9a1/server/internal/inventorywire"
	"s9a1/server/internal/protocol"
	"s9a1/server/internal/questwire"
	"s9a1/server/internal/storage"
	"slices"
	"time"
)

func (g *game) questEmblem(ctx context.Context, op uint16, body []byte) error {
	if op == 256 {
		return g.emblemCompound(ctx, body)
	}
	req, n, err := questwire.ReadEmblem(op, body)
	if err != nil {
		return err
	}
	rejected := []byte{0, 19, 0}
	if !g.commerceInTown() {
		return g.send(1, op, rejected)
	}
	now := time.Now()
	var remaining []storage.EmblemRemaining
	out, err := g.server.DB.Commerce(ctx, g.lease, op, g.seq, body[:n], g.questEmblems[op], func(x *storage.CommerceTx) error {
		var err error
		remaining, err = x.ApplyEmblem(req, g.server.Characters.Quests.Emblems(), now)
		return err
	})
	if expectedCommerceError(err) {
		return g.send(1, op, rejected)
	}
	if err != nil {
		return err
	}
	ack := rejected
	if !out.Replayed {
		ack = questwire.EmblemAccepted(remaining)
		if g.questEmblems == nil {
			g.questEmblems = map[uint16]storage.CommerceReplay{}
		}
		g.questEmblems[op] = out.Replay
	}
	bags, err := inventorywire.CommerceBags(out.Inventory, out.Character, false)
	if err != nil {
		return err
	}
	if op == 201 {
		packets, err := inventorywire.CommerceSpecial(out.Inventory, out.Character)
		if err != nil {
			return err
		}
		if req.List == 3 {
			if err = g.projected(packets[5:6]); err != nil {
				return err
			}
		}
		if err = g.projected(packets[:1]); err != nil {
			return err
		}
	} else if err = g.projected(bags); err != nil {
		return err
	}
	if err = g.send(1, op, ack); err != nil {
		return err
	}
	if err = g.projected(bags); err != nil {
		return err
	}
	if op == 201 && req.List == 3 {
		a, _, err := readActor(out.Character, g.channel)
		if err != nil {
			return err
		}
		b, err := protocol.Mode1(a, true)
		if err != nil {
			return err
		}
		return g.send(0, 2, b)
	}
	return nil
}

func (g *game) emblemCompound(ctx context.Context, body []byte) error {
	const op = 256
	rows, err := questwire.ReadEmblemCompound(body)
	if err != nil {
		return err
	}
	rejected := []byte{0, 0, 0}
	if !g.commerceInTown() {
		return g.send(1, op, rejected)
	}
	cat := g.server.Characters.Quests.Emblems().Compound
	now := time.Now()
	var rewards []storage.GiftReward
	var before storage.Inventory
	out, err := g.server.DB.Commerce(ctx, g.lease, op, g.seq, body, g.questEmblems[op], func(x *storage.CommerceTx) error {
		before = x.Inventory
		before.Items = slices.Clone(before.Items)
		var err error
		rewards, err = x.CompoundEmblems(rows, cat, now)
		return err
	})
	if expectedCommerceError(err) {
		return g.send(1, op, rejected)
	}
	if err != nil {
		return err
	}
	ack := rejected
	if !out.Replayed {
		ack = questwire.EmblemCompoundAccepted(rewards)
		if g.questEmblems == nil {
			g.questEmblems = map[uint16]storage.CommerceReplay{}
		}
		g.questEmblems[op] = out.Replay
	}
	bags, err := g.questRewardBag(before, out)
	if err != nil {
		return err
	}
	if err = g.send(1, op, ack); err != nil {
		return err
	}
	return g.projected(bags)
}
