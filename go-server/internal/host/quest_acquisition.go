package host

import (
	"reflect"
	"s9a1/server/internal/inventorywire"
	"s9a1/server/internal/storage"
)

func (g *game) questRewardBag(before storage.Inventory, out storage.CommerceResult) ([]inventorywire.Packet, error) {
	current := out.Inventory
	if out.Replayed || before.CharacterID != current.CharacterID || before.Revision >= current.Revision || before.BagExpansion != current.BagExpansion {
		return inventorywire.CommerceBags(current, out.Character, false)
	}
	totals := make(map[uint32]int64, len(before.Items))
	old := make(map[uint16]storage.Asset, len(before.Items))
	for _, a := range before.Items {
		if a.Location == 45 {
			continue
		}
		totals[a.TemplateID] -= int64(a.Count)
		if a.Location == 0 {
			old[a.Slot] = a
		}
	}
	for _, a := range current.Items {
		if a.Location == 45 {
			continue
		}
		totals[a.TemplateID] += int64(a.Count)
	}
	acquired := false
	for _, a := range current.Items {
		if a.Location == 0 && totals[a.TemplateID] > 0 {
			acquired = true
			break
		}
	}
	if !acquired {
		return inventorywire.CommerceBags(current, out.Character, false)
	}
	var changed []storage.Asset
	var replaced, cleared []uint16
	for _, a := range current.Items {
		if a.Location != 0 {
			continue
		}
		prior, had := old[a.Slot]
		delete(old, a.Slot)
		if had && reflect.DeepEqual(prior, a) {
			continue
		}
		if had && (a.UID != prior.UID || a.TemplateID != prior.TemplateID) {
			replaced = append(replaced, a.Slot)
		}
		changed = append(changed, a)
	}
	for slot := range old {
		cleared = append(cleared, slot)
	}
	var outPackets []inventorywire.Packet
	if len(replaced) > 0 {
		p, err := inventorywire.CommerceBagDelta(out.Character, nil, replaced, nil)
		if err != nil {
			return nil, err
		}
		outPackets = append(outPackets, p)
	}
	p, err := inventorywire.CommerceBagDeltaWithWallet(out.Character, changed, cleared, &current.Gold, &current.Coins)
	if err != nil {
		return nil, err
	}
	return append(outPackets, p), nil
}
