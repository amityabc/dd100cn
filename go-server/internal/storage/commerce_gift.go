package storage

import (
	"crypto/rand"
	"encoding/json"
	"math"
	"math/big"
	"slices"
	"time"
)

type GiftChoice struct {
	Items     []uint32
	Abilities map[uint32]byte
}

type GiftRequest struct {
	Opcode, Slot          uint16
	Category, Subcategory byte
	Random                bool
	Choices               []GiftChoice
}

type GiftDefinition struct {
	Template                                                                   json.RawMessage
	SelectionCount                                                             int
	Categories                                                                 []GiftCategory
	SetEpicExchangeInstanceType, SetDisjointInstanceType, SetBanTransportStone bool
	RandomUse                                                                  bool
	RandomPools                                                                []GiftPool
	Materials                                                                  []CommercePayment
	UsableFrom                                                                 *int64
	MaximumLevel                                                               byte
	Usage                                                                      *GiftUsage
	RequestOpcode                                                              uint16
}

type GiftCategory struct {
	Rewards           []GiftReward
	RequiredItems     []uint32
	Index, SubIndex   byte
	UnavailableReason *string
}

type GiftReward struct {
	TemplateID        uint32 `json:"TemplateId"`
	Count             uint32
	Template          json.RawMessage
	UnavailableReason *string
	AttachType        byte
	SpecialProduct    *GiftSpecial
	FixedAbility      *byte
	Upgrades          []GiftUpgrade
	Wallet            *struct{ Gold, Coin, Cera int64 }
	BroadcastUpgrade  *byte
	Broadcast         bool
}

type GiftUpgrade struct {
	Level  byte
	Weight uint32
}

type GiftSpecial struct {
	Template       json.RawMessage
	Abilities      map[byte]uint16
	Model          uint32
	ResourcePeriod int64
	AttachType     byte
	InitialPetName string
}

type GiftPool struct {
	DrawCount                  int
	Entries                    []GiftPoolEntry
	UsableJobs                 uint16
	MinimumLevel, MaximumLevel byte
	GrowType                   *byte
	UnavailableReason          *string
}

type GiftPoolEntry struct {
	Weight uint64
	Reward GiftReward
}

type GiftCashPackage struct {
	Template json.RawMessage
	Contents []struct {
		Template json.RawMessage
		Count    uint32
	}
}

type GiftContract struct {
	PremiumType byte
	Days        uint32
}

type GiftUsage struct {
	ConsumeSource                                                bool
	CooldownMilliseconds, CooldownGroup, DailyLimit, WeeklyLimit uint32
	WeeklyResetDay                                               byte
	AccumulatedLimit                                             uint32
	AccumulatedRewards                                           []GiftAccumulatedReward
}

type GiftAccumulatedReward struct {
	Count   uint32
	Rewards []GiftReward
}

type GiftResult struct {
	TemplateID                                  uint32
	Consumed                                    uint32
	FateBox                                     bool
	Rewards                                     []GiftResultRow
	PersonalCargo, Avatar, Pet, Common, Premium bool
}

type GiftResultRow struct {
	TemplateID, CountOrSeed uint32
	AttachType              byte
}

const cargoGiftTemplate = 490709682

