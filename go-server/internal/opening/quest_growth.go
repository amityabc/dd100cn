package opening

import (
	"encoding/json"
	"fmt"
	"s9a1/server/internal/storage"
)

func (r *Run) ConsumableScene(character uint16) string {
	if r == nil || !r.ready || r.character != character {
		return ""
	}
	return fmt.Sprintf("%s:%d:%d:%d", r.runID, r.room.X, r.room.Y, r.room.MapID)
}

// ApplyQuestGrowth uses the same frozen experience and skill-grant tables as
// the tutorial, with quest-earned TP and the validated awakening stage.
func (cat *CombatCatalog) ApplyQuestGrowth(c storage.Character, award uint32, bonusTP uint16, grow byte, forceSkills bool) (storage.Character, uint32, error) {
	next, _, actual, err := cat.applyGrowth(c, award, bonusTP, &grow, forceSkills, false)
	return next, actual, err
}

func (cat *CombatCatalog) ApplyItemExperience(c storage.Character, award uint32) (storage.Character, uint32, error) {
	next, _, actual, err := cat.applyGrowth(c, award, 0, nil, false, true)
	return next, actual, err
}

func (cat *CombatCatalog) ExperienceRange(level byte) (uint64, uint64, error) {
	if len(cat.Levels) != 100 || level == 0 || level >= 100 {
		return 0, 0, storage.ErrInvalid
	}
	var previous int64
	for i := 0; i < 99; i++ {
		n := cat.Levels[i].Threshold
		if n <= previous {
			return 0, 0, storage.ErrInvalid
		}
		previous = n
	}
	low := int64(0)
	if level > 1 {
		low = cat.Levels[level-2].Threshold
	}
	return uint64(low), uint64(cat.Levels[level-1].Threshold), nil
}

func (cat *CombatCatalog) SupportsClassBranch(job, grow byte) bool {
	_, ok := cat.Jobs[job].Grants[grow]
	return grow >= 1 && grow <= 5 && ok
}

func (cat *CombatCatalog) ClassStats(c storage.Character, grow byte) (json.RawMessage, error) {
	job, ok := cat.Jobs[c.Job]
	if !ok || !cat.SupportsClassBranch(c.Job, grow&15) || c.Level == 0 || c.Level > 100 || grow > 0x35 {
		return nil, storage.ErrInvalid
	}
	return job.growth(c.Level, grow, c.Stats)
}

func (cat *CombatCatalog) ClassReplacementCost(level, count byte) (uint32, error) {
	p := cat.Replacement
	if p == nil || p.MinimumLevel == 0 || level < p.MinimumLevel || level > p.MaximumLevel || len(p.Gold) == 0 {
		return 0, storage.ErrInvalid
	}
	return p.Gold[min(int(count), len(p.Gold)-1)], nil
}

func (cat *CombatCatalog) ApplyLevelCapsule(c storage.Character, target, selected byte) (storage.Character, error) {
	if c.Level == 0 || c.Level >= target || target < 2 || target > 100 || len(cat.Levels) != 100 {
		return c, storage.ErrInvalid
	}
	var doc, prog object
	var p progress
	if json.Unmarshal(c.Gameplay, &doc) != nil || json.Unmarshal(doc["Progression"], &prog) != nil || json.Unmarshal(doc["Progression"], &p) != nil || prog == nil || p.GrowType > 0x35 {
		return c, storage.ErrInvalid
	}
	grow := p.GrowType
	branch := grow & 15
	special := c.Job == 9 || c.Job == 10
	if !special && branch == 0 {
		if grow != 0 || !cat.SupportsClassBranch(c.Job, selected) {
			return c, storage.ErrInvalid
		}
		grow, branch = selected, selected
	} else if selected != 0 {
		return c, storage.ErrInvalid
	}
	if special && branch != 0 || !special && !cat.SupportsClassBranch(c.Job, branch) {
		return c, storage.ErrInvalid
	}
	var before int64 = -1
	for i := 0; i < int(target)-1; i++ {
		threshold := cat.Levels[i].Threshold
		if threshold < 0 || threshold <= before {
			return c, storage.ErrInvalid
		}
		before = threshold
	}
	xp := uint64(before)
	if p.Experience > xp {
		return c, storage.ErrInvalid
	}
	prog["Experience"], _ = json.Marshal(xp)
	doc["Progression"], _ = json.Marshal(prog)
	c.Gameplay, _ = json.Marshal(doc)
	next, after, _, err := cat.applyGrowth(c, 0, 0, &grow, false, true)
	if err != nil {
		return c, err
	}
	if next.Level != target || after.Experience != xp || after.GrowType != grow {
		return c, storage.ErrInvalid
	}
	return next, nil
}

func (cat *CombatCatalog) QuestFreeSkills(job, level, grow byte) map[uint16]byte {
	ranks := map[uint16]byte{}
	for _, grant := range cat.Jobs[job].Grants[grow&15] {
		if grant.RequiredLevel <= level && grant.Awakening <= grow>>4 && grant.Level > ranks[grant.ID] {
			ranks[grant.ID] = grant.Level
		}
	}
	return ranks
}
