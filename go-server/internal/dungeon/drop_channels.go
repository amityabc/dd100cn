package dungeon

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"math"
	"strconv"
	"time"

	"s9a1/server/internal/storage"
)

type LuckDebit struct {
	Item                   uint32
	Grade                  int
	Percent, Before, After int64
}

type LuckRoll struct {
	Version, Hash  string
	CharacterLevel byte
	Before, After  int64
	Debits         []LuckDebit
	policy         LuckPolicy
}

func (p LuckPolicy) Start(character storage.Character) (*LuckRoll, error) {
	var profile struct {
		Progression struct{ LuckPoints, LuckPlayMinutes int64 }
	}
	if err := json.Unmarshal(character.Gameplay, &profile); err != nil {
		return nil, err
	}
	x := profile.Progression
	if x.LuckPoints < 0 || x.LuckPoints > 100000 || x.LuckPlayMinutes < 0 {
		return nil, storage.ErrInvalid
	}
	return &LuckRoll{Version: "OrdinarySoloLuck/v1;wide-integer-percent;character-owner;generation-debit", Hash: p.Hash,
		CharacterLevel: character.Level, Before: x.LuckPoints, After: x.LuckPoints, Debits: []LuckDebit{}, policy: p}, nil
}

func (l *LuckRoll) threshold(values []uint32, profile, rarity, channel byte) int64 {
	start := int(profile) * 8
	offset := start + int(rarity)
	if l == nil || channel != 2 || rarity > 1 {
		return int64(values[offset])
	}
	var multiplier int64
	for _, row := range l.policy.Bands {
		if l.After <= row.Maximum {
			multiplier = row.PartsPer10000
			break
		}
	}
	delta := -(int64(values[start+2]) - int64(values[start+1])) * multiplier / 10000
	if rarity == 1 {
		return int64(values[offset]) + delta
	}
	if values[start+1] != 0 {
		delta = int64(values[start]) * delta / int64(values[start+1])
	} else {
		delta = 0
	}
	return int64(values[offset]) + delta
}

func (l *LuckRoll) deduct(it dropCandidate) {
	if l == nil || !it.Equipment || it.Rarity != 2 {
		return
	}
	var percent int64
	if len(l.policy.GapPairs) >= 6 {
		gap := max(it.Grade-int(l.CharacterLevel), int(l.CharacterLevel)-it.Grade)
		for _, row := range l.policy.RareDeduction {
			if int(l.CharacterLevel) > row.MaximumLevel {
				continue
			}
			if len(row.PartsPer10000) != 4 {
				break
			}
			percent = row.PartsPer10000[3]
			for i := range 3 {
				if gap >= l.policy.GapPairs[i*2+1] {
					percent = row.PartsPer10000[i]
					break
				}
			}
			break
		}
	}
	old := l.After
	l.After = min(99999, max(0, old-old*percent/10000))
	l.Debits = append(l.Debits, LuckDebit{it.Id, it.Grade, percent, old, l.After})
}

func (l *LuckRoll) Apply(c storage.Character) (storage.Character, error) {
	if l == nil || l.Before == l.After {
		return c, nil
	}
	var root, prog map[string]json.RawMessage
	if err := json.Unmarshal(c.Gameplay, &root); err != nil {
		return c, err
	}
	if err := json.Unmarshal(root["Progression"], &prog); err != nil {
		return c, err
	}
	prog["LuckPoints"], _ = json.Marshal(l.After)
	root["Progression"], _ = json.Marshal(prog)
	c.Gameplay, _ = json.Marshal(root)
	return c, nil
}

func (t *DropData) prepare() error {
	t.mainPools, t.secondaryPools = make(map[[4]byte][]uint32), make(map[[4]byte][]uint32)
	for group, pools := range [][]dropPool{t.MainPools, t.SecondaryPools} {
		to := t.mainPools
		if group == 1 {
			to = t.secondaryPools
		}
		for _, p := range pools {
			k := [4]byte{p.Kind, p.Level, p.Rarity, p.Channel}
			if len(p.Ids) == 0 || to[k] != nil || p.Rarity > 7 || p.Channel < 1 || p.Channel > 4 || p.Level == 0 {
				return errors.New("invalid prepared dungeon item pool")
			}
			for _, id := range p.Ids {
				if c := t.Candidates[id]; c.Id != id || c.CreationWeight == 0 {
					return errors.New("missing weighted dungeon drop candidate")
				}
			}
			to[k] = p.Ids
		}
	}
	t.MainPools, t.SecondaryPools = nil, nil
	return nil
}

