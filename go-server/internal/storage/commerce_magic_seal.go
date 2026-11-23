package storage

import (
	"encoding/binary"
	"encoding/json"
	"math"
	"slices"
)

type MagicSealDefinition struct {
	TemplateID   uint32 `json:"TemplateId"`
	ResourceHash string
	GoldCost     uint32
	Tiers        []magicSealTier
	Counts       []magicSealWeight
	QualityBands []magicSealBand
}

type magicSealWeight struct{ Value, Weight int }
type magicSealTier struct {
	Type, Weight, MinimumCount, MaximumCount, MinimumQuality, MaximumQuality int
	Groups                                                                   []magicSealGroup
}
type magicSealGroup struct {
	Weight  int
	Options []magicSealWeight
}
type magicSealBand struct {
	Minimum, Maximum float64
	Weight           int
}

type MagicSealWeight = magicSealWeight
type MagicSealTier = magicSealTier
type MagicSealGroup = magicSealGroup
type MagicSealBand = magicSealBand
type magicSealChoice struct {
	ID                      byte `json:"Id"`
	FirstRatio, SecondRatio byte
}
type magicSealResult struct {
	First, Second, Third *magicSealChoice
	Seed                 byte
	GoldCost             uint32
	ResourceHash         string
	PolicyVersion        int
}

func (x *CommerceTx) UnsealEquipment(slot uint16, catalog map[uint32]MagicSealDefinition) error {
	i := x.bagSource(slot)
	if i < 0 {
		return ErrInvalid
	}
	a := x.Inventory.Items[i]
	t, err := moveTemplate(&a)
	if err != nil || a.CharacterID != x.Character.ID || t.Kind == 2 || a.Count != 1 ||
		slot > t.bagEnd(x.Inventory.BagExpansion) || !magicSealSource(a) {
		return ErrInvalid
	}
	d, ok := catalog[a.TemplateID]
	if !ok || d.GoldCost == 0 || d.ResourceHash != t.ResourceHash || d.TemplateID != a.TemplateID {
		return ErrInvalid
	}
	if int64(d.GoldCost) > x.Inventory.Gold {
		return ErrFunds
	}
	result, err := rollMagicSeal(d)
	if err != nil {
		return err
	}
	var attrs map[string]json.RawMessage
	if json.Unmarshal(a.Attributes, &attrs) != nil || attrs == nil {
		return ErrInvalid
	}
	attrs["MagicSeal"], err = json.Marshal(result)
	if err != nil {
		return err
	}
	a.Attributes, err = json.Marshal(attrs)
	if err != nil {
		return err
	}
	if _, err = x.tx.ExecContext(x.ctx, "UPDATE s9a1.assets SET attributes_json=$2::json WHERE instance_id=$1", a.UID, string(a.Attributes)); err != nil {
		return err
	}
	x.Inventory.Gold -= int64(d.GoldCost)
	x.Granted = a
	return nil
}

func magicSealSource(a Asset) bool {
	var v materialAttributes
	var d materialDefinition
	if len(a.Core) != 119 || len(a.Detail) != 0 || json.Unmarshal(a.Attributes, &v) != nil || v.MagicSeal != nil ||
		v.Locked || v.InitialAmplification || v.Reinforcement != 0 || v.Forge != 0 || v.AmplificationType != 0 || v.AmplificationValue != 0 ||
		v.Enchantment != 0 || v.EnchantUpgradeCount != 0 || v.Mythology != nil || json.Unmarshal(a.Template, &d) != nil {
		return false
	}
	var binding byte
	if d.InitialSealed {
		binding = 1
	}
	if v.Binding != binding || a.Core[13] != binding {
		return false
	}
	gift := d.CashPackage || d.CashPackageReward || d.PvfGiftItem
	var expiry int64
	if v.ExpiresAt != nil {
		expiry = *v.ExpiresAt
	}
	if gift {
		if d.ResourceExpiration == nil && !d.PvfGiftItem || d.ResourceExpiration != nil && (*d.ResourceExpiration <= 0 || *d.ResourceExpiration > math.MaxInt32) {
			return false
		}
		timed := d.RelativeLifetimeSeconds != 0 || d.DailyDeletion || d.WeeklyDeletionDay != nil || d.FatigueTimeMonthlyDeletion
		if timed {
			if expiry <= 0 || expiry > math.MaxInt32 || d.ResourceExpiration != nil && expiry > *d.ResourceExpiration {
				return false
			}
		} else if !sameDeadline(v.ExpiresAt, d.ResourceExpiration) {
			return false
		}
		if int64(int32(binary.LittleEndian.Uint32(a.Core[56:]))) != expiry {
			return false
		}
	} else if v.ExpiresAt != nil {
		return false
	}
	for k, n := range a.Core[10:] {
		i := k + 10
		if n != 0 && i != 11 && i != 12 && !(gift && (i == 13 || i >= 56 && i <= 59)) {
			return false
		}
	}
	return true
}

