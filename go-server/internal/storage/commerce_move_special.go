package storage

import (
	"encoding/json"
	"math"
	"time"
)

type AvatarClone struct {
	ID      uint32
	Hash    string
	Slot    uint16
	IsClone bool
}

type specialMoveRow struct {
	Location   byte
	Slot       uint16
	Template   specialPurchaseTemplate
	Attributes struct {
		Locked    bool
		ExpiresAt *int64
	}
	Avatar *struct{ Option uint32 }
}

func SpecialMoveRoute(q MoveRequest) bool {
	special := func(list byte, slot uint16) bool {
		return list == 1 || list == 7 && slot < 216 || (list == 3 || list == 17) && (slot <= 11 || slot >= 26 && slot <= 29 || slot == 33)
	}
	return special(q.SourceList, q.SourceSlot) || special(q.DestinationList, q.DestinationSlot)
}

func (x *CommerceTx) MoveSpecial(q MoveRequest, clones map[uint32]AvatarClone, now time.Time) error {
	if q.Count > 1 || q.DestinationStack > 1 || q.ActorIndex != math.MaxUint32 || q.State0 != 0 || q.State1 != 0 {
		return ErrInvalid
	}
	state, raw, expansion, err := x.loadSpecial()
	if err != nil {
		return err
	}
	sl, dl := q.SourceList, q.DestinationList
	ss, ds := q.SourceSlot, q.DestinationSlot
	worn := func(list byte, slot uint16) bool { return list == 3 && (slot == 26 || slot == 33) }
	avatar := func(list byte, slot uint16) bool { return list == 1 || (list == 3 || list == 17) && slot <= 11 }
	kind, bag, first, last := byte(2), byte(7), uint16(160), uint16(215)
	switch {
	case avatar(sl, ss) || avatar(dl, ds):
		kind, bag, first, last = 0, 1, 0, 119+expansion
		if sl == 17 {
			sl = 3
		}
		if dl == 17 {
			dl = 3
		}
	case worn(sl, ss) || worn(dl, ds) || sl == 7 && dl == 7 && ss <= 159 && ds <= 159:
		kind, first, last = 1, 0, 159
	}
	bagOnly := sl == bag && dl == bag
	bagToWorn, wornToBag := sl == bag && dl == 3, sl == 3 && dl == bag
	doubleWorn := sl == 3 && dl == 3 && ss != ds &&
		(kind == 1 && worn(sl, ss) && worn(dl, ds) || kind == 0 && (ss == 9 || ss == 11) && (ds == 9 || ds == 11))
	if !bagOnly && !bagToWorn && !wornToBag && !doubleWorn {
		return ErrInvalid
	}
	if bagOnly && (ss < first || ss > last || ds < first || ds > last || ss == ds) {
		return ErrInvalid
	}
	pairs := map[[2]uint16]int{}
	rows := make([]specialMoveRow, len(raw))
	for i, b := range raw {
		if err = json.Unmarshal(b, &rows[i]); err != nil {
			return err
		}
		r := rows[i]
		key := [2]uint16{uint16(r.Location), r.Slot}
		if _, exists := pairs[key]; exists {
			return ErrInvalid
		}
		pairs[key] = i
	}
	at := func(list byte, slot uint16) (int, *specialMoveRow) {
		i, ok := pairs[[2]uint16{uint16(list), slot}]
		if !ok {
			return -1, nil
		}
		return i, &rows[i]
	}
	if bagToWorn || wornToBag {
		ps, ws, hint := &ss, ds, q.SourceTemplate
		if wornToBag {
			ps, ws, hint = &ds, ss, q.DestinationTemplate
		}
		wearable := kind == 0 && ws <= 11 || kind == 1 && (ws == 26 || ws == 33) || kind == 2 && ws >= 27 && ws <= 29
		if !wearable {
			return ErrInvalid
		}
		if *ps == math.MaxUint16 {
			_, equipped := at(3, ws)
			if equipped == nil || hint != 0 {
				return ErrInvalid
			}
			slot := first
			for slot <= last {
				if _, r := at(bag, slot); r == nil {
					break
				}
				slot++
			}
			if slot > last {
				return ErrCapacity
			}
			*ps = slot
		}
		if *ps < first || *ps > last {
			return ErrInvalid
		}
	}
	si, source := at(sl, ss)
	ti, target := at(dl, ds)
	id := func(r *specialMoveRow) uint32 {
		if r == nil {
			return 0
		}
		return r.Template.TemplateID
	}
	if id(source) != q.SourceTemplate || id(target) != q.DestinationTemplate || source == nil && target == nil ||
		source == nil && bagOnly && q.Count != 0 || source != nil && source.Template.Kind != kind || target != nil && target.Template.Kind != kind {
		return ErrInvalid
	}
	var profile struct{ AuraSkinUnlocked, PetSkinUnlocked bool }
	if err = json.Unmarshal(x.Character.Gameplay, &profile); err != nil {
		return err
	}
	move := func(index int, row, donor *specialMoveRow, list byte, slot uint16) error {
		if row == nil {
			return nil
		}
		if row.Attributes.Locked || row.Attributes.ExpiresAt != nil && *row.Attributes.ExpiresAt <= now.Unix() {
			return ErrInvalid
		}
		if list == 3 {
			t := row.Template
			fits := t.WornSlot == slot || kind == 1 && slot == 33 || kind == 0 && slot == 11 && t.WornSlot == 9
			level := x.Character.Level >= t.MinimumLevel || kind == 0 && x.conquerorExpiry > now.Unix() && int(x.Character.Level)+10 >= int(t.MinimumLevel)
			if !fits || !level || t.UsableJobs&(1<<x.Character.Job) == 0 || kind == 0 && slot == 11 && !profile.AuraSkinUnlocked ||
				kind == 1 && slot == 33 && !profile.PetSkinUnlocked {
				return ErrInvalid
			}
		}
		var fields map[string]json.RawMessage
		if err := json.Unmarshal(raw[index], &fields); err != nil {
			return err
		}
		loc, err := json.Marshal(list)
		if err != nil {
			return err
		}
		position, err := json.Marshal(slot)
		if err != nil {
			return err
		}
		fields["Location"], fields["Slot"] = loc, position
		if list == 3 && kind == 0 && slot <= 7 {
			if err = linkClone(fields, row, donor, slot, clones); err != nil {
				return err
			}
		}
		raw[index], err = json.Marshal(fields)
		return err
	}
	if err = move(si, source, target, dl, ds); err != nil {
		return err
	}
	if err = move(ti, target, source, sl, ss); err != nil {
		return err
	}
	state["Items"], err = json.Marshal(raw)
	if err != nil {
		return err
	}
	x.moveDestination = ds
	return replaceMailFields(&x.Character, map[string]any{"SpecialAssets": state})
}

