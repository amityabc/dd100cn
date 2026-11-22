package inventorywire

import (
	"math"
	"slices"

	"s9a1/server/internal/storage"
)

// DungeonBagDelta writes the native list0 update for committed ground rewards
// and card claims. A concurrent inventory change cannot reintroduce an item
// that has left this bag because callers supply only the current committed rows.
func DungeonBagDelta(in storage.Inventory, character storage.Character, assets []storage.Asset, includeGold bool) ([]byte, error) {
	var rows []row
	if in.CharacterID != character.ID || in.Gold < 0 || in.Gold > math.MaxUint32 {
		return nil, errShape
	}
	for _, a := range assets {
		if a.Location != 0 || a.CharacterID != character.ID {
			continue
		}
		r, _, err := ordinaryRow(a, character)
		if err != nil {
			return nil, err
		}
		rows = append(rows, r)
	}
	if len(rows) > 370 {
		return nil, errShape
	}
	slices.SortFunc(rows, func(a, b row) int { return int(a.slot) - int(b.slot) })
	for i := 1; i < len(rows); i++ {
		if rows[i-1].slot == rows[i].slot {
			return nil, errShape
		}
	}
	count := len(rows)
	if includeGold {
		count++
	}
	w := writer{0}
	w.u16(uint16(count))
	if includeGold {
		w.raw(walletRow(0, uint32(min(in.Gold, math.MaxInt32))))
	}
	return appendRows(w, rows), nil
}

func DungeonMedalDelta(c storage.Character, grants []storage.Asset) ([]byte, error) {
	var rows []row
	for _, item := range grants {
		if item.Location != 38 {
			continue
		}
		if item.CharacterID != c.ID || item.Slot > 55 {
			return nil, errShape
		}
		r, _, err := ordinaryRow(item, c)
		if err != nil {
			return nil, err
		}
		rows = append(rows, r)
	}
	slices.SortFunc(rows, func(a, b row) int { return int(a.slot) - int(b.slot) })
	return counted(38, rows), nil
}
