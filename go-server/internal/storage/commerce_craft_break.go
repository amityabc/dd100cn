package storage

import (
	"encoding/json"
	"maps"
	"math"
	"slices"
	"time"
)

type CraftDestruction struct {
	CubeID       uint32 `json:"CubeId"`
	CubeCount    uint32
	SuppressSoul bool
	Fixed        map[uint32]uint32
	Row          CraftDestructionRow
}

type CraftDestructionRow struct {
	CubeMultiplier, AdditionalNormalChance         float64
	Additional                                     []uint32
	GreatDivisor, NormalDivisor                    float64
	AdditionalGreatChance                          float64
	SoulID                                         uint32 `json:"SoulId"`
	SoulDivisor, SoulGreatChance, SoulNormalChance float64
}

func craftRemodelLoss(q CraftQuote, seed uint64) (int, error) {
	if len(q.RemodelPenalties) == 0 {
		return 0, nil
	}
	if len(q.RemodelPenalties) > 8 {
		return 0, ErrInvalid
	}
	total := 0
	for _, p := range q.RemodelPenalties {
		if p.Loss < -1 || p.Loss > 31 || p.Weight < 1 || p.Weight > 100000 {
			return 0, ErrInvalid
		}
		total += p.Weight
	}
	if total != 100000 {
		return 0, ErrInvalid
	}
	roll := int(seed % uint64(total))
	for _, p := range q.RemodelPenalties {
		if roll < p.Weight {
			return p.Loss, nil
		}
		roll -= p.Weight
	}
	return 0, ErrInvalid
}

func craftMaxCount(level int, divisor float64) (uint32, error) {
	if divisor <= 0 {
		return 1, nil
	}
	v := float64(max(1, level)) / divisor
	if math.IsNaN(v) || math.IsInf(v, 0) || v >= math.MaxInt32 {
		return 0, ErrInvalid
	}
	return max(1, uint32(math.Ceil(v))), nil
}

func craftAddReturn(rows map[uint32]uint32, id, count uint32) error {
	if id == 0 || count == 0 {
		return nil
	}
	if count > math.MaxInt32-rows[id] {
		return ErrInvalid
	}
	rows[id] += count
	return nil
}

func craftBreakOptions(d CraftEquipment, recipe CraftDestruction, quote CraftQuote) ([]map[uint32]uint32, error) {
	fixed := map[uint32]uint32{}
	maps.Copy(fixed, quote.DestroyRewards)
	if recipe.Fixed != nil {
		for id, count := range recipe.Fixed {
			if err := craftAddReturn(fixed, id, count); err != nil {
				return nil, err
			}
		}
		return []map[uint32]uint32{fixed}, nil
	}
	if err := craftAddReturn(fixed, recipe.CubeID, recipe.CubeCount); err != nil {
		return nil, err
	}
	r := recipe.Row
	if !recipe.SuppressSoul && r.SoulID != 0 && r.SoulNormalChance > 0 {
		n, err := craftMaxCount(d.MinimumLevel, r.SoulDivisor)
		if err != nil {
			return nil, err
		}
		if r.SoulGreatChance > 0 {
			n++
		}
		if err = craftAddReturn(fixed, r.SoulID, n); err != nil {
			return nil, err
		}
	}
	if len(r.Additional) == 0 {
		return []map[uint32]uint32{fixed}, nil
	}
	if len(r.Additional) > 64 {
		return nil, ErrInvalid
	}
	var count uint32
	if r.AdditionalGreatChance > 0 && r.GreatDivisor > 0 {
		n, err := craftMaxCount(d.MinimumLevel, r.GreatDivisor)
		if err != nil {
			return nil, err
		}
		count = n
	}
	if r.AdditionalGreatChance < 100 || r.GreatDivisor <= 0 {
		n, err := craftMaxCount(d.MinimumLevel, r.NormalDivisor)
		if err != nil {
			return nil, err
		}
		count = max(n, count)
	}
	var out []map[uint32]uint32
	for _, id := range r.Additional {
		rows := maps.Clone(fixed)
		if err := craftAddReturn(rows, id, count); err != nil {
			return nil, err
		}
		out = append(out, rows)
	}
	return out, nil
}