func rollMagicSeal(d MagicSealDefinition) (magicSealResult, error) {
	var result magicSealResult
	t, err := pickSeal(d.Tiers, func(v magicSealTier) int { return v.Weight })
	if err != nil {
		return result, err
	}
	if t.MinimumCount < 1 || t.MaximumCount > 3 || t.MinimumCount > t.MaximumCount || t.MinimumQuality < 0 ||
		t.MaximumQuality > 100 || t.MinimumQuality > t.MaximumQuality {
		return result, ErrInvalid
	}
	counts := slices.DeleteFunc(slices.Clone(d.Counts), func(c magicSealWeight) bool { return c.Value < t.MinimumCount || c.Value > t.MaximumCount })
	count, err := pickSeal(counts, func(v magicSealWeight) int { return v.Weight })
	if err != nil {
		return result, err
	}
	var choices [3]*magicSealChoice
	var seen [256]bool
	quality := func() (byte, error) {
		b, err := pickSeal(d.QualityBands, func(v magicSealBand) int { return v.Weight })
		if err != nil {
			return 0, err
		}
		if b.Minimum < 0 || b.Maximum > 1 || b.Minimum > b.Maximum || math.IsNaN(b.Minimum) || math.IsNaN(b.Maximum) {
			return 0, ErrInvalid
		}
		r, err := giftRandom(10001)
		if err != nil {
			return 0, err
		}
		ratio := b.Minimum + (b.Maximum-b.Minimum)*float64(r)/10000
		q := int(math.Round(float64(t.MinimumQuality) + float64(t.MaximumQuality-t.MinimumQuality)*ratio))
		return byte(max(t.MinimumQuality, min(t.MaximumQuality, q))), nil
	}
	for i := 0; i < count.Value; i++ {
		var groups []magicSealGroup
		for _, g := range t.Groups {
			options := slices.DeleteFunc(slices.Clone(g.Options), func(o magicSealWeight) bool {
				return o.Value > 0 && o.Value <= 255 && seen[o.Value]
			})
			if slices.ContainsFunc(options, func(o magicSealWeight) bool { return o.Weight > 0 }) {
				g.Options = options
				groups = append(groups, g)
			}
		}
		group, err := pickSeal(groups, func(g magicSealGroup) int { return g.Weight })
		if err != nil {
			return result, err
		}
		option, err := pickSeal(group.Options, func(o magicSealWeight) int { return o.Weight })
		if err != nil || option.Value < 1 || option.Value > 255 {
			return result, ErrInvalid
		}
		seen[option.Value] = true
		a, err := quality()
		if err != nil {
			return result, err
		}
		b, err := quality()
		if err != nil {
			return result, err
		}
		choices[i] = &magicSealChoice{byte(option.Value), a, b}
	}
	seed, err := giftRandom(256)
	if err != nil {
		return result, err
	}
	return magicSealResult{choices[0], choices[1], choices[2], byte(seed), d.GoldCost, d.ResourceHash, 1}, nil
}

func pickSeal[T any](rows []T, weight func(T) int) (T, error) {
	var zero T
	var total uint64
	for _, r := range rows {
		n := weight(r)
		if n < 0 || uint64(n) > math.MaxInt32-total {
			return zero, ErrInvalid
		}
		total += uint64(n)
	}
	p, err := giftRandom(total)
	if err != nil {
		return zero, err
	}
	for _, r := range rows {
		n := uint64(weight(r))
		if p < n {
			return r, nil
		}
		p -= n
	}
	return zero, ErrInvalid
}
