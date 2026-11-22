package storage

import (
	"encoding/json"
	"math"
	"time"
)

// MoveContainer covers personal/ shared cargo and the two ordinary prefixed
// bags. Avatar, pet and artifact profile rows stay with MoveSpecial.
func (x *CommerceTx) MoveContainer(q MoveRequest, now time.Time) error {
	validList := func(list byte) bool {
		return list == 0 || list == 2 || list == 3 || list == 7 || list == 12 || list == 38
	}
	sl, dl, ss, ds := q.SourceList, q.DestinationList, q.SourceSlot, q.DestinationSlot
	if !validList(sl) || !validList(dl) || sl == dl && ss == ds ||
		q.ActorIndex != math.MaxUint32 || q.State0 != 0 || q.State1 != 0 {
		return ErrInvalid
	}
	at := func(list byte, slot uint16) int {
		for i, a := range x.Inventory.Items {
			if a.Location == list && a.Slot == slot && a.Count != 0 {
				return i
			}
		}
		return -1
	}
	get := func(i int) Asset {
		if i < 0 {
			return Asset{}
		}
		return x.Inventory.Items[i]
	}
	si := at(sl, ss)
	src := get(si)
	t, err := containerTemplate(src)
	if err != nil {
		return err
	}
	automatic := ds == math.MaxUint16
	cargoCross := sl != dl && cargoList(sl) && cargoList(dl)
	if automatic {
		if si < 0 || dl == 3 || q.DestinationTemplate != 0 {
			return ErrInvalid
		}
		ds, err = x.selectContainerSlot(src, t, dl, at, q.Count, cargoCross, cargoCross && dl == 0)
		if err != nil {
			return err
		}
	}
	ti := at(dl, ds)
	dst := get(ti)
	hint := get(at(sl, ds)).TemplateID
	if sl == 0 && (ds == 1 || ds == 2) {
		hint = uint32(ds)
	}
	cargoHint := q.Count > 0 && cargoCross && q.DestinationStack == 0 && q.DestinationTemplate == hint && si >= 0
	if src.TemplateID != q.SourceTemplate || !automatic && !cargoHint && dst.TemplateID != q.DestinationTemplate ||
		si < 0 && ti < 0 || q.DestinationStack != 0 && q.DestinationStack != dst.Count {
		return ErrInvalid
	}
	if cargoHint && ti >= 0 && !containerStack(src, dst) {
		ds, err = x.selectContainerSlot(src, t, dl, at, q.Count, true, dl == 0)
		if err != nil {
			return err
		}
		ti = at(dl, ds)
		dst = get(ti)
	}
	x.moveDestination, x.moveCount = ds, q.Count
	if _, err = x.tx.ExecContext(x.ctx, "SET CONSTRAINTS s9a1.asset_position DEFERRED"); err != nil {
		return err
	}
	move := func(i int, list byte, slot uint16) error {
		if i < 0 {
			return nil
		}
		a, err := x.containerDestination(x.Inventory.Items[i], list, slot, now)
		if err != nil {
			return err
		}
		return x.moveContainerRow(i, a)
	}
	if q.Count == 0 {
		if err = move(si, dl, ds); err != nil {
			return err
		}
		return move(ti, sl, ss)
	}
	if si < 0 {
		if ti < 0 || dl != 3 || q.Count != 1 {
			return ErrInvalid
		}
		return move(ti, sl, ss)
	}
	if q.Count > src.Count {
		return ErrInvalid
	}
	if _, err = x.containerDestination(src, dl, ds, now); err != nil {
		return err
	}
	compatible := ti < 0 || containerStack(src, dst)
	if t.Kind == 2 && cargoCross && compatible {
		return x.transferCargoStack(q, si, ti, ds, t, at, now)
	}
	if t.Kind == 2 && compatible {
		if q.Count > t.StackLimit-dst.Count {
			return ErrCapacity
		}
		return x.transferContainerCount(si, ti, dl, ds, q.Count, now)
	}
	if q.Count != src.Count {
		return ErrInvalid
	}
	if err = move(si, dl, ds); err != nil {
		return err
	}
	return move(ti, sl, ss)
}

func cargoList(n byte) bool { return n == 0 || n == 2 || n == 12 }

