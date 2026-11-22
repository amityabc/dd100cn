package host

import (
	"s9a1/server/internal/inventorywire"
	"s9a1/server/internal/storage"
)

type inventoryFrame struct {
	reads     *storage.InventoryProjection
	projected *storage.Inventory
}

func (g *game) acquiredInventory(class byte, op uint16, body []byte) (bool, error) {
	f := g.inventoryFrame
	if f == nil || class != 0 || op != 13 || len(body) == 0 || body[0] != 0 {
		return false, nil
	}
	before, after := f.reads.Before, f.reads.Current
	if before == nil || after == nil || !inventorywire.HasBagAcquisition(*before, *after) {
		return false, nil
	}
	projected := f.projected
	if projected == nil {
		projected = before
	}
	frames, err := inventorywire.AcquisitionDelta(*projected, *after, body)
	if err != nil {
		return true, err
	}
	if err = g.projected(frames); err != nil {
		return true, err
	}
	f.projected = after
	return true, nil
}
