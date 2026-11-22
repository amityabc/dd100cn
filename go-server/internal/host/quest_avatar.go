package host

import (
	"context"
	"s9a1/server/internal/inventorywire"
	"s9a1/server/internal/questwire"
	"s9a1/server/internal/storage"
	"time"
)

func (g *game) questAvatarEdit(ctx context.Context, op uint16, body []byte) error {
	if op == 202 {
		return g.avatarDisassembly(ctx, body)
	}
	if op == 99 || op == 1113 || op == 1787 {
		return g.avatarCompound(ctx, op, body)
	}
	req, err := questwire.ReadAvatarEdit(op, body)
	if err != nil {
		return err
	}
	rejected := []byte{0}
	if op == 206 {
		rejected = []byte{0, 0, 0}
	}
	if !g.commerceInTown() {
		return g.send(1, op, rejected)
	}
	cat := g.server.Characters.Quests
	now := time.Now()
	var result []byte
	out, err := g.server.DB.Commerce(ctx, g.lease, op, g.seq, body, g.questEmblems[op], func(x *storage.CommerceTx) error {
		var err error
		result, err = x.ApplyAvatarEdit(req, cat.AvatarEdits(), now, cat.AvatarSkillAvailable)
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
		ack = questwire.AvatarEditAccepted(req, result)
		if g.questEmblems == nil {
			g.questEmblems = map[uint16]storage.CommerceReplay{}
		}
		g.questEmblems[op] = out.Replay
	}
	bags, err := inventorywire.CommerceBags(out.Inventory, out.Character, false)
	if err != nil {
		return err
	}
	special, err := inventorywire.CommerceSpecial(out.Inventory, out.Character)
	if err != nil {
		return err
	}
	if op != 490 {
		if err = g.projected(special[:1]); err != nil {
			return err
		}
	}
	if err = g.send(1, op, ack); err != nil {
		return err
	}
	if err = g.projected(bags); err != nil {
		return err
	}
	if op == 490 {
		return g.projected(special[:1])
	}
	return nil
}

func (g *game) avatarDisassembly(ctx context.Context, body []byte) error {
	const op = 202
	slot, id, err := questwire.ReadAvatarDisassembly(body)
	if err != nil {
		return err
	}
	rejected := []byte{0, 0, 0}
	if !g.commerceInTown() {
		return g.send(1, op, rejected)
	}
	cat := g.server.Characters.Quests.AvatarEdits().Disassembly
	now := time.Now()
	var before storage.Inventory
	out, err := g.server.DB.Commerce(ctx, g.lease, op, g.seq, body, g.questEmblems[op], func(x *storage.CommerceTx) error {
		before = x.Inventory
		return x.DisassembleAvatar(slot, id, cat, now)
	})
	if expectedCommerceError(err) {
		return g.send(1, op, rejected)
	}
	if err != nil {
		return err
	}
	ack := rejected
	if !out.Replayed {
		ack, err = questwire.AvatarDisassemblyAccepted(slot, out.Grants, out.Inventory)
		if err != nil {
			return err
		}
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
	if err = g.projected(bags); err != nil {
		return err
	}
	return g.projected(special[:1])
}
