package opening

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"math/big"
	"sort"

	"s9a1/server/internal/protocol"
	"s9a1/server/internal/storage"
)

type CombatCatalog struct {
	ExperienceHash string
	Levels         []struct {
		Threshold int64
		Sp, Tp    uint16
	}
	Jobs         map[byte]JobCombat
	ClassTickets map[uint32]struct {
		Template     json.RawMessage
		MaximumLevel byte
	}
	Replacement *struct {
		MinimumLevel, MaximumLevel byte
		Gold                       []uint32
	}
}

type growthCurve struct {
	Branch, Stage int
	Values        map[string]json.Number
}
type AdvancementSkill struct {
	ID                              uint16 `json:"Id"`
	Level, RequiredLevel, Awakening byte
	Active                          bool
}
type JobCombat struct {
	Initial map[string]json.Number
	Growth  []growthCurve
	Grants  map[byte][]AdvancementSkill
	Awards  []struct {
		Level, Type   byte
		ByPlayerLevel []uint32
	}
	ExperienceMultiplier json.Number
	DisableExperience    bool
}

func (j JobCombat) award(m Monster, player byte) (uint32, error) {
	if j.DisableExperience || m.AiScriptIndex != nil {
		return 0, nil
	}
	for _, a := range j.Awards {
		if a.Level == m.Level && a.Type == m.Type && player > 0 && int(player) <= len(a.ByPlayerLevel) {
			return a.ByPlayerLevel[player-1], nil
		}
	}
	return 0, errors.New("missing resource-backed tutorial monster experience")
}

// The original growth table adds decimal deltas per level and truncates only
// after multiplying the final sum by ten. Keep saved non-growth stats intact.
func (j JobCombat) growth(level, grow byte, source json.RawMessage) (json.RawMessage, error) {
	var s map[string]json.RawMessage
	if err := json.Unmarshal(source, &s); err != nil {
		return nil, err
	}
	var values map[string]int64
	if err := json.Unmarshal(s["Values"], &values); err != nil {
		return nil, err
	}
	base := map[[2]int]map[string]json.Number{}
	for _, c := range j.Growth {
		base[[2]int{c.Branch, c.Stage}] = c.Values
	}
	branch, stage := int(grow&15)+1, int(grow>>4)
	if base[[2]int{1, 0}] == nil || base[[2]int{branch, 0}] == nil {
		return nil, errors.New("missing character growth branch")
	}
	ranges := [...]int{15, 50, 75, 100}
	fields := []string{"hp_max", "mp_max", "strength", "vitality", "intelligence", "spirit", "fire_resistance", "water_resistance", "dark_resistance", "light_resistance", "inventory_limit", "hp_regen_speed", "mp_regen_speed", "move_speed", "attack_speed", "cast_speed", "hit_recovery", "jump_power", "weight"}
	for _, k := range fields {
		total := new(big.Rat)
		if n := j.Initial[k]; n != "" {
			if _, ok := total.SetString(string(n)); !ok {
				return nil, errors.New("invalid initial growth decimal")
			}
		}
		for to := 2; to <= int(level); to++ {
			active := 1
			if to > ranges[0] {
				active = branch
			}
			delta := base[[2]int{1, 0}][k]
			if own, ok := base[[2]int{active, 0}][k]; ok {
				delta = own
			}
			for st := 1; st <= stage; st++ {
				if own, ok := base[[2]int{active, st}][k]; ok && to > ranges[st] {
					delta = own
				}
			}
			if delta == "" {
				continue
			}
			d, ok := new(big.Rat).SetString(string(delta))
			if !ok {
				return nil, errors.New("invalid growth decimal")
			}
			total.Add(total, d)
		}
		total.Mul(total, big.NewRat(10, 1))
		v := new(big.Int).Quo(total.Num(), total.Denom())
		if !v.IsInt64() {
			return nil, fmt.Errorf("growth stat %s overflows", k)
		}
		values[k] = v.Int64()
	}
	values["hp_max"] += 9800
	values["mp_max"] += 10500
	if _, err := protocol.Stats(values); err != nil {
		return nil, err
	}
	s["Values"], _ = json.Marshal(values)
	return json.Marshal(s)
}

type progress struct {
	Experience uint64
	Sp, Tp     uint16
	GrowType   byte
	Hp         uint32
}
type LearnedSkill struct {
	Id    uint16
	Level byte
	Slot  uint16
}
type object map[string]json.RawMessage

func (j JobCombat) advanceSkills(obj object, level, grow byte) error {
	var learned []LearnedSkill
	if err := json.Unmarshal(obj["Skills"], &learned); err != nil {
		return err
	}
	table, ok := j.Grants[grow&15]
	if !ok {
		return errors.New("missing skill grants for character branch")
	}
	result, err := AdvanceLearnedSkills(learned, table, level, grow)
	if err != nil {
		return err
	}
	obj["Skills"], err = json.Marshal(result)
	return err
}

