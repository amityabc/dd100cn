package questwire

import (
	"encoding/json"
	"s9a1/server/internal/storage"
)

// SatisfiedDelivery finds the first supported terminal child that the native
// notice path settles when the character reaches a ready town.
func (catalog *Catalog) SatisfiedDelivery(c storage.Character, in storage.Inventory, rs map[string]json.RawMessage) (uint16, uint16, error) {
	p, err := playerOf(c)
	if err != nil {
		return 0, 0, err
	}
	v := projectionView(catalog, p, in, rs)
	for i := range catalog.quests {
		q := &catalog.quests[i]
		if q.Parent == 0 || !(q.Kind == seeking || q.Kind == clearMap || q.Kind == seekMeet) {
			continue
		}
		if _, ok := p.hist.Active[q.Id]; !ok {
			continue
		}
		parent := catalog.ordinary[q.Parent]
		if parent == nil || parent.Kind != questClear || parent.Subtype != 2 || q.Kind == seekMeet && !parent.DungeonNpcHandIn {
			continue
		}
		if left, err := v.remaining(q, map[uint16]bool{}); err == nil && left == 0 {
			return q.Id, q.Parent, nil
		}
	}
	return 0, 0, nil
}
