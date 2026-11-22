package storage

import (
	"encoding/json"
	"math"
	"slices"
	"time"
)

type UniqueAvatarRequest struct {
	Mode, Material, First, Second, Target int32
	Product                               uint32
	Ability, Season                       int32
}

type UniqueAvatarResult struct {
	Upgraded       bool
	Product, Value uint32
}

type uniqueAvatarReward struct {
	Template      json.RawMessage
	Count, Weight uint32
}

type uniqueAvatarProduct struct {
	Season  int32
	Product avatarCompoundItem
	IsClone bool
	Targets []uint32
}

type uniqueAvatarRule struct {
	Material                    json.RawMessage
	UpgradeCount, CompoundCount uint32
	Gauge                       uint16
	Rates                       []int
	MysticFail, AvatarFail      []uniqueAvatarReward
	Products                    []uniqueAvatarProduct
}

func (x *CommerceTx) UniqueAvatar(q UniqueAvatarRequest, c AvatarCompoundCatalog, now time.Time) (UniqueAvatarResult, error) {
	var out UniqueAvatarResult
	d, ok := c.Unique[x.Character.Job]
	if !ok || !x.livingEmblemUser(now) || x.Character.Job > 15 || len(d.Rates) != 4 || d.Gauge == 0 || d.Gauge > 1000 ||
		x.Inventory.UniqueAvatarGauge > d.Gauge || q.Mode < 0 || q.Mode > 2 || q.Ability < 0 || q.Ability >= 255 {
		return out, ErrInvalid
	}
	state, items, _, err := x.loadSpecial()
	if err != nil {
		return out, err
	}
	removed := map[int]bool{}
	var primary editableAvatar
	var primaryRule avatarCompoundInputRule
	var primaryAt int
	var secondaryID uint32
	cloned := false
	find := func(slot int32, grade byte) (int, editableAvatar, avatarCompoundInputRule, error) {
		if slot < 0 || slot >= 480 {
			return 0, editableAvatar{}, avatarCompoundInputRule{}, ErrInvalid
		}
		i, a, r, err := x.ownedCompoundAvatar(items, uint16(slot), 0, c.Inputs)
		if err != nil {
			return i, a, r, err
		}
		if a.Template.WornSlot > 7 || r.Grade != grade {
			return i, a, r, ErrInvalid
		}
		return i, a, r, nil
	}
	if q.Mode == 2 {
		primaryAt, primary, primaryRule, err = find(q.First, 3)
		if err != nil {
			return out, err
		}
		second, b, r, err := find(q.Second, 3)
		if err != nil {
			return out, err
		}
		if primary.InstanceID == b.InstanceID || primary.Template.WornSlot != b.Template.WornSlot {
			return out, ErrInvalid
		}
		removed[primaryAt], removed[second] = true, true
		secondaryID = b.Template.TemplateID
		cloned = primaryRule.Clone || r.Clone
	} else {
		primaryAt, primary, primaryRule, err = find(q.Target, 3)
		if err != nil {
			return out, err
		}
		cloned = primaryRule.Clone
		if q.Mode == 1 {
			if q.Material != -1 {
				return out, ErrInvalid
			}
			first, a, _, err := find(q.First, 2)
			if err != nil {
				return out, err
			}
			second, b, _, err := find(q.Second, 2)
			if err != nil {
				return out, err
			}
			if a.InstanceID == b.InstanceID || a.InstanceID == primary.InstanceID || b.InstanceID == primary.InstanceID {
				return out, ErrInvalid
			}
			removed[first], removed[second] = true, true
		}
	}
	p, err := uniqueAvatarChoice(d.Products, q.Product, q.Season)
	if err != nil {
		return out, err
	}
	var t specialPurchaseTemplate
	if json.Unmarshal(p.Product.Template, &t) != nil || t.Kind != 0 || p.IsClone != cloned || t.WornSlot != primary.Template.WornSlot ||
		p.Product.Abilities[byte(q.Ability)]&(1<<x.Character.Job) == 0 || t.UsableJobs&(1<<x.Character.Job) == 0 ||
		len(p.Targets) != 0 && (!slices.Contains(p.Targets, primary.Template.TemplateID) || secondaryID != 0 && !slices.Contains(p.Targets, secondaryID)) {
		return out, ErrInvalid
	}
	if q.Mode != 1 {
		if q.Material < 0 || q.Material > 479 {
			return out, ErrInvalid
		}
		i, _, err := x.emblemBag(uint16(q.Material), now)
		if err != nil {
			return out, err
		}
		count := d.UpgradeCount
		if q.Mode == 2 {
			count = d.CompoundCount
		}
		if err = x.eligibleAvatarCatalyst(i, d.Material, now); err != nil {
			return out, err
		}
		if err = x.debitAsset(i, count); err != nil {
			return out, err
		}
	}
	atFull := x.Inventory.UniqueAvatarGauge == d.Gauge
	rate := 100
	if q.Mode != 2 {
		i := q.Mode * 2
		if atFull {
			i++
		}
		rate = d.Rates[i]
		if rate < 0 || rate > 100 {
			return out, ErrInvalid
		}
	}
	roll, err := giftRandom(10000)
	if err != nil {
		return out, err
	}
	var grants []avatarCompoundItem
	var preserve *editableAvatar
	if roll < uint64(rate)*100 {
		out = UniqueAvatarResult{Upgraded: true, Product: q.Product, Value: uint32(q.Ability)}
		grants = []avatarCompoundItem{p.Product}
		if q.Mode != 2 {
			removed[primaryAt] = true
			preserve = &primary
		}
	} else {
		pool := d.MysticFail
		if q.Mode == 1 {
			pool = d.AvatarFail
		}
		reward, err := randomUniqueAvatarReward(pool)
		if err != nil {
			return out, err
		}
		var rt ordinaryTemplate
		if json.Unmarshal(reward.Template, &rt) != nil {
			return out, ErrInvalid
		}
		if err = x.grantCommerceItem(reward.Template, reward.Count, now, false); err != nil {
			return out, err
		}
		out = UniqueAvatarResult{Product: rt.TemplateID, Value: reward.Count}
	}
	if _, err = x.materializeCompoundedAvatars(state, items, removed, grants, uint16(q.Ability), now, preserve); err != nil {
		return out, err
	}
	if q.Mode != 2 {
		if out.Upgraded || atFull {
			x.Inventory.UniqueAvatarGauge = 0
		} else {
			x.Inventory.UniqueAvatarGauge++
		}
	}
	return out, nil
}

func uniqueAvatarChoice(products []uniqueAvatarProduct, id uint32, season int32) (uniqueAvatarProduct, error) {
	for _, p := range products {
		var t specialPurchaseTemplate
		if p.Season == season && json.Unmarshal(p.Product.Template, &t) == nil && t.TemplateID == id {
			return p, nil
		}
	}
	return uniqueAvatarProduct{}, ErrInvalid
}

func randomUniqueAvatarReward(pool []uniqueAvatarReward) (uniqueAvatarReward, error) {
	var total uint64
	for _, r := range pool {
		total += uint64(r.Weight)
	}
	if total == 0 || total > math.MaxInt32 {
		return uniqueAvatarReward{}, ErrInvalid
	}
	pick, err := giftRandom(total)
	if err != nil {
		return uniqueAvatarReward{}, err
	}
	for _, r := range pool {
		if pick < uint64(r.Weight) {
			return r, nil
		}
		pick -= uint64(r.Weight)
	}
	return uniqueAvatarReward{}, ErrInvalid
}
