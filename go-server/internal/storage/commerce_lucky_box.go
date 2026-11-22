package storage

import (
	"encoding/json"
	"math"
	"time"
)

const luckyScale = uint64(100000)

type LuckyBoxRule struct {
	Source                                    json.RawMessage
	Types                                     []LuckyWeight
	Grades                                    map[byte][]LuckyWeight
	Rewards                                   map[byte][]LuckyReward
	TreasureGaugeIncrements                   map[byte]uint32
	TreasureGaugeMaximum, MultiOpenMultiplier uint32
	LimitedRewards                            []LuckyReward
}

type LuckyWeight struct {
	Value  byte
	Weight uint32
}

type LuckyReward struct {
	Template            json.RawMessage
	Count, Weight, Flag uint32
}

type LuckyBoxRequest struct {
	Mode     byte
	Slot     uint16
	Template uint32
	Selected int32
}

type LuckyBoxPayout struct {
	BoxType, ResultGrade            byte
	TemplateID, Count               uint32
	LimitedTemplateID, LimitedCount uint32
}

type luckyChoice struct {
	boxType, grade byte
	weight         uint64
}

type luckyDraw struct {
	boxType, grade byte
	reward         LuckyReward
}

func checkedLuckyWeights(rows []LuckyWeight) bool {
	var keys byte
	var total uint64
	for _, row := range rows {
		if row.Value < 1 || row.Value > 4 || keys&(1<<row.Value) != 0 {
			return false
		}
		keys |= 1 << row.Value
		total += uint64(row.Weight)
	}
	return keys == 30 && total == luckyScale
}

func checkedLuckyRewards(rows []LuckyReward, source, multiplier uint32) (uint64, error) {
	var total uint64
	for _, row := range rows {
		var t ordinaryTemplate
		if json.Unmarshal(row.Template, &t) != nil || t.Kind != 2 || t.TemplateID == source || t.TemplateID == 0 ||
			t.TemplateID > math.MaxInt32 || row.Count == 0 || uint64(row.Count)*uint64(multiplier) > math.MaxInt32 || row.Flag > 1 {
			return 0, ErrInvalid
		}
		total += uint64(row.Weight)
	}
	if total == 0 || total > luckyScale {
		return 0, ErrInvalid
	}
	return total, nil
}

func chooseLucky[T any](rows []T, roll uint64, weight func(T) uint64) (T, error) {
	for _, row := range rows {
		n := weight(row)
		if roll < n {
			return row, nil
		}
		roll -= n
	}
	var empty T
	return empty, ErrInvalid
}

func drawLucky[T any](rows []T, total uint64, weight func(T) uint64) (T, error) {
	roll, err := giftRandom(total)
	if err != nil {
		var empty T
		return empty, err
	}
	return chooseLucky(rows, roll, weight)
}

