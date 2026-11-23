package host

import (
	"context"
	"encoding/binary"
	"errors"

	"s9a1/server/internal/inventorywire"
	"s9a1/server/internal/mailwire"
	"s9a1/server/internal/storage"
)

func expectedCommerceError(err error) bool {
	return errors.Is(err, storage.ErrPhase) || errors.Is(err, storage.ErrInvalid) || errors.Is(err, storage.ErrFunds) || errors.Is(err, storage.ErrCapacity)
}

func (g *game) goldExpansion(ctx context.Context, body []byte) error {
	if len(body) != 14 {
		return storage.ErrInvalid
	}
	stage := body[13]
	out, err := g.server.DB.Commerce(ctx, g.lease, 972, g.seq, body, g.commerceState, func(x *storage.CommerceTx) error {
		if !g.commerceInTown() {
			return storage.ErrPhase
		}
		return x.SpendGoldExpansion(stage)
	})
	if expectedCommerceError(err) {
		r, err := g.server.DB.Snapshot(ctx, g.lease)
		if err != nil {
			return err
		}
		stage = 0
		if c, err := selected(r); err == nil {
			p, err := readGameplay(c.Gameplay)
			if err != nil {
				return err
			}
			stage = p.GoldExpansion
		}
		return g.send(1, 972, []byte{0, 4, 0, stage, 4, 0, 0, 0})
	}
	if err != nil {
		return err
	}
	bag, err := mailBag(out.Inventory, out.Character)
	if err != nil {
		return err
	}
	g.commerceState = out.Replay
	if err = g.send(1, 972, []byte{1, stage, 0, 0, 0, 0}); err != nil {
		return err
	}
	return g.send(0, 13, bag)
}

func (g *game) accountCargo(ctx context.Context, op uint16, body []byte) error {
	var amount uint32
	rejection := uint16(4)
	if op == 307 || op == 308 {
		rejection = 10
		if len(body) != 4 {
			return g.send(1, op, mailwire.Rejected(rejection))
		}
		amount = binary.LittleEndian.Uint32(body)
	} else if len(body) != 0 {
		return g.send(1, op, mailwire.Rejected(rejection))
	}
	catalog := g.catalog()
	out, err := g.server.DB.Commerce(ctx, g.lease, op, g.seq, body, g.commerceState, func(x *storage.CommerceTx) error {
		if !g.commerceInTown() {
			return storage.ErrPhase
		}
		return x.AccountCargo(op, amount, catalog.Commerce.AccountCargo, catalog.GoldCarryLimits)
	})
	var code storage.CommerceRejection
	if errors.As(err, &code) {
		return g.send(1, op, mailwire.Rejected(uint16(code)))
	}
	if expectedCommerceError(err) {
		return g.send(1, op, mailwire.Rejected(rejection))
	}
	if err != nil {
		return err
	}
	bags, err := inventorywire.CommerceBags(out.Inventory, out.Character, true)
	if err != nil {
		return err
	}
	cargo, err := inventorywire.CommerceCargo(out.Inventory, out.Character, 12)
	if err != nil {
		return err
	}
	ack := []byte{1}
	if op == 307 || op == 308 {
		ack = binary.LittleEndian.AppendUint32(ack, amount)
	}
	g.commerceState = out.Replay
	if err = g.send(1, op, ack); err != nil {
		return err
	}
	return g.projected(append(bags, cargo))
}
