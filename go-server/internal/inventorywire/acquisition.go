package inventorywire

import (
	"bytes"
	"reflect"

	"s9a1/server/internal/storage"
)

func HasBagAcquisition(before, after storage.Inventory) bool {
	if before.CharacterID == 0 || before.CharacterID != after.CharacterID || before.Revision >= after.Revision || before.BagExpansion != after.BagExpansion {
		return false
	}
	old := map[uint32]int64{}
	current := map[uint32]int64{}
	for _, a := range before.Items {
		old[a.TemplateID] += int64(a.Count)
	}
	for _, a := range after.Items {
		current[a.TemplateID] += int64(a.Count)
	}
	for _, a := range after.Items {
		if a.Location == 0 && current[a.TemplateID] > old[a.TemplateID] {
			return true
		}
	}
	return false
}

// AcquisitionDelta replaces the complete projected bag with its native item
// update. A reused slot is first removed in a separate notification.
func AcquisitionDelta(projected, after storage.Inventory, full []byte) ([]Packet, error) {
	c := storage.Character{ID: after.CharacterID}
	want, err := OrdinaryBag(after, c)
	if err != nil {
		return nil, err
	}
	if !bytes.Equal(full, want) {
		return nil, errShape
	}
	old := map[uint16]storage.Asset{}
	for _, a := range projected.Items {
		if a.Location == 0 {
			old[a.Slot] = a
		}
	}
	var replaced, cleared []uint16
	var changed []storage.Asset
	for _, a := range after.Items {
		if a.Location != 0 {
			continue
		}
		b, exists := old[a.Slot]
		if !exists || !reflect.DeepEqual(a, b) {
			changed = append(changed, a)
		}
		if exists && (a.UID != b.UID || a.TemplateID != b.TemplateID) {
			replaced = append(replaced, a.Slot)
		}
		delete(old, a.Slot)
	}
	for slot := range old {
		cleared = append(cleared, slot)
	}
	var out []Packet
	if len(replaced) != 0 {
		p, err := CommerceBagDelta(c, nil, replaced, nil)
		if err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	p, err := CommerceBagDeltaWithWallet(c, changed, cleared, &after.Gold, &after.Coins)
	return append(out, p), err
}
