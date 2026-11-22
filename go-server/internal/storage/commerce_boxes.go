package storage

import (
	"encoding/json"
	"maps"
	"math"
	"slices"
	"time"
)

type LotteryRule struct {
	Source       json.RawMessage
	GoldCost     uint32
	ForbidScene8 bool
	Materials    []CommercePayment
	Prizes       []struct {
		Weight     uint64
		TemplateID uint32
	}
}

type KeyedBoxRule struct {
	Source, Key, Reward json.RawMessage
	UsableEvent         uint16
}

type KeyedBoxRequest struct {
	SourceSlot, KeySlot, Count uint16
	SourceID, KeyID            uint32
}

func (c CommerceCatalog) BoxCarryWeights(base map[uint32]int64) map[uint32]int64 {
	out := make(map[uint32]int64, len(c.BoxWeights)+len(base))
	maps.Copy(out, c.BoxWeights)
	maps.Copy(out, base)
	return out
}

func (x *CommerceTx) OpenLottery(slot uint16, ordinaryTown bool, c CommerceCatalog, now time.Time, limits map[byte]int64, weights map[uint32]int64) error {
	i, _, _, _, err := x.giftSource(slot, now)
	if err != nil {
		return err
	}
	source := x.Inventory.Items[i]
	d, ok := c.Lottery[source.TemplateID]
	if !ok || !sameCommerceTemplate(d.Source, source.Template) || d.ForbidScene8 && !ordinaryTown ||
		d.GoldCost > math.MaxInt32 || x.Inventory.Gold < int64(d.GoldCost) || len(d.Materials) > 1 || len(d.Prizes) == 0 {
		return ErrInvalid
	}
	pool := GiftPool{DrawCount: 1}
	var total uint64
	for _, p := range d.Prizes {
		if p.Weight == 0 {
			continue
		}
		var t ordinaryTemplate
		prize := c.LotteryTemplates[p.TemplateID]
		if json.Unmarshal(prize, &t) != nil || t.TemplateID != p.TemplateID || t.Kind > 1 || t.Kind < 0 ||
			t.FirstBagSlot < 3 || t.LastBagSlot > 64 || p.Weight > math.MaxUint64-total {
			return ErrInvalid
		}
		total += p.Weight
		pool.Entries = append(pool.Entries, struct {
			Weight uint64
			Reward GiftReward
		}{p.Weight, GiftReward{TemplateID: p.TemplateID, Count: 1, Template: prize}})
	}
	if total == 0 {
		return ErrInvalid
	}
	if err = x.debitAsset(i, 1); err != nil {
		return err
	}
	x.Inventory.Gold -= int64(d.GoldCost)
	for _, m := range d.Materials {
		if err = x.spendGiftMaterial(m, source.UID, nil, now); err != nil {
			return err
		}
	}
	if err = x.checkGiftPools(GiftDefinition{}, []GiftPool{pool}, nil, now, limits, weights); err != nil {
		return err
	}
	draw, err := giftRandom(total)
	if err != nil {
		return err
	}
	for _, p := range pool.Entries {
		if draw >= p.Weight {
			draw -= p.Weight
			continue
		}
		if err = x.grantCommerceItem(p.Reward.Template, 1, now, false); err != nil {
			return err
		}
		if len(x.Grants) != 1 || x.Granted.Location != 0 || x.Granted.Slot < 3 || x.Granted.Slot > 64 {
			return ErrInvalid
		}
		return x.CheckCarry(limits, weights)
	}
	return ErrInvalid
}

func (x *CommerceTx) OpenKeyedBox(q KeyedBoxRequest, c CommerceCatalog, now time.Time) error {
	d, ok := c.KeyedBoxes[q.SourceID]
	if !ok || !slices.Contains(c.Activities, d.UsableEvent) {
		return ErrInvalid
	}
	var t ordinaryTemplate
	if json.Unmarshal(d.Reward, &t) != nil || t.Kind != 2 || t.TemplateID == q.SourceID || t.TemplateID == q.KeyID ||
		q.SourceID == q.KeyID || q.SourceSlot == q.KeySlot || q.Count == 0 || q.Count > 100 {
		return ErrInvalid
	}
	count := uint32(q.Count)
	require := func(slot uint16, id uint32, template json.RawMessage) (int, error) {
		i, actual, _, _, err := x.giftSource(slot, now)
		if err != nil {
			return i, err
		}
		a := x.Inventory.Items[i]
		if actual.TemplateID != id || a.Count < count || !sameCommerceTemplate(template, a.Template) {
			return i, ErrInvalid
		}
		return i, nil
	}
	source, err := require(q.SourceSlot, q.SourceID, d.Source)
	if err != nil {
		return err
	}
	key, err := require(q.KeySlot, q.KeyID, d.Key)
	if err != nil {
		return err
	}
	if err = x.debitAsset(source, count); err != nil {
		return err
	}
	if err = x.debitAsset(key, count); err != nil {
		return err
	}
	if err = x.grantCommerceItem(d.Reward, count, now, true); err != nil {
		return err
	}
	var granted uint32
	for _, a := range x.Grants {
		if a.Location != 0 || a.TemplateID != t.TemplateID || a.Slot == 0 || a.Slot == math.MaxUint16 {
			return ErrInvalid
		}
		granted += a.Count
	}
	if len(x.Grants) > 100 || granted != count {
		return ErrInvalid
	}
	return nil
}