func linkClone(fields map[string]json.RawMessage, row, donor *specialMoveRow, slot uint16, clones map[uint32]AvatarClone) error {
	t := row.Template
	r, ok := clones[t.TemplateID]
	if !ok || r.Hash != t.ResourceHash || r.Slot != t.WornSlot || r.Slot != slot {
		return ErrInvalid
	}
	if !r.IsClone {
		return nil
	}
	var choice uint32
	if donor != nil {
		t := donor.Template
		d, ok := clones[t.TemplateID]
		if !ok || d.Hash != t.ResourceHash || d.Slot != t.WornSlot || d.Slot != slot || donor.Avatar == nil {
			return ErrInvalid
		}
		choice = t.TemplateID
		if d.IsClone {
			choice = donor.Avatar.Option
		}
		if choice != 0 {
			v, ok := clones[choice]
			if !ok || v.Slot != slot || v.IsClone {
				return ErrInvalid
			}
		}
	}
	var avatar map[string]json.RawMessage
	if err := json.Unmarshal(fields["Avatar"], &avatar); err != nil || avatar == nil {
		return ErrInvalid
	}
	v, err := json.Marshal(choice)
	if err != nil {
		return err
	}
	avatar["Option"] = v
	fields["Avatar"], err = json.Marshal(avatar)
	return err
}
