package dungeon

import (
	"encoding/json"
	"errors"
	"math"
	"slices"
	"time"
)

type weightedItem struct{ Item, Weight uint32 }

type independentRow struct {
	OwnerKind, Flag                    byte
	DirectItem, ResultLimit            uint32
	LevelMin, LevelMax, Difficulty     int
	Probabilities, AttemptsByPartySize []uint32
	DungeonConditions                  []uint32
	Main, Replacement                  []weightedItem
}

type independentDropData struct {
	Hash    string
	Entries []struct {
		Owner   byte
		Code    uint32
		Entries []independentRow
	}
	Templates                                              map[uint32]json.RawMessage
	AllowedDirectItems, AllowedApcItems, AllowedApcSources []uint32
}

func (p *independentDropData) source(code uint32, apc bool) []independentRow {
	if p == nil {
		return nil
	}
	owner := byte(0)
	if apc {
		owner = 1
		if !slices.Contains(p.AllowedApcSources, code) {
			return nil
		}
	}
	for _, row := range p.Entries {
		if row.Owner == owner && row.Code == code {
			return row.Entries
		}
	}
	return nil
}

func pickWeightedItem(p []weightedItem, draw uint64) uint32 {
	for _, row := range p {
		weight := uint64(row.Weight)
		if draw < weight {
			return row.Item
		}
		draw -= weight
	}
	return 0
}

func (p *independentDropData) roll(r *Run, d Death, now time.Time, generation *equipmentGeneration, cards bool) ([]GroundReward, error) {
	mon, def := d.Monster, r.Definition.Dungeon
	apc := mon.AiScriptIndex != nil
	rows := p.source(mon.Code, apc)
	if len(rows) == 0 || cards && apc {
		return nil, nil
	}
	random, err := newDraw(r, d.Actor, mon, p.Hash)
	if err != nil {
		return nil, err
	}
	var initial *stableDraw
	if generation != nil && generation.DamagedEquipmentRatio != nil && *generation.DamagedEquipmentRatio < 100 {
		x, err := newDraw(r, d.Actor, mon, cardBoundDurabilityHash(p.Hash, generation))
		if err != nil {
			return nil, err
		}
		initial = &x
	}
	create := func(id, quantity uint32) (GroundReward, bool, error) {
		template := p.Templates[id]
		if len(template) == 0 || apc && !slices.Contains(p.AllowedApcItems, id) {
			return GroundReward{}, false, nil
		}
		var t dropTemplate
		if err := json.Unmarshal(template, &t); err != nil {
			return GroundReward{}, false, err
		}
		item := GroundReward{Template: template, ExpiresAt: dropExpiration(t, now)}
		if t.Kind == 2 {
			item.StackCount = quantity
		} else {
			item.QualitySeed = uint32(random.next(999999998)) + 1
			item.Durability = t.MaximumDurability
			if initial != nil && item.Durability > 0 {
				maximum := int(item.Durability)
				budget := int(float32(maximum) * (float32(100-*generation.DamagedEquipmentRatio) / 100))
				reduced := int(float32(maximum) + float32(budget)*(float32(initial.next(100))/-100))
				if reduced < 0 {
					reduced = 1
				}
				item.Durability = uint16(min(reduced, maximum))
			}
		}
		return item, item.ExpiresAt == nil || *item.ExpiresAt > now.Unix(), nil
	}
	var result []GroundReward
	difficulty := int(def.Difficulty)
	for _, row := range rows {
		level := int(mon.Level)
		if cards && row.OwnerKind != 0 || row.LevelMin > 0 && level < row.LevelMin || row.LevelMax > 0 && level > row.LevelMax ||
			row.Difficulty >= 0 && row.Difficulty != difficulty || difficulty >= len(row.Probabilities) || len(row.AttemptsByPartySize) == 0 {
			continue
		}
		if row.Flag == 0 && (def.TowerOfGrief || !mon.BlocksRoom || mon.Dummy || mon.HellPhase != 0 ||
			!slices.Contains(p.AllowedDirectItems, row.DirectItem)) {
			continue
		}
		attempts, cap, prob := row.AttemptsByPartySize[0], row.ResultLimit, row.Probabilities[difficulty]
		if attempts == 0 || cap == 0 || attempts > math.MaxUint16 || cap > math.MaxUint16 || prob == 0 {
			continue
		}
		bound := uint64(1000000)
		if row.Flag == 2 && !cards {
			bound = 100000000
		}
		var hits uint32
		for attempt := uint32(0); attempt < attempts; attempt++ {
			if random.next(bound) < uint64(prob) {
				hits++
			}
		}
		count := min(hits, cap)
		if count == 0 {
			continue
		}
		pool := row.Main
		if row.Flag == 5 && difficulty == 4 && slices.Contains(row.DungeonConditions, def.ID) {
			pool = row.Replacement
		}
		if cards && row.Flag == 3 || !cards && (row.Flag == 0 || len(pool) == 0 && row.DirectItem != 0) {
			item, ok, err := create(row.DirectItem, count)
			if err != nil {
				return nil, err
			}
			if !ok {
				continue
			}
			if item.StackCount != 0 {
				result = append(result, item)
			} else {
				if int(count) > 16-len(result) {
					return nil, errors.New("independent equipment copies exceed native object cap")
				}
				for i := uint32(0); i < count; i++ {
					result = append(result, item)
				}
			}
			continue
		}
		var total uint64
		for _, candidate := range pool {
			total += uint64(candidate.Weight)
		}
		if total == 0 {
			continue
		}
		stackSlots := map[uint32]int{}
		for i := uint32(0); i < count; i++ {
			id := pickWeightedItem(pool, random.next(total))
			if out, exists := stackSlots[id]; exists {
				result[out].StackCount++
				continue
			}
			item, ok, err := create(id, 1)
			if err != nil {
				return nil, err
			}
			if !ok {
				continue
			}
			if item.StackCount != 0 {
				stackSlots[id] = len(result)
			}
			result = append(result, item)
			if len(result) > 16 {
				return nil, errors.New("independent items exceed native object cap")
			}
		}
	}
	return result, nil
}
