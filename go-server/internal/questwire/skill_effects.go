package questwire

import (
	"encoding/json"
	"math"
	"slices"

	"s9a1/server/internal/storage"
)

type effectSkill struct {
	Job        byte
	Definition skillDefinition
}

type skillDefinition struct {
	Id                                         uint16
	RequiredLevel, Interval                    int
	FreeLevel                                  byte
	OrdinarySp, GuildSkill                     bool
	Active                                     bool
	Maximum, GrowMaximum, SecondMaximum, Costs []int
	TpCosts                                    []int
	Prerequisites                              []struct {
		Id    uint16
		Level byte
	}
	QuestDiscount *struct {
		QuestId uint16
		Amounts []int
	}
	ThirdAwakeningRequirements map[int]bool
}

func (d skillDefinition) cap(level, grow byte, premium bool, cleared map[uint16]byte) int {
	branch, stage := int(grow&15), int(grow>>4)
	for phase, expected := range d.ThirdAwakeningRequirements {
		if phase == 1 && (cleared[6413] != 0) != expected || phase == 2 && (stage == 3) != expected || phase != 1 && phase != 2 {
			return 0
		}
	}
	indexed := func(rows []int, index int) int {
		if len(rows) == 1 {
			return rows[0]
		}
		if index >= len(rows) {
			return 0
		}
		return rows[index]
	}
	maximum := indexed(d.Maximum, branch)
	primary, secondary := maximum, 0
	if len(d.GrowMaximum) != 0 {
		primary = indexed(d.GrowMaximum, branch)
	}
	if stage != 0 && len(d.SecondMaximum) == 18 {
		secondary = d.SecondMaximum[branch*3+stage-1]
	}
	cap := min(maximum, max(primary, secondary))
	learning := int(level)
	if premium && !d.GuildSkill {
		learning += 5
	}
	if learning < d.RequiredLevel {
		return 0
	}
	if d.Interval > 0 {
		cap = min(cap, 1+(learning-d.RequiredLevel)/d.Interval)
	}
	return min(cap, math.MaxUint8)
}

func (d skillDefinition) cost(rank int, cleared map[uint16]byte) int {
	cost := d.Costs[min(rank, len(d.Costs))-1]
	if q := d.QuestDiscount; q != nil && cleared[q.QuestId] > 0 {
		cost = max(0, cost-q.Amounts[min(rank, len(q.Amounts))-1])
	}
	return cost
}

type questSkill struct {
	Id, Slot uint16
	Level    byte
}

type skillPage map[string]json.RawMessage

func (catalog *Catalog) applySkillEffects(before storage.Character, after storage.Character, q *Quest, opts CompleteContext) (storage.Character, error) {
	var from, to skillPage
	if json.Unmarshal(before.Gameplay, &from) != nil || json.Unmarshal(after.Gameplay, &to) != nil || opts.FreeSkills == nil {
		return after, storage.ErrInvalid
	}
	prior, err := playerOf(before)
	if err != nil {
		return after, err
	}
	next, err := playerOf(after)
	if err != nil {
		return after, err
	}
	free := opts.FreeSkills(after.Job, after.Level, next.grow)
	apply := func(priorRows, nextRows json.RawMessage, points skillPage, premium bool) ([]questSkill, error) {
		var saved, rows []questSkill
		var sp uint16
		if json.Unmarshal(priorRows, &saved) != nil || json.Unmarshal(nextRows, &rows) != nil || json.Unmarshal(points["Sp"], &sp) != nil {
			return nil, storage.ErrInvalid
		}
		balance := int(sp)
		seen := map[uint16]bool{}
		for _, effect := range q.SkillEffects {
			if effect.Job != after.Job {
				continue
			}
			if seen[effect.Id] || !effect.SpDiscount && !effect.MaximumRefund {
				return nil, storage.ErrInvalid
			}
			seen[effect.Id] = true
			index := func(v []questSkill) int {
				return slices.IndexFunc(v, func(row questSkill) bool { return row.Id == effect.Id })
			}
			previous := index(saved)
			if previous < 0 {
				continue
			}
			i := index(rows)
			d := catalog.effectSkills[[2]uint16{uint16(effect.Job), effect.Id}]
			if i < 0 || d.Id != effect.Id || d.Id == 0 || d.RequiredLevel < 1 || d.Interval < 0 || len(d.Maximum) == 0 || len(d.Costs) == 0 ||
				(d.QuestDiscount != nil && len(d.QuestDiscount.Amounts) == 0) {
				return nil, storage.ErrInvalid
			}
			earned := int(max(d.FreeLevel, free[d.Id]))
			retained := int(rows[i].Level)
			if effect.MaximumRefund {
				retained = min(retained, max(earned, d.cap(after.Level, next.grow, premium, next.hist.Cleared)))
			}
			if !d.OrdinarySp && (retained < int(saved[previous].Level) || effect.SpDiscount) {
				return nil, storage.ErrInvalid
			}
			for rank := min(int(saved[previous].Level), earned) + 1; rank <= int(saved[previous].Level); rank++ {
				paid := d.cost(rank, prior.hist.Cleared)
				if rank > retained {
					balance += paid
				} else if effect.SpDiscount {
					reduced := d.cost(rank, next.hist.Cleared)
					if reduced > paid {
						return nil, storage.ErrInvalid
					}
					balance += paid - reduced
				}
			}
			if retained == 0 {
				rows = slices.Delete(rows, i, i+1)
			} else {
				rows[i].Level = byte(retained)
			}
		}
		if balance > math.MaxUint16 || balance < 0 {
			return nil, storage.ErrInvalid
		}
		points["Sp"], _ = json.Marshal(balance)
		return rows, nil
	}
	var main skillPage
	if json.Unmarshal(to["Progression"], &main) != nil || main == nil {
		return after, storage.ErrInvalid
	}
	updated, err := apply(from["Skills"], to["Skills"], main, opts.SkillMaster)
	if err != nil {
		return after, err
	}
	to["Skills"], _ = json.Marshal(updated)
	to["Progression"], _ = json.Marshal(main)
	for _, key := range []string{"DuelSkills", "SkillStyles"} {
		var beforePage, afterPage skillPage
		if len(to[key]) == 0 || string(to[key]) == "null" {
			continue
		}
		if json.Unmarshal(from[key], &beforePage) != nil || json.Unmarshal(to[key], &afterPage) != nil || beforePage == nil || afterPage == nil {
			return after, storage.ErrInvalid
		}
		b, a := beforePage, afterPage
		if key == "SkillStyles" {
			b, a = nil, nil
			if json.Unmarshal(beforePage["Other"], &b) != nil || json.Unmarshal(afterPage["Other"], &a) != nil || b == nil || a == nil {
				return after, storage.ErrInvalid
			}
		}
		updated, err = apply(b["Skills"], a["Skills"], a, key == "SkillStyles" && opts.SkillMaster)
		if err != nil {
			return after, err
		}
		a["Skills"], _ = json.Marshal(updated)
		if key == "SkillStyles" {
			afterPage["Other"], _ = json.Marshal(a)
		}
		to[key], _ = json.Marshal(afterPage)
	}
	after.Gameplay, err = json.Marshal(to)
	return after, err
}
