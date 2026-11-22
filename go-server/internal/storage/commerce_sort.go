package storage

import (
	"cmp"
	"encoding/json"
	"math"
	"slices"
)

type SortRequest struct{ List, Category, Order uint32 }

type SortMetadata struct {
	ResourceHash string
	Info         struct {
		Grade       int
		Type        string
		EmblemColor *uint16
	}
}

type sortRow struct {
	uid        string
	slot, part uint16
	id         uint32
	hash       string
	level      byte
	grade      int
	typ        string
	color      uint16
	expiry     int64
	locked     bool
}

func (x *CommerceTx) Sort(q SortRequest, values map[uint32]SortMetadata) error {
	if q.List == 1 || q.List == 7 {
		return x.sortSpecial(q, values)
	}
	first, last, err := x.sortRange(q)
	if err != nil {
		return err
	}
	var rows []sortRow
	for _, a := range x.Inventory.Items {
		if uint32(a.Location) != q.List || a.CharacterID != x.Character.ID || a.Slot < first || a.Slot > last {
			continue
		}
		t, err := moveTemplate(&a)
		if err != nil {
			return err
		}
		if q.List == 0 && (a.Source == "shared" || t.FirstBagSlot != first ||
			(q.Category != 9 && t.LastBagSlot != first+55 || q.Category == 9 && t.LastBagSlot != 360)) {
			return ErrInvalid
		}
		at, err := movementAttributes(&a)
		if err != nil {
			return err
		}
		r := sortRow{uid: a.UID, slot: a.Slot, id: a.TemplateID, hash: t.ResourceHash, level: t.MinimumLevel, part: t.WornSlot, expiry: math.MaxInt64, locked: at.Locked}
		if at.ExpiresAt != nil {
			r.expiry = *at.ExpiresAt
		}
		if q.Category == 1 && q.Order == 3 && t.Kind != 0 && !t.WornSlotIsNative {
			return ErrInvalid
		}
		rows = append(rows, r)
	}
	moves, err := sortTargets(rows, first, last, q, values)
	if err != nil {
		return err
	}
	if len(moves) == 0 {
		x.noChange = true
		return nil
	}
	if _, err = x.tx.ExecContext(x.ctx, "SET CONSTRAINTS s9a1.asset_position DEFERRED"); err != nil {
		return err
	}
	for uid, slot := range moves {
		if _, err = x.tx.ExecContext(x.ctx, "UPDATE s9a1.assets SET slot=$2 WHERE instance_id=$1", uid, slot); err != nil {
			return err
		}
	}
	return nil
}

func (x *CommerceTx) sortRange(q SortRequest) (uint16, uint16, error) {
	if q.List == 2 && q.Category == 20 && q.Order == 0 && x.Inventory.PersonalCapacity > 0 {
		return 0, x.Inventory.PersonalCapacity - 1, nil
	}
	if q.List != 0 {
		return 0, 0, ErrInvalid
	}
	type tab struct {
		first  uint16
		orders []uint32
	}
	tabs := map[uint32]tab{
		1: {9, []uint32{0, 1, 2, 3}}, 2: {65, []uint32{0, 3, 4}}, 3: {121, []uint32{0, 6}},
		4: {177, []uint32{0}}, 9: {289, []uint32{0, 9, 10}}, 10: {233, []uint32{0, 1, 5}},
	}
	t := tabs[q.Category]
	if !slices.Contains(t.orders, q.Order) {
		return 0, 0, ErrInvalid
	}
	if q.Category == 9 {
		return t.first, 360, nil
	}
	return t.first, min(t.first+31+x.Inventory.BagExpansion, t.first+55), nil
}