func containerTemplate(a Asset) (ordinaryTemplate, error) {
	var t ordinaryTemplate
	if a.UID == "" {
		return t, nil
	}
	if a.Source == "weapon" {
		return moveTemplate(&a)
	}
	if json.Unmarshal(a.Template, &t) != nil || t.TemplateID != a.TemplateID || t.Kind < 0 || t.Kind > 2 ||
		t.StackLimit == 0 || a.Count > t.StackLimit || t.FateCollection || t.AccountCommonInventory {
		return t, ErrInvalid
	}
	return t, nil
}

func containerStack(a, b Asset) bool {
	// Ownership is separate from stack identity while crossing a bank boundary.
	if a.Source == "shared" || b.Source == "shared" {
		ok, err := compatibleMaterial(a, b)
		return err == nil && ok
	}
	return stacks(&a, &b)
}

func (x *CommerceTx) containerRange(t ordinaryTemplate, list byte) (int, int) {
	switch list {
	case 0:
		return int(t.FirstBagSlot), int(t.bagEnd(x.Inventory.BagExpansion))
	case 2:
		return 0, int(x.Inventory.PersonalCapacity) - 1
	case 7:
		return 216, 271
	case 12:
		return 0, int(x.Inventory.CargoCapacity) - 1
	case 38:
		return 0, 55
	}
	return 1, 0
}

func (x *CommerceTx) selectContainerSlot(src Asset, t ordinaryTemplate, list byte, at func(byte, uint16) int, count uint32, partial, quick bool) (uint16, error) {
	first, last := x.containerRange(t, list)
	start := first
	if quick {
		start = 3
	}
	if t.Kind == 2 && count > 0 {
		for slot := start; slot <= last; slot++ {
			if slot < first && (slot < 3 || slot > 8) {
				continue
			}
			i := at(list, uint16(slot))
			if i < 0 {
				continue
			}
			row := x.Inventory.Items[i]
			if row.UID != src.UID && row.Count < t.StackLimit &&
				(partial || count <= t.StackLimit-row.Count) && containerStack(src, row) {
				return uint16(slot), nil
			}
		}
	}
	for slot := first; slot <= last; slot++ {
		if at(list, uint16(slot)) < 0 {
			return uint16(slot), nil
		}
	}
	return 0, ErrCapacity
}

func (x *CommerceTx) containerDestination(a Asset, list byte, slot uint16, now time.Time) (Asset, error) {
	t, err := containerTemplate(a)
	if err != nil {
		return a, err
	}
	attr, err := movementAttributes(&a)
	if err != nil || attr.Locked || list == 3 && attr.ExpiresAt != nil && *attr.ExpiresAt <= now.Unix() {
		return a, ErrInvalid
	}
	medal := t.Kind == 1 && t.WornSlotIsNative && t.WornSlot == 32
	first, last := x.containerRange(t, list)
	valid := int(slot) >= first && int(slot) <= last
	if t.PetConsumable && list != 7 {
		return a, ErrInvalid
	}
	if list == 7 && !t.PetConsumable || list == 38 && !medal || list == 0 && medal {
		return a, ErrInvalid
	}
	a.Location, a.Slot, a.CharacterID = list, slot, x.Character.ID
	if a.Source == "shared" {
		a.Source = "item"
	}
	if list == 12 {
		a.CharacterID = 0
	}
	if list == 0 {
		valid = valid || slot >= 3 && slot <= 8
		if fixed, ok := SharedMaterialSlot(t.TemplateID); ok && slot == fixed {
			var d materialDefinition
			if json.Unmarshal(a.Template, &d) != nil {
				return a, ErrInvalid
			}
			plain, err := plainMaterial(a, d)
			if err != nil {
				return a, err
			}
			if !plain {
				return a, ErrInvalid
			}
			valid = true
			a.Source, a.CharacterID = "shared", 0
		}
	}
	if list == 3 {
		ch := x.Character
		if t.WornSlot >= 12 && (t.WornSlot <= 23 || t.WornSlot == 25) && x.conquerorExpiry > now.Unix() {
			ch.Level = byte(min(255, int(ch.Level)+10))
		}
		valid = allowsEquipment(ch, &a, t, slot)
	}
	if !valid {
		return a, ErrInvalid
	}
	return a, nil
}

