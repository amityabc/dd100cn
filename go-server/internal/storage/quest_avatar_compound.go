package storage

import (
	"encoding/json"
	"math"
	"slices"
	"time"
)

type AvatarCompoundInput struct {
	Slot     uint16
	Template uint32
}

type AvatarCompoundRequest struct {
	Opcode, Source    uint16
	Inputs            []AvatarCompoundInput
	Product           uint32
	Ability           uint16
	RandomStyle, Kind byte
}

type AvatarCompoundProduct struct {
	Slot            uint16
	Template        uint32
	Seconds         int64
	Ability         uint16
	Sockets, Colors []byte
}

type avatarCompoundItem struct {
	Template       json.RawMessage
	Abilities      map[byte]uint16
	ResourcePeriod int64
}

type avatarCompoundPool struct {
	BaseRate, AdvancedRate uint64
	Rare, Ordinary         []avatarCompoundItem
	Weights                []uint64
	Memory                 bool
}

type avatarCompoundInputRule struct {
	ResourceHash string
	WornSlot     uint16
	Grade        byte
	Clone        bool
}

type AvatarCompoundCatalog struct {
	Sources map[uint32]struct {
		Template json.RawMessage
		Kind     byte
	}
	Inputs                   map[uint32]avatarCompoundInputRule
	NormalPools, MemoryPools map[byte]map[uint16]avatarCompoundPool
	Absolute                 map[byte]map[uint32]struct {
		Source       json.RawMessage
		Count, Grade byte
		Products     []avatarCompoundItem
	}
	Unique map[byte]uniqueAvatarRule
}

func (x *CommerceTx) ownedCompoundAvatar(items []json.RawMessage, slot uint16, id uint32, rules map[uint32]avatarCompoundInputRule) (int, editableAvatar, avatarCompoundInputRule, error) {
	var a editableAvatar
	var attrs materialAttributes
	i, err := specialSlot(items, 1, slot)
	if err != nil {
		return i, a, avatarCompoundInputRule{}, err
	}
	if json.Unmarshal(items[i], &a) != nil || json.Unmarshal(a.Attributes, &attrs) != nil {
		return i, a, avatarCompoundInputRule{}, ErrInvalid
	}
	rule, ok := rules[a.Template.TemplateID]
	if !ok || id != 0 && id != a.Template.TemplateID || rule.ResourceHash != a.Template.ResourceHash || rule.WornSlot != a.Template.WornSlot ||
		a.Template.Kind != 0 || a.Avatar == nil || attrs.Locked || attrs.Binding != 0 || attrs.ExpiresAt != nil ||
		a.Template.ResourceExpiration != nil || a.Template.RelativeLifetimeSeconds != 0 || a.Template.InitialSealed || a.Template.DailyDeletion ||
		a.Template.WeeklyDeletionDay != nil || a.Template.UsableJobs&(1<<x.Character.Job) == 0 || !validEmblemID(a.InstanceID) {
		return i, a, rule, ErrInvalid
	}
	return i, a, rule, nil
}