func (x *CommerceTx) preflightCraftBreak(target int, d CraftEquipment, q CraftQuote, c CraftCatalog, method CraftMethod, now time.Time,
	limits map[byte]int64, weights map[uint32]int64) error {
	opts := []map[uint32]uint32{q.DestroyRewards}
	if method != CraftRemodel {
		recipe, ok := c.Destruction[d.TemplateID]
		if !ok {
			return ErrInvalid
		}
		var err error
		if opts, err = craftBreakOptions(d, recipe, q); err != nil {
			return err
		}
	}
	x.Inventory.Items[target].Count = 0
	defer func() { x.Inventory.Items[target].Count = 1 }()
	for _, option := range opts {
		if len(option) > 32 {
			return ErrInvalid
		}
		var rewards []GiftReward
		for id, count := range option {
			item := c.Templates[id]
			var t ordinaryTemplate
			if json.Unmarshal(item, &t) != nil || t.Kind != 2 || t.TemplateID != id || count == 0 {
				return ErrInvalid
			}
			rewards = append(rewards, GiftReward{TemplateID: id, Count: count, Template: item})
		}
		if err := x.checkGiftPools(GiftDefinition{}, nil, rewards, now, limits, weights); err != nil {
			return err
		}
	}
	return nil
}

func craftChance(percent float64) (bool, error) {
	if percent < 0 || percent > 100 || math.IsNaN(percent) {
		return false, ErrInvalid
	}
	roll, err := giftRandom(1000000)
	return float64(roll)/10000 < percent, err
}

func craftFractionCount(level int, divisor float64) (uint32, error) {
	if divisor <= 0 {
		return 1, nil
	}
	upper, err := craftMaxCount(level, divisor)
	if err != nil {
		return 0, err
	}
	value := float64(max(1, level)) / divisor
	whole := math.Floor(value)
	roll, err := giftRandom(1000000)
	if err != nil {
		return 0, err
	}
	if float64(roll)/1000000 < value-whole {
		return upper, nil
	}
	return max(1, uint32(whole)), nil
}

func (x *CommerceTx) grantCraftBreak(d CraftEquipment, q CraftQuote, c CraftCatalog, method CraftMethod, now time.Time) error {
	rows := map[uint32]uint32{}
	maps.Copy(rows, q.DestroyRewards)
	if method != CraftRemodel {
		var err error
		if err = craftRollBreak(rows, d.MinimumLevel, c.Destruction[d.TemplateID]); err != nil {
			return err
		}
	}
	for _, id := range slices.Sorted(maps.Keys(rows)) {
		if err := x.grantCommerceItem(c.Templates[id], rows[id], now, false); err != nil {
			return err
		}
	}
	return nil
}

func craftRollBreak(rows map[uint32]uint32, level int, d CraftDestruction) error {
	if d.Fixed != nil {
		for id, count := range d.Fixed {
			if err := craftAddReturn(rows, id, count); err != nil {
				return err
			}
		}
		return nil
	}
	if err := craftAddReturn(rows, d.CubeID, d.CubeCount); err != nil {
		return err
	}
	r := d.Row
	if len(r.Additional) > 0 {
		index, err := giftRandom(uint64(len(r.Additional)))
		if err != nil {
			return err
		}
		divisor := r.NormalDivisor
		great, err := craftChance(r.AdditionalGreatChance)
		if err != nil {
			return err
		}
		if great && r.GreatDivisor > 0 {
			divisor = r.GreatDivisor
		}
		count, err := craftFractionCount(level, divisor)
		if err != nil {
			return err
		}
		if err = craftAddReturn(rows, r.Additional[index], count); err != nil {
			return err
		}
	}
	if d.SuppressSoul || r.SoulID == 0 {
		return nil
	}
	ok, err := craftChance(r.SoulNormalChance)
	if err != nil || !ok {
		return err
	}
	count, err := craftFractionCount(level, r.SoulDivisor)
	if err != nil {
		return err
	}
	great, err := craftChance(r.SoulGreatChance)
	if err != nil {
		return err
	}
	if great {
		count++
	}
	return craftAddReturn(rows, r.SoulID, count)
}
