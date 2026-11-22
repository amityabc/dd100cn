package storage

import (
	"encoding/json"
	"math"
	"math/rand/v2"
	"slices"
	"time"
)

type DiscardLine struct {
	Action            uint32
	Slot              uint16
	TemplateID, Count uint32
}

type DiscardPolicy struct {
	Template     uint32
	ResourceHash string
}

type DisassemblyRecipe struct {
	Cube, Cubes, Expanded                                                            uint32
	Additional                                                                       []uint32
	LowDivisor, HighDivisor, LowChance, ExpandedDivisor, ExtraChance, ExpandedChance float64
	Level                                                                            int
}

func (x *CommerceTx) bagSource(slot uint16) int {
	return slices.IndexFunc(x.Inventory.Items, func(a Asset) bool { return a.Location == 0 && a.Slot == slot && a.Count > 0 })
}

func (x *CommerceTx) Discard(rows []DiscardLine, c CommerceCatalog, now time.Time) error {
	seen := map[uint16]bool{}
	for _, row := range rows {
		i := x.bagSource(row.Slot)
		if i < 0 || seen[row.Slot] || row.Action != 1 || row.Count == 0 || row.Count > math.MaxInt32 {
			return ErrInvalid
		}
		seen[row.Slot] = true
		a := x.Inventory.Items[i]
		t, err := moveTemplate(&a)
		if err != nil || a.CharacterID != x.Character.ID || a.TemplateID != row.TemplateID ||
			row.Count > a.Count || t.Kind != 2 && row.Count != 1 || len(a.Detail) != 0 ||
			(row.Slot < 3 || row.Slot > 8) && (row.Slot < t.FirstBagSlot || row.Slot > t.bagEnd(x.Inventory.BagExpansion)) {
			return ErrInvalid
		}
		def, ok := c.Discard[a.TemplateID]
		if !ok || def.Template != a.TemplateID || def.ResourceHash != t.ResourceHash ||
			!sameCommerceTemplate(c.Templates[a.TemplateID], a.Template) {
			return ErrInvalid
		}
		if err = x.debitEligible(i, row.Count, now); err != nil {
			return err
		}
	}
	return nil
}

func (x *CommerceTx) SpendSkillCube(row DiscardLine, template json.RawMessage, now time.Time) error {
	i := x.bagSource(row.Slot)
	if i < 0 || row.Action != 2 || row.Slot != 367 || row.TemplateID != 3037 || row.Count == 0 || row.Count > math.MaxInt32 {
		return ErrInvalid
	}
	a := x.Inventory.Items[i]
	var t materialDefinition
	if a.Source != "shared" || a.TemplateID != 3037 || !sameCommerceTemplate(template, a.Template) || json.Unmarshal(a.Template, &t) != nil {
		return ErrInvalid
	}
	ok, err := plainMaterial(a, t)
	if err != nil {
		return err
	}
	if !ok {
		return ErrInvalid
	}
	return x.debitEligible(i, row.Count, now)
}

func (x *CommerceTx) debitEligible(i int, n uint32, now time.Time) error {
	a := x.Inventory.Items[i]
	p, err := movementAttributes(&a)
	if err != nil {
		return err
	}
	if p.Locked || p.ExpiresAt != nil && *p.ExpiresAt <= now.Unix() {
		return ErrInvalid
	}
	return x.debitAsset(i, n)
}

func sameCommerceTemplate(expected, saved json.RawMessage) bool {
	if len(expected) == 0 || len(saved) == 0 || string(expected) == "null" || string(saved) == "null" {
		return false
	}
	if equalJSON(expected, saved) {
		return true
	}
	a, err := frozenMaterialObject(expected, materialTemplateDefaults)
	if err != nil {
		return false
	}
	b, err := frozenMaterialObject(saved, materialTemplateDefaults)
	if err != nil {
		return false
	}
	var t, d materialDefinition
	if json.Unmarshal(expected, &t) != nil || json.Unmarshal(saved, &d) != nil {
		return false
	}
	if t.Kind == 2 && d.Kind == 2 {
		var q, r qualification
		if json.Unmarshal(a["WeaponQualification"], &q) != nil || json.Unmarshal(b["WeaponQualification"], &r) != nil {
			return false
		}
		if q.Verified && !r.Verified && len(q.Jobs) == 0 && len(r.Jobs) == 0 {
			b["WeaponQualification"] = a["WeaponQualification"]
		}
		if sameDeletionWeekday(t, d) {
			b["WeeklyDeletionDay"] = a["WeeklyDeletionDay"]
		}
	}
	rawA, err := json.Marshal(a)
	if err != nil {
		return false
	}
	rawB, err := json.Marshal(b)
	return err == nil && equalJSON(rawA, rawB)
}