func (x *CommerceTx) CompoundAvatars(q AvatarCompoundRequest, c AvatarCompoundCatalog, now time.Time) ([]AvatarCompoundProduct, error) {
	if q.Product == 0 || q.Product == math.MaxUint32 || q.Ability >= 255 || x.Character.Job > 15 || !x.livingEmblemUser(now) {
		return nil, ErrInvalid
	}
	i, tmpl, err := x.emblemBag(q.Source, now)
	if err != nil {
		return nil, err
	}
	state, items, _, err := x.loadSpecial()
	if err != nil {
		return nil, err
	}
	removed := make(map[int]bool, len(q.Inputs))
	identities := make(map[string]bool, len(q.Inputs))
	var grant []avatarCompoundItem
	var part uint16
	advanced := uint64(0)
	switch q.Opcode {
	case 99:
		src, ok := c.Sources[tmpl.TemplateID]
		if !ok || src.Kind != q.Kind || len(q.Inputs) != 2 || (q.Product == 1) != (q.RandomStyle == 1) {
			return nil, ErrInvalid
		}
		if err = x.eligibleAvatarCatalyst(i, src.Template, now); err != nil {
			return nil, err
		}
		for _, input := range q.Inputs {
			if input.Template == 0 || input.Template == math.MaxUint32 {
				return nil, ErrInvalid
			}
			pos, a, d, err := x.ownedCompoundAvatar(items, input.Slot, input.Template, c.Inputs)
			if err != nil {
				return nil, err
			}
			if d.Grade != 1 && d.Grade != 2 || identities[a.InstanceID] || len(removed) > 0 && part != a.Template.WornSlot {
				return nil, ErrInvalid
			}
			identities[a.InstanceID] = true
			removed[pos] = true
			part = a.Template.WornSlot
			if d.Grade == 2 {
				advanced++
			}
		}
		pools := c.NormalPools
		if tmpl.TemplateID == 2683630 {
			pools = c.MemoryPools
		}
		p, ok := pools[x.Character.Job][part]
		if !ok || len(p.Ordinary) == 0 {
			return nil, ErrInvalid
		}
		ordinary, err := selectedAvatarStyle(p.Ordinary, q.Product)
		if err != nil {
			return nil, err
		}
		grant = append(grant, ordinary)
		rare, err := weightedRare(p, q.Kind, advanced)
		if err != nil {
			return nil, err
		}
		if rare >= 0 {
			grant = append(grant, p.Rare[rare])
		}
	case 1113:
		d, ok := c.Absolute[x.Character.Job][tmpl.TemplateID]
		if !ok || len(q.Inputs) != int(d.Count) || (d.Count != 4 && d.Count != 8) || (d.Grade != 1 && d.Grade != 2) {
			return nil, ErrInvalid
		}
		if err = x.eligibleAvatarCatalyst(i, d.Source, now); err != nil {
			return nil, err
		}
		for _, input := range q.Inputs {
			pos, a, r, err := x.ownedCompoundAvatar(items, input.Slot, input.Template, c.Inputs)
			if err != nil {
				return nil, err
			}
			if r.Grade != d.Grade || a.Template.WornSlot > 7 || identities[a.InstanceID] {
				return nil, ErrInvalid
			}
			identities[a.InstanceID] = true
			removed[pos] = true
		}
		if q.Product == 1 {
			return nil, ErrInvalid
		}
		p, err := selectedAvatarStyle(d.Products, q.Product)
		if err != nil {
			return nil, err
		}
		grant = append(grant, p)
	default:
		return nil, ErrInvalid
	}
	for _, item := range grant {
		var out specialPurchaseTemplate
		var life materialDefinition
		if json.Unmarshal(item.Template, &out) != nil || json.Unmarshal(item.Template, &life) != nil || out.Kind != 0 ||
			out.UsableJobs&(1<<x.Character.Job) == 0 || item.Abilities[byte(q.Ability)]&(1<<x.Character.Job) == 0 ||
			q.Opcode == 99 && out.WornSlot != part ||
			q.Opcode == 1113 && (out.WornSlot > 7 || item.ResourcePeriod != 0 || out.RelativeLifetimeSeconds != 0 || life.ResourceExpiration != nil) {
			return nil, ErrInvalid
		}
	}
	if err = x.debitAsset(i, 1); err != nil {
		return nil, err
	}
	return x.materializeCompoundedAvatars(state, items, removed, grant, q.Ability, now, nil)
}

func selectedAvatarStyle(items []avatarCompoundItem, id uint32) (avatarCompoundItem, error) {
	if id == 1 && len(items) > 0 {
		roll, err := giftRandom(100000)
		if err != nil {
			return avatarCompoundItem{}, err
		}
		return items[roll*uint64(len(items))/100000], nil
	}
	for _, item := range items {
		var t specialPurchaseTemplate
		if json.Unmarshal(item.Template, &t) == nil && t.TemplateID == id {
			return item, nil
		}
	}
	return avatarCompoundItem{}, ErrInvalid
}

