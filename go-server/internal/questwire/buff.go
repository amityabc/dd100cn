package questwire

import (
	"encoding/json"
	"errors"
	"math"
	"s9a1/server/internal/storage"
	"slices"
	"time"
)

type BuffRequest struct {
	Skill      uint16
	Part, List byte
	Slot       uint16
}
type buffState struct {
	Skill uint16
	Items map[byte]string
}
type buffSource struct {
	ID         string
	Part, List byte
	Slot       uint16
}

func ReadBuff(body []byte) (BuffRequest, error) {
	if len(body) != 6 {
		return BuffRequest{}, errors.New("buff registration requires exactly six bytes")
	}
	return BuffRequest{little.Uint16(body), body[2], body[3], little.Uint16(body[4:])}, nil
}

func savedBuff(raw json.RawMessage) (*buffState, error) {
	if raw == nil || string(raw) == "null" {
		return nil, nil
	}
	var saved buffState
	if json.Unmarshal(raw, &saved) != nil || saved.Items == nil || len(saved.Items) > 38 {
		return nil, storage.ErrInvalid
	}
	seen := map[string]bool{}
	for part, id := range saved.Items {
		key := guidKey(id)
		if part >= 38 || key == "" || seen[key] {
			return nil, storage.ErrInvalid
		}
		seen[key] = true
	}
	return &saved, nil
}

func buffSources(c storage.Character, in storage.Inventory, now time.Time) ([]buffSource, error) {
	var sources []buffSource
	for _, a := range in.Items {
		if a.Location != 0 && a.Location != 3 || a.CharacterID != c.ID || a.Count == 0 {
			continue
		}
		var t struct {
			Kind             byte
			WornSlot         uint16
			WornSlotIsNative bool
		}
		if a.Source == "weapon" {
			t.WornSlot = 12
		} else if a.Source != "item" || json.Unmarshal(a.Template, &t) != nil || t.Kind != 1 || !t.WornSlotIsNative {
			continue
		}
		var v struct{ ExpiresAt *int64 }
		if json.Unmarshal(a.Attributes, &v) != nil {
			return nil, storage.ErrInvalid
		}
		if t.WornSlot >= 12 && t.WornSlot < 38 && (v.ExpiresAt == nil || *v.ExpiresAt > now.Unix()) && guidKey(a.UID) != "" {
			sources = append(sources, buffSource{wireGUID(a.UID), byte(t.WornSlot), a.Location, a.Slot})
		}
	}
	var special struct {
		SpecialAssets struct {
			Items []struct {
				InstanceId string
				Template   struct {
					Kind     byte
					WornSlot uint16
				}
				Location   byte
				Slot       uint16
				Attributes struct{ ExpiresAt *int64 }
			}
		}
	}
	if json.Unmarshal(c.Gameplay, &special) != nil {
		return nil, storage.ErrInvalid
	}
	for _, a := range special.SpecialAssets.Items {
		if a.Template.Kind <= 1 && a.Template.WornSlot < 38 && (a.Location == 1 || a.Location == 3 || a.Location == 7) && (a.Attributes.ExpiresAt == nil || *a.Attributes.ExpiresAt > now.Unix()) && guidKey(a.InstanceId) != "" {
			sources = append(sources, buffSource{wireGUID(a.InstanceId), byte(a.Template.WornSlot), a.Location, a.Slot})
		}
	}
	return sources, nil
}

func (cat *Catalog) RegisterBuff(c storage.Character, in storage.Inventory, r BuffRequest, now time.Time) (storage.TownQuestMutation, error) {
	var out storage.TownQuestMutation
	var doc skillPage
	var p struct {
		Initialized bool
		Skills      []questSkill
	}
	if c.ID != in.CharacterID || json.Unmarshal(c.Gameplay, &doc) != nil || json.Unmarshal(c.Gameplay, &p) != nil || !p.Initialized {
		return out, storage.ErrInvalid
	}
	if r.Skill != 0 {
		d, ok := cat.definition(c.Job, r.Skill)
		if !ok || !d.Active || !slices.ContainsFunc(p.Skills, func(s questSkill) bool { return s.Id == r.Skill && s.Level > 0 }) {
			return out, storage.ErrInvalid
		}
	}
	prior, err := savedBuff(doc["BuffRegistration"])
	if err != nil {
		return out, err
	}
	selection := r.Part == 38 && r.List == 49 && r.Slot == math.MaxUint16
	if !selection && (r.Part >= 38 || prior != nil && r.Skill != prior.Skill) {
		return out, storage.ErrInvalid
	}
	next := buffState{Skill: r.Skill, Items: map[byte]string{}}
	if prior != nil {
		for part, id := range prior.Items {
			next.Items[part] = id
		}
	}
	if !selection {
		if r.List == 49 && r.Slot == math.MaxUint16 {
			delete(next.Items, r.Part)
		} else {
			sources, err := buffSources(c, in, now)
			if err != nil {
				return out, err
			}
			i := slices.IndexFunc(sources, func(s buffSource) bool { return s.Part == r.Part && s.List == r.List && s.Slot == r.Slot })
			if i < 0 {
				return out, storage.ErrInvalid
			}
			next.Items[r.Part] = sources[i].ID
		}
	}
	if next.Skill == 0 && len(next.Items) == 0 {
		if prior == nil {
			return out, nil
		}
		delete(doc, "BuffRegistration")
	} else {
		raw, err := json.Marshal(next)
		if err != nil {
			return out, err
		}
		if _, err = savedBuff(raw); err != nil {
			return out, err
		}
		if prior != nil && prior.Skill == next.Skill && len(prior.Items) == len(next.Items) {
			same := true
			for part, id := range next.Items {
				same = same && wireGUID(id) == wireGUID(prior.Items[part])
			}
			if same {
				return out, nil
			}
		}
		doc["BuffRegistration"] = raw
	}
	c.Gameplay, err = json.Marshal(doc)
	if err != nil {
		return out, err
	}
	out.Reward = &storage.QuestRewardMutation{Character: c, Inventory: in}
	return out, nil
}

func BuffProjection(c storage.Character, in storage.Inventory, now time.Time) (uint16, []byte, error) {
	var p struct {
		BuffRegistration json.RawMessage
		Skills           []questSkill
	}
	if json.Unmarshal(c.Gameplay, &p) != nil {
		return 0, nil, storage.ErrInvalid
	}
	state, err := savedBuff(p.BuffRegistration)
	if err != nil {
		return 0, nil, err
	}
	var skill uint16
	byID := map[string]buffSource{}
	if state != nil {
		sources, err := buffSources(c, in, now)
		if err != nil {
			return 0, nil, err
		}
		for _, row := range sources {
			byID[row.ID] = row
		}
		if slices.ContainsFunc(p.Skills, func(r questSkill) bool { return r.Id == state.Skill && r.Level > 0 }) {
			skill = state.Skill
		}
	}
	b := []byte{38}
	for part := byte(0); part < 38; part++ {
		if state != nil {
			row, ok := byID[wireGUID(state.Items[part])]
			if ok && row.Part == part {
				b = little.AppendUint16(append(b, 1, part, row.List), row.Slot)
				continue
			}
		}
		b = little.AppendUint16(append(b, 0, part, 49), math.MaxUint16)
	}
	return skill, b, nil
}
