package dungeon

import (
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"slices"
	"strings"
	"time"
)

type pvfRewardCandidate struct {
	Quantity, Weight uint32
	Template         json.RawMessage
}

type pvfRewardPool struct {
	SelectionCount byte
	Candidates     []pvfRewardCandidate
}

type pvfRewardBranch struct {
	Weight               uint32
	Pool, CompletionPool *pvfRewardPool
}

type pvfRewardData struct {
	Hash   string
	Groups []struct {
		Dungeon          uint32
		Maze, Difficulty byte
		ChallengeStage   uint16
		Branches         []pvfRewardBranch
	}
	Indexed []struct {
		Dungeon uint32
		Maze    byte
		Pool    *pvfRewardPool
	}
}

func (p *pvfRewardData) choose(r *Run, draw *stableDraw, completion bool) *pvfRewardPool {
	def := r.Definition.Dungeon
	for _, group := range p.Groups {
		if group.Dungeon != def.ID || group.Maze != def.MazeIndex ||
			group.Difficulty != def.Difficulty || group.ChallengeStage != def.ChallengeStage {
			continue
		}
		var total uint64
		for _, branch := range group.Branches {
			total += uint64(branch.Weight)
		}
		if total == 0 || total > math.MaxUint32 {
			return nil
		}
		value := draw.next(total)
		for _, branch := range group.Branches {
			if value < uint64(branch.Weight) {
				if completion {
					return branch.CompletionPool
				}
				return branch.Pool
			}
			value -= uint64(branch.Weight)
		}
	}
	return nil
}

func (p *pvfRewardPool) roll(draw *stableDraw, now time.Time, maxWeight uint64) ([]GroundReward, error) {
	if p == nil {
		return nil, nil
	}
	remaining := slices.Clone(p.Candidates)
	var items []GroundReward
	for range p.SelectionCount {
		if len(remaining) == 0 {
			break
		}
		var total uint64
		for _, row := range remaining {
			total += uint64(row.Weight)
		}
		if total == 0 || total > maxWeight {
			return nil, errors.New("PVF reward pool weights are inconsistent")
		}
		value := draw.next(total)
		var selected pvfRewardCandidate
		for index, row := range remaining {
			if value < uint64(row.Weight) {
				selected = row
				remaining = slices.Delete(remaining, index, index+1)
				break
			}
			value -= uint64(row.Weight)
		}
		if len(selected.Template) == 0 || string(selected.Template) == "null" || selected.Quantity == 0 {
			continue
		}
		var t dropTemplate
		if err := json.Unmarshal(selected.Template, &t); err != nil {
			return nil, err
		}
		reward := GroundReward{Template: selected.Template, ExpiresAt: dropExpiration(t, now)}
		switch {
		case t.Kind == 2 && selected.Quantity <= t.StackLimit:
			reward.StackCount = selected.Quantity
		case t.Kind <= 1 && selected.Quantity == 1:
			reward.QualitySeed = uint32(draw.next(999999998)) + 1
			reward.Durability = t.MaximumDurability
		default:
			continue
		}
		if reward.ExpiresAt == nil || *reward.ExpiresAt > now.Unix() {
			items = append(items, reward)
		}
	}
	return items, nil
}

func (p *pvfRewardData) ground(r *Run, d Death, now time.Time) ([]GroundReward, error) {
	def := r.Definition.Dungeon
	m := d.Monster
	if p == nil || !def.UsesOrdinaryRewards || def.Difficulty >= 5 || r.Room.X != def.BossX ||
		r.Room.Y != def.BossY || m.Type != 3 || m.Dummy || m.AiScriptIndex != nil {
		return nil, nil
	}
	draw, err := newDraw(r, d.Actor, m, p.Hash)
	if err != nil {
		return nil, err
	}
	return p.choose(r, &draw, false).roll(&draw, now, math.MaxInt32)
}

func (p *pvfRewardData) completion(r *Run) (*CardReward, error) {
	def := r.Definition.Dungeon
	if p == nil || def.DisableClearReward || !def.UsesOrdinaryRewards || def.Difficulty >= 5 {
		return nil, nil
	}
	const policy = "LocalDungeonPvFCompletion/v9;difficulty-allow-list;secretshop-dungeon-npc;clearreward-dungeon-index-override;ground-and-clear-pools;difficulty-subpools;native-six-field-pool;weighted-without-replacement;final-boss-only"
	h := sha256.Sum256([]byte(fmt.Sprintf("%s|completion|%d|%s|%d|%d|%d|%d|%s", policy, r.Seed,
		strings.ReplaceAll(strings.ToLower(r.ID), "-", ""), def.ID, def.MazeIndex, def.Difficulty, def.ChallengeStage, p.Hash)))
	var draw stableDraw
	copy(draw.state[:], h[:])
	var pool *pvfRewardPool
	for _, index := range p.Indexed {
		if index.Dungeon == def.ID && index.Maze == def.MazeIndex {
			pool = index.Pool
			break
		}
	}
	if pool == nil {
		pool = p.choose(r, &draw, true)
	}
	items, err := pool.roll(&draw, time.Unix(0, 0), math.MaxUint32)
	if err != nil || len(items) == 0 {
		return nil, err
	}
	reward := &CardReward{Policy: "LocalDungeonPvFCompletion", ResourceHash: p.Hash}
	for _, item := range items {
		var t dropTemplate
		if err := json.Unmarshal(item.Template, &t); err != nil {
			return nil, err
		}
		reward.Items = append(reward.Items, CardItem{Reward: item, Metadata: cardLuckItem{Item: t.TemplateID}})
	}
	return reward, nil
}