func (x *CommerceTx) OpenGift(q GiftRequest, c CommerceCatalog, now time.Time, limits map[byte]int64, weights map[uint32]int64) error {
	i, t, lifetime, _, err := x.giftSource(q.Slot, now)
	if err != nil {
		return err
	}
	src := x.Inventory.Items[i]
	x.gift = &GiftResult{TemplateID: src.TemplateID}
	d, ok := c.Gifts[src.TemplateID]
	if !ok {
		if box, found := c.AvatarBoxes[src.TemplateID]; found {
			return x.openAvatarGift(i, q, box, now)
		}
	}
	if src.TemplateID == cargoGiftTemplate {
		capacity := x.Inventory.PersonalCapacity
		if q.Opcode != 160 || !q.Random || !sameCommerceTemplate(c.CargoGift, src.Template) ||
			capacity < 8 || capacity >= 104 || (capacity-8)%16 != 0 {
			return ErrInvalid
		}
		if err = x.debitAsset(i, 1); err != nil {
			return err
		}
		x.Inventory.PersonalCapacity = 104
		x.gift.PersonalCargo = true
		return nil
	}
	if !ok {
		pkg, ok := c.CashPackages[src.TemplateID]
		if !ok || q.Opcode != 510 || len(q.Choices) != 0 || !lifetime.CashPackage ||
			!sameCommerceTemplate(pkg.Template, src.Template) {
			return ErrInvalid
		}
		return x.openCashPackage(i, pkg, now)
	}
	if !sameCommerceTemplate(d.Template, src.Template) || d.UsableFrom != nil && *d.UsableFrom > now.Unix() ||
		x.Character.Level > d.MaximumLevel || d.RandomUse != q.Random ||
		(d.RandomUse || d.SelectionCount >= 0) != (q.Opcode == 160) {
		return ErrInvalid
	}
	bonus, err := x.giftUse(t.TemplateID, d.Usage, now)
	if err != nil {
		return err
	}
	if d.Usage == nil || d.Usage.ConsumeSource {
		if err = x.debitAsset(i, 1); err != nil {
			return err
		}
	}
	for _, m := range d.Materials {
		if err = x.spendGiftMaterial(m, src.UID, d.Usage, now); err != nil {
			return err
		}
	}
	var rewards []GiftReward
	var abilities map[uint32]byte
	switch {
	case d.RandomUse:
		var p struct{ Progression struct{ GrowType byte } }
		if err = json.Unmarshal(x.Character.Gameplay, &p); err != nil {
			return err
		}
		pools := make([]GiftPool, 0, len(d.RandomPools))
		for _, pool := range d.RandomPools {
			if pool.UsableJobs&(1<<x.Character.Job) != 0 && pool.MinimumLevel <= x.Character.Level && x.Character.Level <= pool.MaximumLevel &&
				(pool.GrowType == nil || *pool.GrowType == p.Progression.GrowType&15) {
				pools = append(pools, pool)
			}
		}
		if len(pools) == 0 {
			return ErrInvalid
		}
		if err = x.checkGiftPools(d, pools, bonus, now, limits, weights); err != nil {
			return err
		}
		for _, pool := range pools {
			var total uint64
			for _, e := range pool.Entries {
				total += e.Weight
			}
			for range pool.DrawCount {
				pick, err := giftRandom(total)
				if err != nil {
					return err
				}
				for _, e := range pool.Entries {
					if pick < e.Weight {
						rewards = append(rewards, e.Reward)
						break
					}
					pick -= e.Weight
				}
			}
		}
	case d.SelectionCount == -1:
		if len(q.Choices) != 0 || len(d.Categories) != 1 || d.Categories[0].UnavailableReason != nil {
			return ErrInvalid
		}
		rewards = d.Categories[0].Rewards
	default:
		rewards, abilities, err = giftSelection(d, q)
		if err != nil {
			return err
		}
	}
	rewards = append(slices.Clone(rewards), bonus...)
	if len(rewards) == 0 || len(rewards) > 4096 {
		return ErrInvalid
	}
	for _, id := range abilities {
		if id == 255 {
			return ErrInvalid
		}
	}
	for item := range abilities {
		if !slices.ContainsFunc(rewards, func(r GiftReward) bool {
			if r.TemplateID != item || r.SpecialProduct == nil {
				return false
			}
			var t specialPurchaseTemplate
			return json.Unmarshal(r.SpecialProduct.Template, &t) == nil && t.Kind == 0
		}) {
			return ErrInvalid
		}
	}
	types := map[uint32]byte{}
	for _, r := range rewards {
		if typ, ok := types[r.TemplateID]; ok && typ != r.AttachType {
			return ErrInvalid
		}
		types[r.TemplateID] = r.AttachType
		if err = x.receiveGift(d, r, abilities, now, c.ContractGrants); err != nil {
			return err
		}
	}
	return nil
}

