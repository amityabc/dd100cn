package storage

import (
	"crypto/rand"
	"encoding/binary"
	"encoding/json"
	"math"
	"math/big"
	"slices"
	"time"
)

func (x *CommerceTx) grantCommerceItem(template json.RawMessage, quantity uint32, now time.Time, preferQuick bool) error {
	return x.grantGiftItem(template, quantity, now, preferQuick, giftEquipment{})
}

type giftEquipment struct {
	epicExchange, disjointRestricted, banTransportStone bool
	upgrade                                             byte
	qualitySeed                                         uint32
	plainGM                                             bool
}

func (x *CommerceTx) grantGiftItem(template json.RawMessage, quantity uint32, now time.Time, preferQuick bool, flags giftEquipment) error {
	var t ordinaryTemplate
	var lifetime materialDefinition
	if json.Unmarshal(template, &t) != nil || json.Unmarshal(template, &lifetime) != nil || t.TemplateID == 0 ||
		t.Kind < 0 || t.Kind > 2 || quantity == 0 || t.StackLimit == 0 || t.Kind != 2 && t.StackLimit != 1 ||
		t.FateCollection || t.TemplateID == math.MaxUint32 {
		return ErrInvalid
	}
	qualitySeed := flags.qualitySeed
	if flags.plainGM {
		lifetime = materialDefinition{Kind: t.Kind}
	}
	flags.qualitySeed, flags.plainGM = 0, false
	attr, core, err := commerceItemCore(t, lifetime, now)
	if err != nil {
		return err
	}
	if flags != (giftEquipment{}) || qualitySeed != 0 {
		if t.Kind == 2 || flags.upgrade != 0 && !lifetime.PvfGiftItem || qualitySeed >= 999999999 {
			return ErrInvalid
		}
		var attrs map[string]json.RawMessage
		if err = json.Unmarshal(attr, &attrs); err != nil {
			return err
		}
		values := map[string]any{}
		if flags != (giftEquipment{}) {
			values = map[string]any{"EpicExchangeInstance": flags.epicExchange, "DisjointRestricted": flags.disjointRestricted,
				"BanTransportStone": flags.banTransportStone, "Reinforcement": flags.upgrade}
		}
		if qualitySeed != 0 {
			values["QualitySeed"] = qualitySeed
		}
		for k, v := range values {
			attrs[k], err = json.Marshal(v)
			if err != nil {
				return err
			}
		}
		attr, err = json.Marshal(attrs)
		if err != nil {
			return err
		}
		core[10] = flags.upgrade
	}
	a := Asset{Source: "item", CharacterID: x.Character.ID, TemplateID: t.TemplateID, Template: template, Attributes: attr, Core: core, Detail: []byte{}}
	if err = attachCommerceWeapon(&a, t); err != nil {
		return err
	}
	first, last := t.FirstBagSlot, t.bagEnd(x.Inventory.BagExpansion)
	sharedSlot, shared := SharedMaterialSlot(t.TemplateID)
	switch {
	case shared:
		ok, err := plainMaterial(a, lifetime)
		if err != nil {
			return err
		}
		if !ok {
			return ErrInvalid
		}
		a.Source, a.CharacterID = "shared", 0
		first, last = sharedSlot, sharedSlot
	case t.AccountCommonInventory:
		if t.Kind != 2 || quantity > math.MaxInt32 {
			return ErrInvalid
		}
		a.Location, a.CharacterID = 45, 0
		first, last, t.StackLimit = 0, 49, min(t.StackLimit, math.MaxInt32)
	case t.PetConsumable:
		a.Location = 7
		first, last = 216, 271
	case t.Kind == 1 && t.WornSlotIsNative && t.WornSlot == 32:
		a.Location = 38
		first, last = 0, 55
	default:
		if first < 3 || first > last || last > 360 {
			return ErrInvalid
		}
	}
	left := quantity
	slots := map[uint16]int{}
	for i, row := range x.Inventory.Items {
		if row.Count > 0 && row.Location == a.Location && row.CharacterID == a.CharacterID {
			slots[row.Slot] = i
		}
	}
	if t.Kind == 2 {
		for slot := uint16(0); slot <= last && left > 0; slot++ {
			i, ok := slots[slot]
			if !ok {
				continue
			}
			row := &x.Inventory.Items[i]
			if row.Count >= t.StackLimit || row.TemplateID != a.TemplateID {
				continue
			}
			same := stacks(row, &a)
			if shared {
				same, err = compatibleMaterial(*row, a)
				if err != nil {
					return err
				}
			}
			if !same {
				continue
			}
			take := min(left, t.StackLimit-row.Count)
			if _, err = x.tx.ExecContext(x.ctx, "UPDATE s9a1.assets SET count=count+$2 WHERE instance_id=$1", row.UID, take); err != nil {
				return err
			}
			row.Count += take
			left -= take
			delta := *row
			delta.Count = take
			x.Grants = append(x.Grants, delta)
		}
	}
	var positions []uint16
	if preferQuick && a.Location == 0 && t.Kind == 2 && t.FirstBagSlot == 65 && t.LastBagSlot == 120 {
		positions = append(positions, 3, 4, 5, 6, 7, 8)
	}
	for slot := first; slot <= last; slot++ {
		positions = append(positions, slot)
	}
	for _, slot := range positions {
		if left == 0 {
			break
		}
		if _, taken := slots[slot]; taken {
			continue
		}
		a.UID, err = newMailUUID()
		if err != nil {
			return err
		}
		a.Slot, a.Count = slot, min(left, t.StackLimit)
		err = insertCommerceAsset(x.ctx, x.tx, x.account, a)
		if err != nil {
			return err
		}
		x.Inventory.Items = append(x.Inventory.Items, a)
		x.Grants = append(x.Grants, a)
		left -= a.Count
	}
	if left != 0 {
		return ErrCapacity
	}
	if a.CharacterID == 0 {
		x.Inventory.AssetRevision++
	}
	slices.SortFunc(x.Grants, func(a, b Asset) int {
		if a.Location != b.Location {
			return int(a.Location) - int(b.Location)
		}
		return int(a.Slot) - int(b.Slot)
	})
	if len(x.Grants) != 0 {
		x.Granted = x.Grants[0]
	}
	return nil
}

