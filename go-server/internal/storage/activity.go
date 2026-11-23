package storage

import (
	"encoding/json"
	"slices"
	"strings"
	"time"

	"github.com/google/uuid"
)

func RubyBoxGrants(c Character, in Inventory, templates map[uint32]json.RawMessage, ids []uint32, now time.Time) (Inventory, error) {
	counts := map[uint32]uint32{}
	order := []uint32{}
	for _, id := range ids {
		if counts[id] == 0 {
			order = append(order, id)
		}
		counts[id]++
	}
	attr, err := InitialItemAttributes(0, 0)
	if err != nil {
		return in, err
	}
	grants := make([]QuestGrant, 0, len(order))
	for _, id := range order {
		raw := templates[id]
		var t ordinaryTemplate
		if json.Unmarshal(raw, &t) != nil || t.TemplateID != id || t.Kind != 2 || t.StackLimit == 0 || t.FirstBagSlot < 3 {
			return in, ErrInvalid
		}
		a := Asset{Source: "item", CharacterID: c.ID, TemplateID: id, Count: counts[id], Template: raw, Attributes: attr, Core: make([]byte, 119), Detail: []byte{}}
		grants = append(grants, QuestGrant{Asset: a, Kind: 2, First: t.FirstBagSlot, Last: t.bagEnd(in.BagExpansion), StackLimit: t.StackLimit, QuickSlots: t.FirstBagSlot == 65 && t.LastBagSlot == 120 && !t.AccountCommonInventory && !t.PetConsumable})
	}
	return QuestLoot(in, nil, grants, in.Gold, now)
}

func ActivityHealth(c Character, restore bool) (Character, error) {
	var gameplay struct{ Progression map[string]json.RawMessage }
	if err := json.Unmarshal(c.Gameplay, &gameplay); err != nil {
		return c, err
	}
	if gameplay.Progression == nil {
		return c, ErrInvalid
	}
	hp := int64(0)
	if restore {
		h, m, err := dungeonVitals(c)
		if err != nil {
			return c, err
		}
		hp = h
		gameplay.Progression["Mp"], err = json.Marshal(m)
		if err != nil {
			return c, err
		}
	}
	h, err := json.Marshal(hp)
	if err != nil {
		return c, err
	}
	gameplay.Progression["Hp"] = h
	err = replaceMailFields(&c, map[string]any{"Progression": gameplay.Progression})
	return c, err
}

func ActivityCollection(in Inventory, slot uint16, raw json.RawMessage) (Inventory, error) {
	var t struct {
		TemplateID     uint32
		Kind           int
		FateCollection bool
	}
	if err := json.Unmarshal(raw, &t); err != nil {
		return in, err
	}
	if !t.FateCollection || t.Kind != 2 || t.TemplateID == 0 || slot >= 24 {
		return in, ErrInvalid
	}
	for _, a := range in.Items {
		if a.Location == 44 && a.Slot == slot {
			if a.CharacterID != 0 || a.Source != "item" || a.Count != 1 || a.TemplateID != t.TemplateID || !sameCommerceTemplate(raw, a.Template) {
				return in, ErrInvalid
			}
			return in, nil
		}
	}
	id, err := uuid.NewRandom()
	if err != nil {
		return in, err
	}
	attr, err := InitialItemAttributes(0, 0)
	if err != nil {
		return in, err
	}
	a := Asset{UID: strings.ReplaceAll(id.String(), "-", ""), Source: "item", TemplateID: t.TemplateID, Count: 1, Location: 44, Slot: slot, Template: raw, Attributes: attr, Core: make([]byte, 119), Detail: []byte{}}
	in.Items = append(slices.Clone(in.Items), a)
	return in, nil
}

func ActivityReceipt(c Character, prior map[string]json.RawMessage, key string, input, result string, grants json.RawMessage) (map[string]json.RawMessage, error) {
	var items = []json.RawMessage{}
	if grants != nil {
		if err := json.Unmarshal(grants, &items); err != nil {
			return nil, err
		}
	}
	if raw, ok := prior[key]; ok {
		var old deathReceipt
		if err := json.Unmarshal(raw, &old); err != nil {
			return nil, err
		}
		if old.Key != key || old.Input != input || old.Result != result {
			return nil, ErrConflict
		}
		return nil, nil
	}
	b, err := json.Marshal(deathReceipt{Key: key, Input: input, Result: result, Revision: c.Revision + 1, Grants: items})
	if err != nil {
		return nil, err
	}
	return map[string]json.RawMessage{key: b}, nil
}