func weightedRare(p avatarCompoundPool, kind byte, advanced uint64) (int, error) {
	if advanced > 2 || p.BaseRate > 10000 || p.AdvancedRate > 10000 || len(p.Rare) != len(p.Weights) {
		return -1, ErrInvalid
	}
	if p.Memory {
		if kind != 0 || len(p.Weights) < 1 || len(p.Weights) > 16 {
			return -1, ErrInvalid
		}
		var sum uint64
		for _, weight := range p.Weights {
			if weight == 0 || weight > 1000 {
				return -1, ErrInvalid
			}
			sum += weight
		}
		if sum != 1000 {
			return -1, ErrInvalid
		}
	} else if !slices.Equal(slices.Sorted(slices.Values(p.Weights)), []uint64{200, 300, 500}) || kind != 0 && kind != 1 && kind != 5 {
		return -1, ErrInvalid
	}
	rate := p.BaseRate + advanced*p.AdvancedRate
	if kind == 1 {
		rate += 200
	}
	roll, err := giftRandom(100000)
	if err != nil {
		return -1, err
	}
	total, best := uint64(0), -1
	for i, w := range p.Weights {
		total += rate * w / 100
		if kind == 5 {
			total += map[uint64]uint64{200: 2200, 300: 2800, 500: 4000}[w]
		}
		if best < 0 && roll < total {
			best = i
		}
	}
	if total > 100000 {
		return -1, ErrInvalid
	}
	return best, nil
}

func (x *CommerceTx) materializeCompoundedAvatars(state map[string]json.RawMessage, items []json.RawMessage, removed map[int]bool,
	grants []avatarCompoundItem, ability uint16, now time.Time, preserve *editableAvatar) ([]AvatarCompoundProduct, error) {
	remaining := make([]json.RawMessage, 0, len(items))
	for i, row := range items {
		if !removed[i] {
			remaining = append(remaining, row)
		}
	}
	raw, err := json.Marshal(remaining)
	if err != nil {
		return nil, err
	}
	state["Items"] = raw
	if err = replaceMailFields(&x.Character, map[string]any{"SpecialAssets": state}); err != nil {
		return nil, err
	}
	for _, p := range grants {
		if err = x.grantSpecial(p.Template, ability, "", now, false); err != nil {
			return nil, err
		}
	}
	state, items, _, err = x.loadSpecial()
	if err != nil {
		return nil, err
	}
	if len(items) != len(remaining)+len(grants) {
		return nil, ErrInvalid
	}
	added := items[len(remaining):]
	out := make([]AvatarCompoundProduct, 0, len(added))
	for i, row := range added {
		var a editableAvatar
		var attrs materialAttributes
		var f struct{ Slot uint16 }
		if json.Unmarshal(row, &a) != nil || json.Unmarshal(row, &f) != nil || a.Avatar == nil || json.Unmarshal(a.Attributes, &attrs) != nil {
			return nil, ErrInvalid
		}
		p := AvatarCompoundProduct{Slot: f.Slot, Template: a.Template.TemplateID, Ability: attrs.Durability, Sockets: a.Avatar.Sockets, Colors: a.Avatar.Colors}
		if attrs.ExpiresAt != nil {
			p.Seconds = max(0, min(2147483647, *attrs.ExpiresAt-now.Unix()))
		}
		if preserve != nil {
			m := map[string]json.RawMessage{}
			if json.Unmarshal(row, &m) != nil {
				return nil, ErrInvalid
			}
			detail := map[string]json.RawMessage{}
			if json.Unmarshal(m["Avatar"], &detail) != nil {
				return nil, ErrInvalid
			}
			detail["Sockets"], err = nativeByteList(preserve.Avatar.Sockets)
			if err != nil {
				return nil, err
			}
			detail["Colors"], err = nativeByteList(preserve.Avatar.Colors)
			if err != nil {
				return nil, err
			}
			detail["Option"], err = json.Marshal(preserve.Avatar.Option)
			if err != nil {
				return nil, err
			}
			m["Avatar"], err = json.Marshal(detail)
			if err != nil {
				return nil, err
			}
			added[i], err = json.Marshal(m)
			if err != nil {
				return nil, err
			}
			p.Sockets, p.Colors = preserve.Avatar.Sockets, preserve.Avatar.Colors
		}
		out = append(out, p)
	}
	if preserve != nil {
		state["Items"], err = json.Marshal(items)
		if err != nil {
			return nil, err
		}
		if err = replaceMailFields(&x.Character, map[string]any{"SpecialAssets": state}); err != nil {
			return nil, err
		}
	}
	slices.SortFunc(out, func(a, b AvatarCompoundProduct) int { return int(a.Slot) - int(b.Slot) })
	return out, nil
}