func (x *CommerceTx) Disassemble(slots []uint16, c CommerceCatalog, now time.Time) error {
	rewards := map[uint32]uint32{}
	for _, slot := range slots {
		i := x.bagSource(slot)
		if i < 0 {
			return ErrInvalid
		}
		a := x.Inventory.Items[i]
		t, err := moveTemplate(&a)
		if err != nil || a.CharacterID != x.Character.ID || a.Count != 1 || t.Kind == 2 || len(a.Core) != 119 {
			return ErrInvalid
		}
		var attr struct {
			DisjointRestricted                      bool
			Reinforcement, Forge, AmplificationType byte
			AmplificationValue                      uint16
		}
		attr.Reinforcement, attr.Forge = a.Core[10], a.Core[0x57]
		if json.Unmarshal(a.Attributes, &attr) != nil || attr.DisjointRestricted || attr.Reinforcement > 31 || attr.Forge != 0 ||
			attr.AmplificationType != 0 || attr.AmplificationValue != 0 || a.Core[10] != attr.Reinforcement ||
			a.Core[19] != 0 || a.Core[20] != 0 || a.Core[21] != 0 || a.Core[0x57] != 0 {
			return ErrInvalid
		}
		r, ok := c.Disassembly[a.TemplateID]
		if !ok {
			return ErrInvalid
		}
		if err = r.roll(rewards); err != nil {
			return err
		}
		if err = x.debitEligible(i, 1, now); err != nil {
			return err
		}
	}
	ids := make([]uint32, 0, len(rewards))
	for id := range rewards {
		ids = append(ids, id)
	}
	slices.Sort(ids)
	for _, id := range ids {
		raw := c.Templates[id]
		var t ordinaryTemplate
		if json.Unmarshal(raw, &t) != nil || t.Kind != 2 || t.TemplateID != id {
			return ErrInvalid
		}
		if err := x.grantCommerceItem(raw, rewards[id], now, true); err != nil {
			return err
		}
	}
	if len(x.Grants) == 0 || len(x.Grants) > 255 {
		return ErrInvalid
	}
	return nil
}

func (r DisassemblyRecipe) roll(rewards map[uint32]uint32) error {
	add := func(id uint32, amount uint64) error {
		if id == 0 || amount == 0 || amount > math.MaxUint32-uint64(rewards[id]) {
			return ErrInvalid
		}
		rewards[id] += uint32(amount)
		return nil
	}
	chance := func() float64 { return float64(rand.IntN(1_000_000)) / 10_000 }
	quantity := func(divisor float64) uint64 {
		if divisor <= 0 {
			return 1
		}
		n := float64(r.Level) / divisor
		whole := math.Floor(n)
		if chance()/100 < n-whole {
			whole++
		}
		if whole > math.MaxUint32 {
			return math.MaxUint32 + 1
		}
		return uint64(max(1, whole))
	}
	if err := add(r.Cube, uint64(r.Cubes)); err != nil {
		return err
	}
	if len(r.Additional) > 0 {
		divisor := r.HighDivisor
		if chance() < r.LowChance && r.LowDivisor > 0 {
			divisor = r.LowDivisor
		}
		if err := add(r.Additional[rand.IntN(len(r.Additional))], quantity(divisor)); err != nil {
			return err
		}
	}
	if r.Expanded != 0 && chance() < r.ExpandedChance {
		count := quantity(r.ExpandedDivisor)
		if chance() < r.ExtraChance {
			count++
		}
		return add(r.Expanded, count)
	}
	return nil
}
