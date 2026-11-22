package storage

import (
	"encoding/json"
	"math"
	"slices"
	"time"
)

type giftRange struct {
	location    byte
	first, last uint16
	owner       uint16
}

type giftCapacitySample struct {
	row      Asset
	range_   giftRange
	template ordinaryTemplate
	count    uint64
}

func (x *CommerceTx) checkGiftPools(d GiftDefinition, pools []GiftPool, bonus []GiftReward, now time.Time, limits map[byte]int64, weights map[uint32]int64) error {
	var gold, coins, weight, specialTotal uint64
	var specialUpper [3]uint64
	ranges := map[giftRange]uint64{}
	samples := map[string]giftCapacitySample{}
	// Reserve a safe upper bound for every possible draw before selecting any
	// outcome. A full destination can never be used to reroll a paid box.
	accumulate := func(rewards []GiftReward, n uint64) error {
		blocks := map[giftRange]uint64{}
		values := map[string]uint64{}
		var maxGold, maxCoins, maxWeight, maxSpecial uint64
		var specials [3]uint64
		for _, r := range rewards {
			if r.UnavailableReason != nil || r.Count == 0 || r.TemplateID == math.MaxUint32 {
				return ErrInvalid
			}
			switch {
			case r.Wallet != nil:
				w := r.Wallet
				if d.SetDisjointInstanceType || d.SetBanTransportStone || d.SetEpicExchangeInstanceType ||
					w.Cera != 0 || !(r.TemplateID == 0 && w.Gold == int64(r.Count) && w.Coin == 0 || r.TemplateID == 1 && w.Coin == int64(r.Count) && w.Gold == 0) {
					return ErrInvalid
				}
				maxGold, maxCoins = max(maxGold, uint64(w.Gold)), max(maxCoins, uint64(w.Coin))
			case r.SpecialProduct != nil:
				p := r.SpecialProduct
				var t specialPurchaseTemplate
				var lifetime materialDefinition
				if json.Unmarshal(p.Template, &t) != nil || json.Unmarshal(p.Template, &lifetime) != nil || t.TemplateID != r.TemplateID || t.Kind > 2 ||
					p.ResourcePeriod != 0 && t.RelativeLifetimeSeconds == 0 || d.SetDisjointInstanceType || d.SetBanTransportStone || d.SetEpicExchangeInstanceType ||
					t.MinimumLevel == 0 || t.UsableJobs == 0 || t.Kind == 1 && t.WornSlot != 26 || t.Kind == 2 && (t.WornSlot < 27 || t.WornSlot > 29) {
					return ErrInvalid
				}
				if t.Kind == 0 {
					if t.WornSlot > 11 {
						return ErrInvalid
					}
					if _, err := giftAbility(r, p, nil, t.UsableJobs); err != nil {
						return err
					}
					if _, err := initialSpecialSockets(t); err != nil {
						return err
					}
				}
				if _, _, err := commerceItemCore(ordinaryTemplate{Kind: 2}, lifetime, now); err != nil {
					return err
				}
				specials[t.Kind] = max(specials[t.Kind], uint64(r.Count))
				maxSpecial = max(maxSpecial, uint64(r.Count))
			default:
				s, err := x.giftSample(d, r, now)
				if err != nil {
					return err
				}
				key := string(s.row.Template)
				if _, ok := samples[key]; !ok {
					samples[key] = s
				}
				values[key] = max(values[key], uint64(r.Count))
				if s.row.Source != "shared" {
					blocks[s.range_] = max(blocks[s.range_], ceilGift(uint64(r.Count), uint64(s.template.StackLimit)))
					if s.range_.location == 0 {
						unit, ok := weights[r.TemplateID]
						if !ok || unit < 0 || unit > math.MaxInt32 {
							return ErrInvalid
						}
						maxWeight = max(maxWeight, uint64(unit)*uint64(r.Count))
					}
				}
			}
		}
		gold += n * maxGold
		coins += n * maxCoins
		specialTotal += n * maxSpecial
		if maxWeight > (math.MaxUint64-weight)/n {
			return ErrCapacity
		}
		weight += n * maxWeight
		for loc, v := range blocks {
			ranges[loc] += n * v
		}
		for idx, v := range specials {
			specialUpper[idx] += n * v
		}
		for key, v := range values {
			s := samples[key]
			s.count += n * v
			samples[key] = s
		}
		return nil
	}
	draws := len(bonus)
	for _, p := range pools {
		draws += p.DrawCount
		if p.UnavailableReason != nil || p.DrawCount < 1 || p.DrawCount > 255 || draws > 4096 ||
			len(p.Entries) == 0 || len(p.Entries) > 32768 {
			return ErrInvalid
		}
		var total uint64
		var choices []GiftReward
		for _, e := range p.Entries {
			if e.Weight == 0 {
				continue
			}
			if e.Weight > math.MaxInt64-total {
				return ErrInvalid
			}
			total += e.Weight
			choices = append(choices, e.Reward)
		}
		if total == 0 {
			return ErrInvalid
		}
		if err := accumulate(choices, uint64(p.DrawCount)); err != nil {
			return err
		}
	}
	for _, r := range bonus {
		if err := accumulate([]GiftReward{r}, 1); err != nil {
			return err
		}
	}
	if gold > uint64(math.MaxUint32-x.Inventory.Gold) || coins > uint64(math.MaxUint32-x.Inventory.Coins) {
		return ErrCapacity
	}
	if gold != 0 {
		limit, err := MailGoldLimit(x.Character, limits)
		if err != nil {
			return err
		}
		if uint64(x.Inventory.Gold)+gold > uint64(limit) {
			return ErrCapacity
		}
	}
	if weight != 0 {
		if err := x.giftWeight(weight, weights); err != nil {
			return err
		}
	}
	if specialTotal != 0 {
		if err := x.reserveSpecial(specialUpper, specialTotal); err != nil {
			return err
		}
	}
	occupied := map[[3]uint16]bool{}
	for _, a := range x.Inventory.Items {
		if a.Count != 0 {
			occupied[[3]uint16{uint16(a.Location), a.Slot, a.CharacterID}] = true
		}
	}
	distinct := map[giftRange]uint64{}
	sharedSeen := map[uint16]bool{}
	for _, s := range samples {
		var room uint64
		if s.row.Source == "shared" {
			if sharedSeen[s.range_.first] {
				return ErrCapacity
			}
			sharedSeen[s.range_.first] = true
		}
		for _, row := range x.Inventory.Items {
			if row.Count == 0 || row.Location != s.row.Location || row.CharacterID != s.row.CharacterID ||
				row.TemplateID != s.row.TemplateID || row.Slot > s.range_.last {
				continue
			}
			compatible := stacks(&row, &s.row)
			if s.row.Source == "shared" && row.Slot == s.range_.first && !compatible {
				return ErrCapacity
			}
			if compatible && row.Count < s.template.StackLimit {
				room += uint64(s.template.StackLimit - row.Count)
			}
		}
		need := ceilGift(s.count-min(s.count, room), uint64(s.template.StackLimit))
		if s.row.Source == "shared" {
			if need > 1 || need > 0 && occupied[[3]uint16{0, s.range_.first, 0}] {
				return ErrCapacity
			}
			continue
		}
		distinct[s.range_] += need
	}
	keys := make([]giftRange, 0, len(ranges))
	for r := range ranges {
		keys = append(keys, r)
	}
	slices.SortFunc(keys, func(a, b giftRange) int {
		if a.last != b.last {
			return int(a.last) - int(b.last)
		}
		return int(a.first) - int(b.first)
	})
	for _, r := range keys {
		need := min(ranges[r], distinct[r])
		take := func(slot uint16) {
			k := [3]uint16{uint16(r.location), slot, r.owner}
			if need > 0 && !occupied[k] {
				occupied[k] = true
				need--
			}
		}
		if r.location == 0 && r.first == 65 && r.last <= 120 {
			for slot := uint16(3); slot <= 8; slot++ {
				take(slot)
			}
		}
		for slot := r.first; slot <= r.last && need > 0; slot++ {
			take(slot)
		}
		if need != 0 {
			return ErrCapacity
		}
	}
	return nil
}