func (x *CommerceTx) moveContainerRow(i int, a Asset) error {
	var owner any
	if a.CharacterID != 0 {
		owner = a.CharacterID
	}
	prior := x.Inventory.Items[i]
	_, err := x.tx.ExecContext(x.ctx, `UPDATE s9a1.assets SET source=$2,character_id=$3,location=$4,slot=$5,count=$6 WHERE instance_id=$1`,
		a.UID, a.Source, owner, a.Location, a.Slot, a.Count)
	if err == nil {
		if prior.CharacterID == 0 || a.CharacterID == 0 {
			x.Inventory.AssetRevision++
		}
		x.Inventory.Items[i] = a
	}
	return err
}

func (x *CommerceTx) transferContainerCount(si, ti int, list byte, slot uint16, count uint32, now time.Time) error {
	src := x.Inventory.Items[si]
	if ti >= 0 {
		target, err := x.containerDestination(x.Inventory.Items[ti], list, slot, now)
		if err != nil {
			return err
		}
		target.Count += count
		if err = x.moveContainerRow(ti, target); err != nil {
			return err
		}
		return x.debitAsset(si, count)
	}
	next, err := x.containerDestination(src, list, slot, now)
	if err != nil {
		return err
	}
	if count == src.Count {
		return x.moveContainerRow(si, next)
	}
	if err = x.debitAsset(si, count); err != nil {
		return err
	}
	next.UID, err = newMailUUID()
	if err != nil {
		return err
	}
	next.Count = count
	if err = insertCommerceAsset(x.ctx, x.tx, x.account, next); err != nil {
		return err
	}
	if next.CharacterID == 0 {
		x.Inventory.AssetRevision++
	}
	x.Inventory.Items = append(x.Inventory.Items, next)
	return nil
}

func (x *CommerceTx) transferCargoStack(q MoveRequest, si, ti int, requested uint16, t ordinaryTemplate, at func(byte, uint16) int, now time.Time) error {
	src := x.Inventory.Items[si]
	left := q.Count
	updated := uint32(0)
	list := q.DestinationList
	first, last := x.containerRange(t, list)
	eligible := func(slot uint16) bool {
		return int(slot) >= first && int(slot) <= last || list == 0 && slot >= 3 && slot <= 8
	}
	merge := func(i int) error {
		if i < 0 || left == 0 {
			return nil
		}
		a := x.Inventory.Items[i]
		if !eligible(a.Slot) || a.Count >= t.StackLimit || !containerStack(src, a) {
			return nil
		}
		a, err := x.containerDestination(a, list, a.Slot, now)
		if err != nil {
			return err
		}
		n := min(left, t.StackLimit-a.Count)
		left -= n
		a.Count += n
		if updated == 0 {
			updated = n
			x.moveDestination, x.moveCount = a.Slot, n
		}
		return x.moveContainerRow(i, a)
	}
	if err := merge(ti); err != nil {
		return err
	}
	start := first
	if list == 0 {
		start = 3
	}
	for slot := start; slot <= last && left > 0; slot++ {
		i := at(list, uint16(slot))
		if i >= 0 && i != ti {
			if err := merge(i); err != nil {
				return err
			}
		}
	}
	if left == 0 {
		return x.debitAsset(si, q.Count)
	}
	free := requested
	if !eligible(free) || at(list, free) >= 0 {
		n := first
		for n <= last && at(list, uint16(n)) >= 0 {
			n++
		}
		if n > last {
			return ErrCapacity
		}
		free = uint16(n)
	}
	if updated == 0 {
		x.moveDestination, x.moveCount = free, left
	}
	next, err := x.containerDestination(src, list, free, now)
	if err != nil {
		return err
	}
	next.Count = left
	if q.Count == src.Count {
		return x.moveContainerRow(si, next)
	}
	if err = x.debitAsset(si, q.Count); err != nil {
		return err
	}
	next.UID, err = newMailUUID()
	if err != nil {
		return err
	}
	if err = insertCommerceAsset(x.ctx, x.tx, x.account, next); err != nil {
		return err
	}
	if next.CharacterID == 0 {
		x.Inventory.AssetRevision++
	}
	x.Inventory.Items = append(x.Inventory.Items, next)
	return nil
}
