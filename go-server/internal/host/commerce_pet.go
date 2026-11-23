package host

import (
	"bytes"
	"context"
	"encoding/binary"
	"time"

	"golang.org/x/text/encoding/simplifiedchinese"

	"s9a1/server/internal/inventorywire"
	"s9a1/server/internal/mailwire"
	"s9a1/server/internal/protocol"
	"s9a1/server/internal/storage"
)

func (g *game) petInventory(in storage.Inventory, c storage.Character) ([]inventorywire.Packet, error) {
	return inventorywire.CommerceSpecialWith(in, c, inventorywire.Options{
		Now: time.Now(), PetDefaultName: g.catalog().Commerce.PetDefaultName,
	})
}

func (g *game) hatchPet(ctx context.Context, b []byte) error {
	if len(b) != 3 {
		return storage.ErrInvalid
	}
	reject := func() error { return g.send(1, 102, mailwire.Rejected(0)) }
	if !g.commerceInTown() || b[0] != 7 {
		return reject()
	}
	slot := binary.LittleEndian.Uint16(b[1:])
	c := g.catalog().Commerce
	out, err := g.server.DB.Commerce(ctx, g.lease, 102, g.seq, b, g.commerceState, func(x *storage.CommerceTx) error {
		return x.HatchPet(slot, c.PetHatch, time.Now())
	})
	if expectedCommerceError(err) {
		return reject()
	}
	if err != nil {
		return err
	}
	special, err := g.petInventory(out.Inventory, out.Character)
	if err != nil {
		return err
	}
	g.trackPetCare(out.Character)
	g.commerceState = out.Replay
	ack := []byte{1}
	if out.Replayed {
		ack = mailwire.Rejected(0)
	}
	if err = g.send(1, 102, ack); err != nil {
		return err
	}
	return g.projected(special[1:3])
}

func (g *game) renamePet(ctx context.Context, b []byte) error {
	le := binary.LittleEndian
	if len(b) < 7 || len(b) > 26 || int(le.Uint32(b[3:])) != len(b)-7 {
		return storage.ErrInvalid
	}
	reject := func() error { return g.send(1, 100, mailwire.Rejected(0)) }
	if b[2] != 7 || !g.commerceInTown() {
		return reject()
	}
	raw, err := simplifiedchinese.GB18030.NewDecoder().Bytes(b[7:])
	if err != nil {
		return storage.ErrInvalid
	}
	name := string(raw)
	encoded, err := protocol.EncodeText(name, 19)
	if err != nil || !bytes.Equal(encoded, b[7:]) {
		return storage.ErrInvalid
	}
	slot := le.Uint16(b)
	c := g.catalog().Commerce
	out, err := g.server.DB.Commerce(ctx, g.lease, 100, g.seq, b, g.commerceState, func(x *storage.CommerceTx) error {
		return x.RenamePet(slot, name, c.PetRenameCard, c.PetNames, time.Now())
	})
	if expectedCommerceError(err) {
		return reject()
	}
	if err != nil {
		return err
	}
	special, err := g.petInventory(out.Inventory, out.Character)
	if err != nil {
		return err
	}
	var actor protocol.Actor
	if err = inventorywire.SpecialActor(out.Character.Gameplay, &actor, time.Now()); err != nil {
		return err
	}
	if actor.Pet == nil {
		return storage.ErrInvalid
	}
	petName, err := protocol.EncodeText(actor.Pet.Name, 19)
	if err != nil {
		return err
	}
	notice := le.AppendUint16(nil, out.Character.ID)
	notice = append(le.AppendUint32(notice, uint32(len(petName))), petName...)
	ack := le.AppendUint16([]byte{1}, slot)
	ack = append(ack, 7)
	if out.Replayed {
		ack = mailwire.Rejected(0)
	}
	g.commerceState = out.Replay
	if err = g.send(1, 100, ack); err != nil {
		return err
	}
	if err = g.send(0, 101, notice); err != nil {
		return err
	}
	return g.projected([]inventorywire.Packet{special[2], special[1]})
}