func (t *DropData) rollRegular(r *Run, d Death, restrict byte, now time.Time, luck *LuckRoll, first uint32) ([]GroundReward, error) {
	level := r.Definition.Dungeon.BasisLevel
	if level == 0 {
		level = d.Monster.Level
	}
	var drops []GroundReward
	suppressed := false
	if restrict&4 == 0 {
		for _, extra := range t.Secondary {
			if extra.Kind != 0 {
				continue
			}
			p := extra.dropRate
			if row := p.forLevel(level); len(row) >= 5 {
				random, err := newDraw(r, d.Actor, d.Monster, extra.Hash)
				if err != nil {
					return nil, err
				}
				drops, suppressed, err = t.rollItemChannels(r, d, row, &p, &random, now, luck, t.secondaryPools, 0, extra.Hash, 1)
				if err != nil {
					return nil, err
				}
			}
			break
		}
	}
	p := &t.Native.dropRate
	row := p.forLevel(level)
	if len(row) < 5 || restrict&5 == 5 {
		return drops, nil
	}
	draw, err := newDraw(r, d.Actor, d.Monster, t.Hash)
	if err != nil {
		return nil, err
	}
	if restrict&1 == 0 {
		gold, err := t.rollGold(r, d, &draw, row[0])
		if err != nil {
			return nil, err
		}
		drops = append(drops, gold...)
	}
	if !suppressed && restrict&4 == 0 && len(t.mainPools) != 0 {
		more, _, err := t.rollItemChannels(r, d, row, p, &draw, now, luck, t.mainPools, 0, "", first)
		if err != nil {
			return nil, err
		}
		drops = append(drops, more...)
	}
	return drops, nil
}

func (p *dropRate) forLevel(level byte) []uint32 {
	for _, row := range p.Probabilities {
		if level >= row.MinimumLevel && level <= row.MaximumLevel {
			return row.Per10000
		}
	}
	return nil
}

func dropBonus(values []json.Number, index int) json.Number {
	if index < len(values) {
		return values[index]
	}
	return "1"
}

func (t *DropData) rollItemChannels(r *Run, d Death, row []uint32, p *dropRate, draw *stableDraw, now time.Time,
	luck *LuckRoll, pools map[[4]byte][]uint32, kind byte, secondaryHash string, first uint32) ([]GroundReward, bool, error) {
	var out []GroundReward
	selected := false
	diff, mon := int(r.Definition.Dungeon.Difficulty), d.Monster
	for channel := byte(1); channel <= 4; channel++ {
		if secondaryHash != "" && row[channel] == 0 {
			continue
		}
		factors := []json.Number{dropBonus(p.DifficultyBonus, int(channel)*5+diff),
			dropBonus(p.PartyBonus, int(channel)*4), dropBonus(p.MonsterBonus, int(channel)*4+int(mon.Type))}
		if channel == 2 && first != 1 {
			factors = append(factors, json.Number(strconv.FormatUint(uint64(first), 10)))
		}
		hit, err := scaledFloor(uint64(row[channel]), 10000, factors...)
		if err != nil {
			return nil, false, err
		}
		profile := channel - 1
		if draw.next(10000) >= hit || len(p.RarityThresholds) < (int(profile)+1)*8 {
			continue
		}
		roll := int64(draw.next(1000000))
		rarity := byte(0)
		for rarity < 8 && roll >= luck.threshold(p.RarityThresholds, profile, rarity, channel) {
			rarity++
		}
		if rarity == 8 {
			continue
		}
		pool := pools[[4]byte{kind, mon.Level, rarity, channel}]
		if len(pool) == 0 {
			continue
		}
		var total uint64
		for _, id := range pool {
			total += uint64(t.Candidates[id].CreationWeight)
		}
		weight := draw.next(total)
		var candidate dropCandidate
		for _, id := range pool {
			candidate = t.Candidates[id]
			step := uint64(candidate.CreationWeight)
			if weight < step {
				break
			}
			weight -= step
		}
		selected = true
		if len(candidate.Artifact) != 0 && string(candidate.Artifact) != "null" {
			var t dropTemplate
			if err := json.Unmarshal(candidate.Artifact, &t); err != nil || t.Kind != 2 || t.TemplateID != candidate.Id ||
				t.WornSlot < 27 || t.WornSlot > 29 || t.ResourceExpiration != nil || t.RelativeLifetimeSeconds != 0 || t.DailyDeletion || t.WeeklyDeletionDay != nil {
				return nil, false, storage.ErrInvalid
			}
			out = append(out, GroundReward{Artifact: candidate.Artifact, QualitySeed: uint32(draw.next(999999998)) + 1, Durability: t.MaximumDurability})
			continue
		}
		if len(candidate.Template) == 0 || string(candidate.Template) == "null" {
			continue
		}
		luck.deduct(candidate)
		var item dropTemplate
		if err := json.Unmarshal(candidate.Template, &item); err != nil {
			return nil, false, err
		}
		reward := GroundReward{Template: candidate.Template, ExpiresAt: dropExpiration(item, now)}
		if item.Kind == 2 {
			reward.StackCount = 1
		} else {
			if err := t.generateEquipment(r, d, draw, &reward, item, candidate, secondaryHash); err != nil {
				return nil, false, err
			}
		}
		if reward.ExpiresAt == nil || *reward.ExpiresAt > now.Unix() {
			out = append(out, reward)
		}
	}
	return out, selected, nil
}

