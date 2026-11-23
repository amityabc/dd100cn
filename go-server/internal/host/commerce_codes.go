package host

import (
	"encoding/binary"
	"slices"

	"s9a1/server/internal/storage"
)

func (g *game) showPurchasedCodes(history map[uint32]storage.CommercePurchase) error {
	var ids []uint32
	for _, id := range g.catalog().Commerce.PurchaseCodes {
		if history[id].Count != 0 && !g.commerceOwnedCodes[id] {
			ids = append(ids, id)
		}
	}
	if len(ids) == 0 {
		return nil
	}
	slices.Sort(ids)
	b := binary.LittleEndian.AppendUint16([]byte{0}, uint16(len(ids)))
	for _, id := range ids {
		b = binary.LittleEndian.AppendUint32(b, id)
	}
	if err := g.send(0, 68, b); err != nil {
		return err
	}
	if g.commerceOwnedCodes == nil {
		g.commerceOwnedCodes = map[uint32]bool{}
	}
	for _, id := range ids {
		g.commerceOwnedCodes[id] = true
	}
	return nil
}