func sortTargets(rows []sortRow, first, last uint16, q SortRequest, values map[uint32]SortMetadata) (map[string]uint16, error) {
	occupied := map[uint16]bool{}
	var movable []sortRow
	for _, r := range rows {
		if r.locked {
			occupied[r.slot] = true
			continue
		}
		info, ok := values[r.id]
		metadata := q.Order == 1 || q.Order == 5 || q.Order == 6 || q.Order == 9 || q.Order == 10 || q.Order == 3 && q.List == 0 && q.Category == 2
		if metadata {
			if !ok || info.ResourceHash != r.hash || q.Order == 10 && info.Info.EmblemColor == nil {
				return nil, ErrInvalid
			}
			r.grade, r.typ = info.Info.Grade, info.Info.Type
			if info.Info.EmblemColor != nil {
				r.color = *info.Info.EmblemColor
			}
		}
		movable = append(movable, r)
	}
	slices.SortFunc(movable, func(a, b sortRow) int {
		n := 0
		switch q.Order {
		case 0:
			return cmp.Compare(a.slot, b.slot)
		case 1, 9:
			n = cmp.Compare(b.grade, a.grade)
		case 2:
			n = cmp.Compare(b.level, a.level)
		case 3:
			if q.Category == 1 || q.List == 1 {
				n = cmp.Compare(a.part, b.part)
			} else {
				n = cmp.Compare(a.typ, b.typ)
			}
		case 4:
			n = cmp.Compare(a.expiry, b.expiry)
		case 5, 6:
			n = cmp.Compare(a.typ, b.typ)
		case 10:
			n = cmp.Compare(a.color, b.color)
		}
		if n != 0 {
			return n
		}
		if n = cmp.Compare(a.id, b.id); n != 0 {
			return n
		}
		return cmp.Compare(a.slot, b.slot)
	})
	moves := map[string]uint16{}
	slot := first
	for _, r := range movable {
		for occupied[slot] {
			slot++
		}
		if slot > last {
			return nil, ErrCapacity
		}
		if slot != r.slot {
			moves[r.uid] = slot
		}
		slot++
	}
	return moves, nil
}

func (x *CommerceTx) sortSpecial(q SortRequest, values map[uint32]SortMetadata) error {
	if q.List == 1 && (q.Category != 8 || q.Order != 0 && q.Order != 1 && q.Order != 3) ||
		q.List == 7 && (q.Category != 6 || q.Order != 0) {
		return ErrInvalid
	}
	var profile struct{ SpecialAssets map[string]json.RawMessage }
	if err := json.Unmarshal(x.Character.Gameplay, &profile); err != nil {
		return err
	}
	state := profile.SpecialAssets
	var items []map[string]json.RawMessage
	if v := state["Items"]; len(v) != 0 {
		if err := json.Unmarshal(v, &items); err != nil {
			return err
		}
	}
	last := uint16(159)
	kind := byte(1)
	if q.List == 1 {
		var expansion uint16
		if v := state["AvatarExpansion"]; len(v) != 0 {
			if err := json.Unmarshal(v, &expansion); err != nil {
				return err
			}
		}
		if expansion > 360 || expansion%8 != 0 {
			return ErrInvalid
		}
		last, kind = 119+expansion, 0
	}
	var rows []sortRow
	type special struct {
		InstanceID string
		Location   byte
		Slot       uint16
		Template   struct {
			TemplateID   uint32
			WornSlot     uint16
			ResourceHash string
			Kind         byte
		}
		Attributes struct{ Locked bool }
	}
	indices := map[string]int{}
	for i, fields := range items {
		raw, err := json.Marshal(fields)
		if err != nil {
			return err
		}
		var p special
		if err = json.Unmarshal(raw, &p); err != nil {
			return err
		}
		if uint32(p.Location) != q.List || p.Template.Kind != kind {
			continue
		}
		if !mailUID(p.InstanceID) || p.Slot > last {
			return ErrInvalid
		}
		indices[p.InstanceID] = i
		rows = append(rows, sortRow{uid: p.InstanceID, slot: p.Slot, id: p.Template.TemplateID,
			hash: p.Template.ResourceHash, part: p.Template.WornSlot, locked: p.Attributes.Locked})
	}
	moves, err := sortTargets(rows, 0, last, q, values)
	if err != nil {
		return err
	}
	if len(moves) == 0 {
		x.noChange = true
		return nil
	}
	for uid, to := range moves {
		raw, err := json.Marshal(to)
		if err != nil {
			return err
		}
		items[indices[uid]]["Slot"] = raw
	}
	state["Items"], err = json.Marshal(items)
	if err != nil {
		return err
	}
	return replaceMailFields(&x.Character, map[string]any{"SpecialAssets": state})
}