func ceilGift(n, limit uint64) uint64 {
	return (n + limit - 1) / limit
}

func (x *CommerceTx) giftSample(d GiftDefinition, r GiftReward, now time.Time) (giftCapacitySample, error) {
	var s giftCapacitySample
	var t ordinaryTemplate
	var p materialDefinition
	if json.Unmarshal(r.Template, &t) != nil || json.Unmarshal(r.Template, &p) != nil ||
		t.TemplateID != r.TemplateID || t.Kind < 0 || t.Kind > 2 || t.StackLimit == 0 || t.FateCollection ||
		t.Kind != 2 && (r.Count > 56 || t.StackLimit != 1) || t.Kind == 2 && (d.SetDisjointInstanceType || d.SetBanTransportStone) ||
		d.SetEpicExchangeInstanceType {
		return s, ErrInvalid
	}
	// Quality does not affect stackable capacity. A fixed sample preserves
	// lifetime/binding without consuming a random draw before capacity is known.
	attr, core, err := commerceItemCore(ordinaryTemplate{Kind: 2, MaximumDurability: t.MaximumDurability}, p, now)
	if err != nil {
		return s, err
	}
	a := Asset{TemplateID: t.TemplateID, Template: r.Template, Attributes: attr, Core: core, Source: "item", CharacterID: x.Character.ID}
	dest := giftRange{location: 0, first: t.FirstBagSlot, last: t.bagEnd(x.Inventory.BagExpansion), owner: x.Character.ID}
	switch {
	case t.AccountCommonInventory:
		if t.Kind != 2 {
			return s, ErrInvalid
		}
		a.Location, a.CharacterID = 45, 0
		dest = giftRange{location: 45, first: 0, last: 49}
		t.StackLimit = min(t.StackLimit, math.MaxInt32)
	case t.PetConsumable:
		a.Location = 7
		dest = giftRange{location: 7, first: 216, last: 271, owner: x.Character.ID}
	case t.Kind == 1 && t.WornSlotIsNative && t.WornSlot == 32:
		a.Location = 38
		dest = giftRange{location: 38, first: 0, last: 55, owner: x.Character.ID}
	default:
		if slot, ok := SharedMaterialSlot(t.TemplateID); ok {
			plain, err := plainMaterial(a, p)
			if err != nil {
				return s, err
			}
			if !plain {
				return s, ErrInvalid
			}
			a.Source, a.CharacterID = "shared", 0
			dest = giftRange{first: slot, last: slot}
		} else if dest.first < 3 || dest.last < dest.first || dest.last > 360 {
			return s, ErrInvalid
		}
	}
	return giftCapacitySample{row: a, range_: dest, template: t}, nil
}