func (t *DropData) generateEquipment(r *Run, d Death, fallback *stableDraw, reward *GroundReward,
	tpl dropTemplate, item dropCandidate, secondaryHash string) error {
	p := t.EquipmentGeneration
	reward.Durability = tpl.MaximumDurability
	if p != nil && p.DamagedEquipmentRatio != nil && *p.DamagedEquipmentRatio < 100 && reward.Durability > 0 {
		hash := p.InitialDurabilityHash
		if secondaryHash != "" {
			sum := sha256.Sum256([]byte(secondaryHash + "\ninitial-durability-callers-reference/v1\n" + hash))
			hash = hex.EncodeToString(sum[:])
		}
		q, err := newDraw(r, d.Actor, d.Monster, hash)
		if err != nil {
			return err
		}
		damageBudget := int(float32(reward.Durability) * (float32(100-*p.DamagedEquipmentRatio) / 100))
		value := int(float32(reward.Durability) + float32(damageBudget)*(float32(q.next(100))/-100))
		if value < 0 {
			value = 1
		}
		reward.Durability = uint16(min(value, int(reward.Durability)))
	}
	if p == nil || secondaryHash != "" {
		reward.QualitySeed = uint32(fallback.next(999999998)) + 1
		return nil
	}
	pr, err := newDraw(r, d.Actor, d.Monster, p.Hash)
	if err != nil {
		return err
	}
	gap := int(d.Monster.Level) - item.Grade
	factor := p.factor(gap)
	seeds := [...]uint32{0x5aa7, 0x0fa6, 0x6a23, 0x5df0, 0x7270}
	reward.QualitySeed = seeds[0]
	if len(p.Rates) > 0 {
		initial := uint64(float32(1000) * factor)
		at := pr.next(initial + 4001)
		for i, seed := range seeds {
			if at <= initial+1000*uint64(i) {
				reward.QualitySeed = seed
				break
			}
		}
	}
	weights := p.Rarities[item.Rarity]
	if len(weights) == 0 || !item.CanRandomUpgrade {
		return nil
	}
	var total uint64
	for _, row := range weights {
		total += row.Weight
	}
	if total == 0 {
		return nil
	}
	first := uint64(float32(weights[0].Weight) * factor)
	total = total - weights[0].Weight + first
	if total > math.MaxInt32 {
		return errors.New("generated equipment weight exceeds native range")
	}
	roll := pr.next(total + 1)
	cumulative := first
	for i, row := range weights {
		if i > 0 {
			cumulative += row.Weight
		}
		if roll <= cumulative {
			reward.Reinforcement = row.Level
			break
		}
	}
	return nil
}

func (p equipmentGeneration) factor(gap int) float32 {
	if len(p.Rates) == 0 {
		return 1
	}
	if first := p.Rates[0]; gap > first.Gap {
		return first.Factor
	}
	if last := p.Rates[len(p.Rates)-1]; gap < last.Gap {
		return last.Factor
	}
	for _, t := range p.Rates {
		if t.Gap == gap {
			return t.Factor
		}
	}
	return 1
}
