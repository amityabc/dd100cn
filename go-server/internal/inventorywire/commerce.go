package inventorywire

import (
	"encoding/json"
	"math"
	"s9a1/server/internal/storage"
	"slices"
	"time"
)

func CommerceBags(in storage.Inventory, c storage.Character, extended bool) ([]Packet, error) {
	if !extended {
		packets, err := BagAndWear(in, c)
		if err != nil {
			return nil, err
		}
		return packets[:1], nil
	}
	rows, err := ordinary(in, c)
	if err != nil {
		return nil, err
	}
	out := bagAndWear(in, rows)[:1:1]
	if len(rows[7]) != 0 {
		special, err := CommerceSpecial(in, c)
		if err != nil {
			return nil, err
		}
		out = append(out, special[1])
	}
	if len(rows[38]) != 0 {
		out = append(out, Packet{13, counted(38, rows[38])})
	}
	return out, nil
}

func CommerceSpecial(in storage.Inventory, c storage.Character) ([]Packet, error) {
	return CommerceSpecialWith(in, c, Options{Now: time.Now()})
}

func CommerceSpecialWith(in storage.Inventory, c storage.Character, opts Options) ([]Packet, error) {
	rows, err := ordinary(in, c)
	if err != nil {
		return nil, err
	}
	var profile struct {
		SpecialAssets   specialState
		PetSkinUnlocked bool
	}
	if err = json.Unmarshal(c.Gameplay, &profile); err != nil {
		return nil, err
	}
	return specialPackets(profile.SpecialAssets, profile.PetSkinUnlocked, rows[7], opts)
}

func CommerceCargo(in storage.Inventory, c storage.Character, list byte) (Packet, error) {
	rows, err := ordinary(in, c)
	if err != nil {
		return Packet{}, err
	}
	w := writer{list}
	switch list {
	case 2:
		w.u16(in.PersonalCapacity)
	case 12:
		w.u16(in.CargoCapacity)
		w.u32(uint32(in.CargoGold))
	default:
		return Packet{}, errShape
	}
	w.u16(uint16(len(rows[list])))
	b := appendRows(w, rows[list])
	if list == 2 {
		b = append(b, 0)
	}
	return Packet{13, b}, nil
}

func CommerceCommon(in storage.Inventory, c storage.Character) (Packet, error) {
	rows, err := ordinary(in, c)
	if err != nil {
		return Packet{}, err
	}
	return Packet{13, counted(45, rows[45])}, nil
}

func CommerceMedal(in storage.Inventory, c storage.Character) (Packet, error) {
	rows, err := ordinary(in, c)
	if err != nil {
		return Packet{}, err
	}
	return Packet{13, counted(38, rows[38])}, nil
}

func CommerceEquipmentDelta(gold int64, c storage.Character, a storage.Asset) (Packet, error) {
	r, _, err := ordinaryRow(a, c)
	if err != nil {
		return Packet{}, err
	}
	w := writer{0, 2, 0}
	w.raw(walletRow(0, uint32(min(gold, math.MaxInt32))))
	w.raw(r.body)
	return Packet{14, w}, nil
}

func CommerceBagDelta(c storage.Character, assets []storage.Asset, cleared []uint16, gold *int64) (Packet, error) {
	return CommerceBagDeltaWithWallet(c, assets, cleared, gold, nil)
}

func CommerceBagDeltaWithWallet(c storage.Character, assets []storage.Asset, cleared []uint16, gold, coin *int64) (Packet, error) {
	if len(assets)+len(cleared) > 370 || gold != nil && (*gold < 0 || *gold > math.MaxUint32) {
		return Packet{}, errShape
	}
	w := writer{0}
	n := len(assets) + len(cleared)
	if gold != nil {
		n++
	}
	if coin != nil {
		if *coin < 0 || *coin > math.MaxUint32 {
			return Packet{}, errShape
		}
		n++
	}
	w.u16(uint16(n))
	if gold != nil {
		w.raw(walletRow(0, uint32(min(*gold, math.MaxInt32))))
	}
	if coin != nil {
		w.raw(walletRow(1, uint32(*coin)))
	}
	rows := slices.Clone(assets)
	slices.SortFunc(rows, func(a, b storage.Asset) int { return int(a.Slot) - int(b.Slot) })
	seen := map[uint16]bool{}
	for _, a := range rows {
		if a.Location != 0 || seen[a.Slot] {
			return Packet{}, errShape
		}
		if a.CharacterID != c.ID && a.CharacterID != 0 {
			return Packet{}, errShape
		}
		r, _, err := ordinaryRow(a, c)
		if err != nil {
			return Packet{}, err
		}
		w.raw(r.body)
		seen[a.Slot] = true
	}
	slots := slices.Clone(cleared)
	slices.Sort(slots)
	for _, slot := range slots {
		if seen[slot] || (slot < 3 || slot > 360) && (slot < 363 || slot > 374) {
			return Packet{}, errShape
		}
		tombstone := writer{}
		tombstone.u16(slot)
		tombstone.u32(math.MaxUint32)
		w.raw(append(tombstone, make([]byte, 113)...))
		seen[slot] = true
	}
	return Packet{14, w}, nil
}