func (x *CommerceTx) giftWeight(extra uint64, weights map[uint32]int64) error {
	weight := func(in Inventory) (uint64, error) {
		var w uint64
		for _, row := range in.Items {
			if row.Location != 0 || row.CharacterID != x.Character.ID || row.Count == 0 {
				continue
			}
			unit, ok := weights[row.TemplateID]
			if !ok || unit < 0 || unit > math.MaxInt32 {
				return 0, ErrInvalid
			}
			n := uint64(unit) * uint64(row.Count)
			if math.MaxUint64-w < n {
				return 0, ErrCapacity
			}
			w += n
		}
		return w, nil
	}
	was, err := weight(x.before)
	if err != nil {
		return err
	}
	left, err := weight(x.Inventory)
	if err != nil {
		return err
	}
	var stats struct{ Values map[string]int64 }
	if err = json.Unmarshal(x.Character.Stats, &stats); err != nil {
		return err
	}
	base := stats.Values["inventory_limit"]
	if base < 0 {
		return ErrInvalid
	}
	capacity := uint64(base) + uint64(x.Inventory.BagExpansion/8)*10000
	allowed := max(was, capacity)
	if left > allowed || extra > allowed-left {
		return ErrCapacity
	}
	return nil
}

func (x *CommerceTx) reserveSpecial(need [3]uint64, total uint64) error {
	_, items, exp, err := x.loadSpecial()
	if err != nil {
		return err
	}
	if total > 255 || uint64(len(items))+total > 585 {
		return ErrCapacity
	}
	capacity := [3]uint64{120 + uint64(exp), 160, 56}
	for _, raw := range items {
		var item struct {
			Location byte
			Template struct{ Kind byte }
		}
		if err = json.Unmarshal(raw, &item); err != nil {
			return err
		}
		kind := item.Template.Kind
		if kind > 2 {
			return ErrInvalid
		}
		if item.Location == 1 && kind == 0 || item.Location == 7 && kind != 0 {
			if capacity[kind] == 0 {
				return ErrCapacity
			}
			capacity[kind]--
		}
	}
	for k, v := range need {
		if v > capacity[k] {
			return ErrCapacity
		}
	}
	return nil
}
