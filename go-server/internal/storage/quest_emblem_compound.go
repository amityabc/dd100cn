package storage

import (
	"encoding/json"
	"maps"
	"math"
	"slices"
	"time"
)

type EmblemCompoundInput struct {
	Slot     uint16
	Template uint32
}

type rewardGraphNode struct {
	Leaf  json.RawMessage
	Pools []GiftPool
}

type EmblemCompoundCatalog struct {
	Sources map[uint32]struct {
		Template json.RawMessage
		Grade    int
	}
	Roots map[int]map[int]uint32
	Nodes map[uint32]rewardGraphNode
}

func (x *CommerceTx) CompoundEmblems(rows []EmblemCompoundInput, cat EmblemCompoundCatalog, now time.Time) ([]GiftReward, error) {
	if len(rows) < 2 || len(rows) > 5 || !x.livingEmblemUser(now) {
		return nil, ErrInvalid
	}
	used := make(map[int]uint32, len(rows))
	var grade int
	for j, r := range rows {
		i, t, err := x.emblemBag(r.Slot, now)
		if err != nil {
			return nil, err
		}
		a := x.Inventory.Items[i]
		source, ok := cat.Sources[r.Template]
		if !ok || t.TemplateID != r.Template || t.Kind != 2 || t.FirstBagSlot != 289 || t.LastBagSlot != 360 ||
			j > 0 && source.Grade != grade || !sameEmblemTemplate(source.Template, a.Template) {
			return nil, ErrInvalid
		}
		grade = source.Grade
		used[i]++
		if used[i] > a.Count {
			return nil, ErrInvalid
		}
	}
	rewards, err := x.drawRewardGraph([]uint32{cat.Roots[grade][len(rows)]}, cat.Nodes, 32)
	if err != nil {
		return nil, err
	}
	for i, count := range used {
		if err = x.debitAsset(i, count); err != nil {
			return nil, err
		}
	}
	for _, r := range rewards {
		if err = x.grantCommerceItem(r.Template, r.Count, now, false); err != nil {
			return nil, err
		}
	}
	return rewards, nil
}

func sameEmblemTemplate(prepared, saved json.RawMessage) bool {
	var p map[string]json.RawMessage
	var s materialDefinition
	if json.Unmarshal(prepared, &p) != nil || p == nil || json.Unmarshal(saved, &s) != nil {
		return false
	}
	p["PvfGiftItem"] = json.RawMessage("false")
	if s.PvfGiftItem {
		p["PvfGiftItem"] = json.RawMessage("true")
	}
	norm, err := json.Marshal(p)
	return err == nil && sameCommerceTemplate(norm, saved)
}

func (x *CommerceTx) drawRewardGraph(roots []uint32, nodes map[uint32]rewardGraphNode, limit int) ([]GiftReward, error) {
	var profile struct{ Progression struct{ GrowType byte } }
	if len(roots) == 0 || x.Character.Job >= 16 || json.Unmarshal(x.Character.Gameplay, &profile) != nil {
		return nil, ErrInvalid
	}
	counts := map[uint32]uint32{}
	steps := 0
	var draw func(uint32, int) error
	draw = func(id uint32, depth int) error {
		node, ok := nodes[id]
		steps++
		if !ok || depth > 8 || steps > 4096 {
			return ErrInvalid
		}
		if len(node.Leaf) > 0 && string(node.Leaf) != "null" {
			counts[id]++
			if len(counts) > limit {
				return ErrInvalid
			}
			return nil
		}
		matched := false
		for _, p := range node.Pools {
			if p.UsableJobs&(1<<x.Character.Job) == 0 || x.Character.Level < p.MinimumLevel || x.Character.Level > p.MaximumLevel ||
				p.GrowType != nil && *p.GrowType != profile.Progression.GrowType&15 {
				continue
			}
			matched = true
			var total uint64
			for _, e := range p.Entries {
				if e.Weight > math.MaxInt64-total {
					return ErrInvalid
				}
				total += e.Weight
			}
			if total == 0 || p.DrawCount < 1 || p.DrawCount > 4096 {
				return ErrInvalid
			}
			for range p.DrawCount {
				roll, err := giftRandom(total)
				if err != nil {
					return err
				}
				for _, e := range p.Entries {
					if roll >= e.Weight {
						roll -= e.Weight
						continue
					}
					for range e.Reward.Count {
						if err = draw(e.Reward.TemplateID, depth+1); err != nil {
							return err
						}
					}
					break
				}
			}
		}
		if !matched {
			return ErrInvalid
		}
		return nil
	}
	for _, root := range roots {
		if err := draw(root, 0); err != nil {
			return nil, err
		}
	}
	if len(counts) == 0 {
		return nil, ErrInvalid
	}
	out := make([]GiftReward, 0, len(counts))
	for _, id := range slices.Sorted(maps.Keys(counts)) {
		out = append(out, GiftReward{TemplateID: id, Count: counts[id], Template: nodes[id].Leaf})
	}
	return out, nil
}