func (x *CommerceTx) giftSource(slot uint16, now time.Time) (int, ordinaryTemplate, materialDefinition, useState, error) {
	// Gift boxes share the inventory and frozen instance checks of ordinary
	// consumables, but their original owner does not require initialized HP.
	i := x.bagSource(slot)
	var t ordinaryTemplate
	var d materialDefinition
	if i < 0 {
		return i, t, d, useState{}, ErrInvalid
	}
	a := x.Inventory.Items[i]
	if a.CharacterID != x.Character.ID || json.Unmarshal(a.Template, &t) != nil || json.Unmarshal(a.Template, &d) != nil ||
		t.TemplateID != a.TemplateID || t.UsableJobs&(1<<x.Character.Job) == 0 ||
		t.MinimumLevel > x.Character.Level || t.Kind != 2 || (slot < 3 || slot > 8) && (slot < t.FirstBagSlot || slot > t.bagEnd(x.Inventory.BagExpansion)) {
		return i, t, d, useState{}, ErrInvalid
	}
	plain, err := plainMaterial(a, d)
	if err != nil {
		return i, t, d, useState{}, err
	}
	var v materialAttributes
	if !plain || json.Unmarshal(a.Attributes, &v) != nil || v.ExpiresAt != nil && *v.ExpiresAt <= now.Unix() ||
		d.ResourceExpiration != nil && *d.ResourceExpiration <= now.Unix() {
		return i, t, d, useState{}, ErrInvalid
	}
	return i, t, d, useState{}, nil
}

func (x *CommerceTx) openCashPackage(i int, pkg GiftCashPackage, now time.Time) error {
	if len(pkg.Contents) == 0 || len(pkg.Contents) > 255 {
		return ErrInvalid
	}
	if err := x.debitAsset(i, 1); err != nil {
		return err
	}
	for _, r := range pkg.Contents {
		var t materialDefinition
		if json.Unmarshal(r.Template, &t) != nil || !t.CashPackageReward || t.ResourceExpiration == nil || *t.ResourceExpiration <= now.Unix() {
			return ErrInvalid
		}
		if err := x.grantCommerceItem(r.Template, r.Count, now, true); err != nil {
			return err
		}
	}
	return nil
}

func giftSelection(d GiftDefinition, q GiftRequest) ([]GiftReward, map[uint32]byte, error) {
	idx := slices.IndexFunc(d.Categories, func(c GiftCategory) bool { return c.Index == q.Category && c.SubIndex == q.Subcategory })
	if idx < 0 {
		return nil, nil, ErrInvalid
	}
	c := d.Categories[idx]
	if c.UnavailableReason != nil {
		return nil, nil, ErrInvalid
	}
	n := d.SelectionCount
	if n == 0 {
		n = len(c.Rewards)
	}
	i := slices.IndexFunc(q.Choices, func(v GiftChoice) bool { return len(v.Items) == n })
	if n < 1 || n > 255 || i < 0 {
		return nil, nil, ErrInvalid
	}
	v := q.Choices[i]
	for _, id := range c.RequiredItems {
		if !slices.Contains(v.Items, id) {
			return nil, nil, ErrInvalid
		}
	}
	var selected []GiftReward
	for _, id := range v.Items {
		i := slices.IndexFunc(c.Rewards, func(r GiftReward) bool { return r.TemplateID == id })
		if i < 0 {
			return nil, nil, ErrInvalid
		}
		selected = append(selected, c.Rewards[i])
	}
	return selected, v.Abilities, nil
}

func (x *CommerceTx) spendGiftMaterial(m CommercePayment, source string, u *GiftUsage, now time.Time) error {
	if m.TemplateID == 0 || m.Count == 0 {
		return ErrInvalid
	}
	left := m.Count
	for i, a := range x.Inventory.Items {
		if left == 0 {
			break
		}
		if a.Location != 0 || a.TemplateID != m.TemplateID || a.Count == 0 || u != nil && !u.ConsumeSource && a.UID == source {
			continue
		}
		var t ordinaryTemplate
		attrs, err := movementAttributes(&a)
		if json.Unmarshal(a.Template, &t) != nil || err != nil {
			return ErrInvalid
		}
		if a.Slot > t.bagEnd(x.Inventory.BagExpansion) && a.Source != "shared" || attrs.Locked ||
			attrs.ExpiresAt != nil && *attrs.ExpiresAt <= now.Unix() {
			continue
		}
		use := min(left, a.Count)
		if err = x.debitAsset(i, use); err != nil {
			return err
		}
		left -= use
	}
	if left != 0 {
		return ErrFunds
	}
	return nil
}

