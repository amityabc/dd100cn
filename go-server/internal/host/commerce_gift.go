package host

import (
	"context"
	"encoding/binary"
	"errors"
	"time"

	"s9a1/server/internal/commercewire"
	"s9a1/server/internal/inventorywire"
	"s9a1/server/internal/mailwire"
	"s9a1/server/internal/storage"
)

func (g *game) openGift(ctx context.Context, op uint16, body []byte) error {
	reject := func(code uint16) error { return g.send(1, op, mailwire.Rejected(code)) }
	q, parseErr := commercewire.ReadGift(op, body)
	fateShape := op == 160 && len(body) >= 2 && (len(body) == 6 || len(body) == 8 && body[6] == 0 && body[7] == 0)
	if parseErr != nil && !fateShape || !g.commerceInTown() {
		code := uint16(4)
		if op == 160 && len(body) == 91 {
			code = 0
		}
		return reject(code)
	}
	c := g.server.Characters
	replay := g.commerceState
	out, err := g.server.DB.Commerce(ctx, g.lease, op, g.seq, body, replay, func(x *storage.CommerceTx) error {
		if op == 160 && len(body) >= 2 {
			if replay.Opcode == 160 && replay.Sequence == g.seq && replay.Gift != nil && replay.Gift.FateBox &&
				replay.CharacterID == x.Character.ID && replay.Revision == x.Character.Revision && replay.AssetRevision == x.Inventory.AssetRevision {
				return storage.ErrFateBoxRejected
			}
			q.Slot = binary.LittleEndian.Uint16(body)
			var amount uint32
			if fateShape {
				amount = binary.LittleEndian.Uint32(body[2:])
			}
			matched, err := x.OpenFateBox(q.Slot, amount, fateShape, c.Commerce.FateBoxes, time.Now())
			if matched {
				if err == nil {
					err = x.CheckCarry(c.GoldCarryLimits, c.Weights)
				}
				if expectedCommerceError(err) {
					err = storage.ErrFateBoxRejected
				}
				return err
			}
		}
		if parseErr != nil {
			return storage.ErrInvalid
		}
		if err := x.OpenGift(q, c.Commerce, time.Now(), c.GoldCarryLimits, c.Weights); err != nil {
			return err
		}
		return x.CheckCarry(c.GoldCarryLimits, c.Weights)
	})
	if errors.Is(err, storage.ErrFateBoxRejected) {
		return reject(0)
	}
	if expectedCommerceError(err) {
		if op == 160 && len(body) == 91 {
			return reject(0)
		}
		return reject(4)
	}
	if err != nil {
		return err
	}
	opened := out.Replay.Gift
	if opened == nil {
		return storage.ErrInvalid
	}
	bags, err := inventorywire.CommerceBags(out.Inventory, out.Character, !opened.FateBox)
	if err != nil {
		return err
	}
	if opened.Avatar || opened.Pet {
		special, err := inventorywire.CommerceSpecial(out.Inventory, out.Character)
		if err != nil {
			return err
		}
		if opened.Avatar {
			bags = append(bags, special[0])
		}
		if opened.Pet {
			bags = append(bags, special[1:3]...)
			g.trackPetCare(out.Character)
		}
	}
	if opened.Common {
		common, err := inventorywire.CommerceCommon(out.Inventory, out.Character)
		if err != nil {
			return err
		}
		bags = append(bags, common)
	}
	if opened.PersonalCargo {
		cargo, err := inventorywire.CommerceCargo(out.Inventory, out.Character, 2)
		if err != nil {
			return err
		}
		bags = append(bags, cargo)
	}
	g.commerceState = out.Replay
	if err = g.send(1, op, commercewire.GiftOpened(op, q.Slot, *opened)); err != nil {
		return err
	}
	if err = g.projected(bags); err != nil {
		return err
	}
	if opened.Premium {
		r, err := g.server.DB.Snapshot(ctx, g.lease)
		if err != nil {
			return err
		}
		g.contracts = [4]int64{r.ConquerorExpiresAt, r.SkillMasterExpiresAt, r.CrystalExpiresAt, r.GrowthExpiresAt}
		return g.showPremium()
	}
	return nil
}