func (x *CommerceTx) OpenLuckyBox(q LuckyBoxRequest, c CommerceCatalog, now time.Time) error {
	d, ok := c.LuckyBoxes[q.Template]
	if !ok || q.Mode > 1 || q.Selected < 0 || q.Selected > 9 || q.Mode == 0 && q.Selected != 0 ||
		d.TreasureGaugeMaximum == 0 || d.TreasureGaugeMaximum > math.MaxInt32 || x.Inventory.TreasureGauge > d.TreasureGaugeMaximum ||
		d.MultiOpenMultiplier == 0 || d.MultiOpenMultiplier > math.MaxInt32 || now.Unix() <= 0 || !checkedLuckyWeights(d.Types) ||
		len(d.Grades) != 4 || len(d.Rewards) != 4 || len(d.TreasureGaugeIncrements) != 4 {
		return ErrInvalid
	}
	i, t, _, _, err := x.giftSource(q.Slot, now)
	if err != nil {
		return err
	}
	n := 1
	if q.Mode == 1 {
		n = 10
	}
	a := x.Inventory.Items[i]
	if a.TemplateID != q.Template || a.Count < uint32(n) || t.FirstBagSlot != 65 || t.PetConsumable ||
		t.AccountCommonInventory || !sameCommerceTemplate(d.Source, a.Template) {
		return ErrInvalid
	}
	var eligible []luckyChoice
	var totalEligible uint64
	for _, typ := range d.Types {
		increment, ok := d.TreasureGaugeIncrements[typ.Value]
		if !ok || increment > d.TreasureGaugeMaximum || !checkedLuckyWeights(d.Grades[typ.Value]) {
			return ErrInvalid
		}
		total, err := checkedLuckyRewards(d.Rewards[typ.Value], q.Template, d.MultiOpenMultiplier)
		if err != nil || total != luckyScale {
			return ErrInvalid
		}
		for _, grade := range d.Grades[typ.Value] {
			if grade.Value >= 2 && typ.Weight > 0 && grade.Weight > 0 {
				w := uint64(typ.Weight) * uint64(grade.Weight)
				eligible = append(eligible, luckyChoice{typ.Value, grade.Value, w})
				totalEligible += w
			}
		}
	}
	limitedTotal, err := checkedLuckyRewards(d.LimitedRewards, q.Template, 1)
	if err != nil {
		return err
	}
	weight := func(r LuckyWeight) uint64 { return uint64(r.Weight) }
	rewardWeight := func(r LuckyReward) uint64 { return uint64(r.Weight) }
	draws := make([]luckyDraw, n)
	guaranteed := n == 1
	for j := range draws {
		typ, err := drawLucky(d.Types, luckyScale, weight)
		if err != nil {
			return err
		}
		grade, err := drawLucky(d.Grades[typ.Value], luckyScale, weight)
		if err != nil {
			return err
		}
		reward, err := drawLucky(d.Rewards[grade.Value], luckyScale, rewardWeight)
		if err != nil {
			return err
		}
		draws[j] = luckyDraw{typ.Value, grade.Value, reward}
		guaranteed = guaranteed || grade.Value >= 2
	}
	if !guaranteed {
		index, err := giftRandom(10)
		if err != nil {
			return err
		}
		pick, err := drawLucky(eligible, totalEligible, func(r luckyChoice) uint64 { return r.weight })
		if err != nil {
			return err
		}
		reward, err := drawLucky(d.Rewards[pick.grade], luckyScale, rewardWeight)
		if err != nil {
			return err
		}
		draws[index] = luckyDraw{pick.boxType, pick.grade, reward}
	}
	if err = x.debitAsset(i, uint32(n)); err != nil {
		return err
	}
	gauge := x.Inventory.TreasureGauge
	for j, result := range draws {
		count := result.reward.Count
		if q.Mode == 1 && int(q.Selected) == j {
			count *= d.MultiOpenMultiplier
		}
		var t ordinaryTemplate
		if err = json.Unmarshal(result.reward.Template, &t); err != nil {
			return err
		}
		if err = x.grantCommerceItem(result.reward.Template, count, now, true); err != nil {
			return err
		}
		payout := LuckyBoxPayout{BoxType: result.boxType, ResultGrade: result.grade, TemplateID: t.TemplateID, Count: count}
		ceiling := luckyScale
		if gauge == d.TreasureGaugeMaximum {
			ceiling = limitedTotal
		}
		roll, err := giftRandom(ceiling)
		if err != nil {
			return err
		}
		if roll < limitedTotal {
			limited, err := chooseLucky(d.LimitedRewards, roll, rewardWeight)
			if err != nil {
				return err
			}
			if err = json.Unmarshal(limited.Template, &t); err != nil {
				return err
			}
			if err = x.grantCommerceItem(limited.Template, limited.Count, now, true); err != nil {
				return err
			}
			payout.LimitedTemplateID, payout.LimitedCount = t.TemplateID, limited.Count
			gauge = 0
		} else {
			gauge = uint32(min(uint64(gauge)+uint64(d.TreasureGaugeIncrements[result.grade]), uint64(d.TreasureGaugeMaximum)))
		}
		x.luckyRewards = append(x.luckyRewards, payout)
	}
	x.Inventory.TreasureGauge = gauge
	return nil
}