func (x *CommerceTx) receiveGift(d GiftDefinition, r GiftReward, abilities map[uint32]byte, now time.Time, contracts map[uint32]GiftContract) error {
	if r.Count == 0 || r.UnavailableReason != nil || r.TemplateID == math.MaxUint32 {
		return ErrInvalid
	}
	row := GiftResultRow{TemplateID: r.TemplateID, CountOrSeed: r.Count, AttachType: r.AttachType}
	restricted := d.SetEpicExchangeInstanceType || d.SetDisjointInstanceType || d.SetBanTransportStone
	switch {
	case r.Wallet != nil:
		w := r.Wallet
		if restricted || w.Cera != 0 || w.Gold < 0 || w.Coin < 0 ||
			!(r.TemplateID == 0 && w.Gold == int64(r.Count) && w.Coin == 0 || r.TemplateID == 1 && w.Coin == int64(r.Count) && w.Gold == 0) {
			return ErrInvalid
		}
		x.Inventory.Gold += w.Gold
		x.Inventory.Coins += w.Coin
	case r.SpecialProduct != nil:
		if restricted {
			return ErrInvalid
		}
		p := r.SpecialProduct
		var t specialPurchaseTemplate
		if json.Unmarshal(p.Template, &t) != nil || t.TemplateID != r.TemplateID || p.ResourcePeriod != 0 && t.RelativeLifetimeSeconds == 0 || r.Count > 480 {
			return ErrInvalid
		}
		value := t.MaximumDurability
		switch t.Kind {
		case 0:
			a, err := giftAbility(r, p, abilities, t.UsableJobs)
			if err != nil {
				return err
			}
			value, x.gift.Avatar = uint16(a), true
		case 1:
			value, x.gift.Pet = 0, true
		case 2:
			x.gift.Pet = true
		}
		for range r.Count {
			if err := x.grantSpecial(p.Template, value, p.InitialPetName, now, false); err != nil {
				return err
			}
		}
		row.CountOrSeed = x.specialValue
		if t.Kind == 0 {
			row.CountOrSeed = 1
		}
	default:
		var t ordinaryTemplate
		if json.Unmarshal(r.Template, &t) != nil || t.TemplateID != r.TemplateID ||
			restricted && t.Kind == 2 || d.SetEpicExchangeInstanceType && (d.RandomUse || d.SelectionCount < 0) {
			return ErrInvalid
		}
		x.gift.Common = x.gift.Common || t.AccountCommonInventory
		if t.Kind == 2 {
			if err := x.grantCommerceItem(r.Template, r.Count, now, true); err != nil {
				return err
			}
			if credit, ok := contracts[r.TemplateID]; ok {
				if err := x.activateGiftContract(r.TemplateID, r.Count, credit, now); err != nil {
					return err
				}
			}
		} else {
			if r.Count > 56 {
				return ErrInvalid
			}
			for n := uint32(0); n < r.Count; n++ {
				level, err := giftUpgrade(r.Upgrades)
				if err != nil {
					return err
				}
				facts := giftEquipment{epicExchange: d.SetEpicExchangeInstanceType, disjointRestricted: d.SetDisjointInstanceType,
					banTransportStone: d.SetBanTransportStone, upgrade: level}
				if err = x.grantGiftItem(r.Template, 1, now, true, facts); err != nil {
					return err
				}
			}
			row.CountOrSeed = 1
		}
	}
	x.gift.Rewards = append(x.gift.Rewards, row)
	return nil
}

func giftAbility(r GiftReward, p *GiftSpecial, asked map[uint32]byte, jobs uint16) (byte, error) {
	a, ok := asked[r.TemplateID]
	if r.FixedAbility != nil {
		a, ok = *r.FixedAbility, true
	}
	if !ok && len(p.Abilities) == 1 {
		for opt := range p.Abilities {
			a, ok = opt, true
		}
	}
	if !ok || a == 255 || p.Abilities[a]&jobs == 0 {
		return 0, ErrInvalid
	}
	return a, nil
}

func giftUpgrade(table []GiftUpgrade) (byte, error) {
	if len(table) == 0 {
		return 0, nil
	}
	var seen [32]bool
	var total uint64
	for _, v := range table {
		if v.Level > 31 || seen[v.Level] {
			return 0, ErrInvalid
		}
		seen[v.Level] = true
		total += uint64(v.Weight)
	}
	p, err := giftRandom(total)
	if err != nil {
		return 0, err
	}
	for _, v := range table {
		if p < uint64(v.Weight) {
			return v.Level, nil
		}
		p -= uint64(v.Weight)
	}
	return 0, ErrInvalid
}

func giftRandom(upper uint64) (uint64, error) {
	if upper == 0 || upper > math.MaxInt64 {
		return 0, ErrInvalid
	}
	n, err := rand.Int(rand.Reader, new(big.Int).SetUint64(upper))
	if err != nil {
		return 0, err
	}
	return n.Uint64(), nil
}