func commerceItemCore(t ordinaryTemplate, d materialDefinition, now time.Time) (json.RawMessage, []byte, error) {
	core := make([]byte, 119)
	attr := materialAttributes{Durability: t.MaximumDurability, ExpiresAt: d.ResourceExpiration}
	if d.InitialSealed {
		attr.Binding = 1
		core[13] = 1
	}
	choose := func(exp int64) {
		if attr.ExpiresAt == nil || exp < *attr.ExpiresAt {
			attr.ExpiresAt = &exp
		}
	}
	if d.RelativeLifetimeSeconds != 0 {
		choose(now.Unix() + int64(d.RelativeLifetimeSeconds))
	}
	if d.DailyDeletion {
		_, reset := CommerceWindow(1, now, CommercePurchase{})
		choose(reset)
	} else if d.WeeklyDeletionDay != nil {
		local := now.In(commerceZone)
		reset := time.Date(local.Year(), local.Month(), local.Day(), 6, 0, 0, 0, commerceZone)
		reset = reset.AddDate(0, 0, (7+int(*d.WeeklyDeletionDay)-int(reset.Weekday()))%7)
		if !reset.After(now) {
			reset = reset.AddDate(0, 0, 7)
		}
		choose(reset.Unix())
	}
	if d.FatigueTimeMonthlyDeletion {
		_, reset := CommerceWindow(102, now, CommercePurchase{})
		choose(reset)
	}
	if attr.ExpiresAt != nil {
		if *attr.ExpiresAt <= now.Unix() || *attr.ExpiresAt > math.MaxInt32 {
			return nil, nil, ErrInvalid
		}
		binary.LittleEndian.PutUint32(core[56:], uint32(*attr.ExpiresAt))
	}
	if t.Kind != 2 {
		quality, err := rand.Int(rand.Reader, big.NewInt(999999998))
		if err != nil {
			return nil, nil, err
		}
		attr.QualitySeed = uint32(quality.Uint64() + 1)
	}
	attrs, err := frozenMaterialObject([]byte("{}"), materialAttributeDefaults)
	if err != nil {
		return nil, nil, err
	}
	for k, v := range map[string]any{"QualitySeed": attr.QualitySeed, "Durability": attr.Durability, "Binding": attr.Binding, "ExpiresAt": attr.ExpiresAt} {
		raw, err := json.Marshal(v)
		if err != nil {
			return nil, nil, err
		}
		attrs[k] = raw
	}
	raw, err := json.Marshal(attrs)
	return raw, core, err
}