// AdvanceLearnedSkills applies profession grants to a newly created or saved skill page.
func AdvanceLearnedSkills(current []LearnedSkill, table []AdvancementSkill, level, grow byte) ([]LearnedSkill, error) {
	learned := append([]LearnedSkill{}, current...)
	eligible := map[uint16]AdvancementSkill{}
	for _, gr := range table {
		if gr.ID == 0 || gr.Level == 0 {
			return nil, errors.New("invalid advancement skill")
		}
		if gr.RequiredLevel <= level && gr.Awakening <= grow>>4 && gr.Level > eligible[gr.ID].Level {
			eligible[gr.ID] = gr
		}
	}
	index := map[uint16]int{}
	slots := map[uint16]bool{}
	for i, s := range learned {
		_, dupe := index[s.Id]
		if s.Id == 0 || s.Level == 0 || s.Slot > 254 || dupe || slots[s.Slot] {
			return nil, errors.New("invalid saved skill page")
		}
		index[s.Id] = i
		slots[s.Slot] = true
	}
	keys := make([]int, 0, len(eligible))
	for id := range eligible {
		keys = append(keys, int(id))
	}
	sort.Ints(keys)
	for _, id := range keys {
		gr := eligible[uint16(id)]
		if i, ok := index[gr.ID]; ok {
			if gr.Level > learned[i].Level {
				learned[i].Level = gr.Level
			}
			continue
		}
		var slot uint16 = 102
		if gr.Active {
			for i := uint16(0); i < 12; i++ {
				if !slots[i] {
					slot = i
					break
				}
			}
		}
		for slot <= 254 && slots[slot] {
			slot++
		}
		if slot > 254 {
			return nil, errors.New("native learned skill slots are full")
		}
		slots[slot] = true
		learned = append(learned, LearnedSkill{gr.ID, gr.Level, slot})
	}
	sort.Slice(learned, func(i, j int) bool { return learned[i].Id < learned[j].Id })
	return learned, nil
}

func (cat *CombatCatalog) apply(c storage.Character, award uint32) (next storage.Character, p progress, actual uint32, err error) {
	return cat.applyGrowth(c, award, 0, nil, false, false)
}

func (cat *CombatCatalog) applyGrowth(c storage.Character, award uint32, bonusTP uint16, questGrow *byte, forceSkills, advanceBasic bool) (next storage.Character, p progress, actual uint32, err error) {
	var profile, progression object
	if err = json.Unmarshal(c.Gameplay, &profile); err != nil {
		return
	}
	if err = json.Unmarshal(profile["Progression"], &progression); err != nil {
		return
	}
	if err = json.Unmarshal(profile["Progression"], &p); err != nil {
		return
	}
	job, ok := cat.Jobs[c.Job]
	if !ok || c.Level < 1 || c.Level > 100 || len(cat.Levels) != 100 || p.GrowType > 0x3f || p.GrowType&15 > 5 || questGrow == nil && p.Hp == 0 {
		err = errors.New("invalid saved tutorial progression")
		return
	}
	before := p
	if questGrow != nil {
		p.GrowType = *questGrow
		if p.GrowType > 0x35 || p.GrowType&15 > 5 || uint32(p.Tp)+uint32(bonusTP) > math.MaxUint16 {
			err = storage.ErrInvalid
			return
		}
		p.Tp += bonusTP
		if before.GrowType != p.GrowType {
			progression["GrowType"], _ = json.Marshal(p.GrowType)
		}
	}
	xp := min(uint64(award), math.MaxUint64-p.Experience)
	actual = uint32(xp)
	p.Experience += xp
	next = c
	for next.Level < 100 {
		th := cat.Levels[next.Level-1].Threshold
		if th < 0 {
			err = errors.New("negative experience threshold")
			return
		}
		if p.Experience < uint64(th) {
			break
		}
		next.Level++
		points := cat.Levels[next.Level-1]
		if uint32(p.Sp)+uint32(points.Sp) > math.MaxUint16 || uint32(p.Tp)+uint32(points.Tp) > math.MaxUint16 {
			err = errors.New("level-up points overflow")
			return
		}
		p.Sp += points.Sp
		p.Tp += points.Tp
	}
	progression["Experience"], _ = json.Marshal(p.Experience)
	awakened := before.GrowType != p.GrowType
	advance := forceSkills || awakened || (next.Level != c.Level && (p.GrowType&15 != 0 || advanceBasic))
	if next.Level != c.Level || awakened || forceSkills {
		next.Stats, err = job.growth(next.Level, p.GrowType, c.Stats)
		if err != nil {
			return
		}
	}
	if advance || next.Level != c.Level || p.Sp != before.Sp || p.Tp != before.Tp {
		progression["Sp"], _ = json.Marshal(p.Sp)
		progression["Tp"], _ = json.Marshal(p.Tp)
		if advance {
			if err = job.advanceSkills(profile, next.Level, p.GrowType); err != nil {
				return
			}
		}
		for _, other := range []string{"DuelSkills", "SkillStyles"} {
			if len(profile[other]) == 0 || string(profile[other]) == "null" {
				continue
			}
			var owner object
			if err = json.Unmarshal(profile[other], &owner); err != nil {
				return
			}
			page := owner
			if other == "SkillStyles" {
				page = nil
				if err = json.Unmarshal(owner["Other"], &page); err != nil {
					return
				}
			}
			var balance struct{ Sp, Tp uint16 }
			b, _ := json.Marshal(page)
			if err = json.Unmarshal(b, &balance); err != nil {
				return
			}
			sp := uint32(balance.Sp) + uint32(p.Sp-before.Sp)
			tp := uint32(balance.Tp) + uint32(p.Tp-before.Tp)
			if sp > math.MaxUint16 || tp > math.MaxUint16 {
				err = errors.New("secondary skill page points overflow")
				return
			}
			page["Sp"], _ = json.Marshal(sp)
			page["Tp"], _ = json.Marshal(tp)
			if advance {
				if err = job.advanceSkills(page, next.Level, p.GrowType); err != nil {
					return
				}
			}
			if other == "SkillStyles" {
				owner["Other"], _ = json.Marshal(page)
			}
			profile[other], _ = json.Marshal(owner)
		}
	}
	profile["Progression"], _ = json.Marshal(progression)
	next.Gameplay, err = json.Marshal(profile)
	return
}
