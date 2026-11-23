package catalogprep

import (
	"fmt"
	"regexp"
	"slices"
	"strings"

	"s9a1/server/internal/catalogprep/pvftext"
)

type SkillRequirement struct {
	Id    uint16
	Level byte
}
type SkillDiscount struct {
	QuestId uint16
	Amounts []int
}
type SkillDefinition struct {
	Id                                                                 uint16
	Active                                                             bool
	RequiredLevel, Interval                                            int
	Maximum, GrowMaximum, Fitness, SecondFitness, SecondMaximum, Costs []int
	Prerequisites                                                      []SkillRequirement
	FreeLevel                                                          byte
	SourceHash                                                         string
	OrdinarySp                                                         bool
	TpCosts                                                            []int
	GuildSkill, UsesTp, Purchasable                                    bool
	QuestDiscount                                                      *SkillDiscount
	ThirdAwakeningRequirements                                         map[int]bool
}

var fixedSkill = regexp.MustCompile(`(?im)^[ \t]*\[fixed level skill\]`)
var guildSkill = regexp.MustCompile(`(?im)^[ \t]*\[guild skill\]`)
var requiredSkillLevel = regexp.MustCompile(`(?im)^[ \t]*\[required level\]`)

type skillFields struct {
	text string
	err  error
}

func (f *skillFields) section(tag string) string {
	v, e := pvftext.Section(f.text, tag)
	if f.err == nil {
		f.err = e
	}
	return v
}
func (f *skillFields) numbers(tag string) []int {
	v, e := pvftext.Integers(f.section(tag))
	if f.err == nil {
		f.err = e
	}
	return v
}
func (f *skillFields) rows(tag string, matching bool) []int {
	out := []int{}
	for i, b := range pvftext.Blocks(f.text, tag) {
		n, e := pvftext.Integers(b)
		if f.err == nil {
			f.err = e
		}
		if matching && i > 0 {
			if !slices.Equal(out, n) && f.err == nil {
				f.err = fmt.Errorf("conflicting %s", tag)
			}
			continue
		}
		out = append(out, n...)
	}
	return out
}

func parseSkill(id uint16, text, hash string, free byte, advancement bool) (SkillDefinition, error) {
	f := skillFields{text: text}
	var z SkillDefinition
	tag := func(k string) string { return strings.ToLower(strings.Trim(f.section(k), " \t\n\r`[]")) }
	one, two := tag("type"), tag("skill type")
	if one == "" {
		one = two
	}
	if one != "active" && one != "passive" || two != "" && two != one {
		return z, fmt.Errorf("unsupported skill type")
	}
	fixed := fixedSkill.MatchString(text)
	special := f.numbers("special purchase cost")
	features := f.rows("feature skill type", true)
	if len(pvftext.Blocks(text, "feature skill type")) > 1 && len(features) != 1 {
		return z, fmt.Errorf("repeated feature skill type")
	}
	ordinary := !fixed && len(features) == 0 && !slices.ContainsFunc(special, func(n int) bool { return n != 0 })
	tp := !fixed && slices.Equal(features, []int{1}) && len(special) > 0
	z = SkillDefinition{Id: id, Active: one == "active", FreeLevel: free, SourceHash: hash, OrdinarySp: ordinary, UsesTp: tp, Purchasable: ordinary || tp,
		GuildSkill: guildSkill.MatchString(text), Maximum: f.numbers("maximum level"), GrowMaximum: f.numbers("growtype maximum level"),
		Fitness: f.rows("skill fitness growtype", false), SecondFitness: f.rows("skill fitness second growtype", false), Costs: f.numbers("purchase cost"),
		Prerequisites: []SkillRequirement{}, TpCosts: []int{}, SecondMaximum: []int{}, ThirdAwakeningRequirements: map[int]bool{}}
	if !ordinary && len(z.Costs) == 0 {
		z.Costs = []int{0}
	}
	if !ordinary && !tp && len(z.Maximum) == 0 {
		z.Maximum = []int{0}
	}
	if tp {
		z.TpCosts = special
	}
	required := f.numbers("required level")
	interval := f.numbers("required level range")
	prereq := f.numbers("pre required skill")
	if len(required) > 1 || len(interval) > 1 || len(prereq)%2 != 0 {
		return z, fmt.Errorf("invalid skill requirements")
	}
	if len(required) == 1 {
		z.RequiredLevel = required[0]
	} else if !ordinary && !tp || (free > 0 || advancement) && !requiredSkillLevel.MatchString(text) {
		z.RequiredLevel = 1
	}
	if len(interval) == 1 {
		z.Interval = interval[0]
	}
	for i := 0; i < len(prereq); i += 2 {
		n, lv := prereq[i], prereq[i+1]
		if n <= 0 || n > 65535 || lv <= 0 || lv > 255 {
			return z, fmt.Errorf("invalid skill prerequisite")
		}
		z.Prerequisites = append(z.Prerequisites, SkillRequirement{uint16(n), byte(lv)})
	}
	modern, legacy := f.numbers("awakening maximum level"), f.numbers("second growtype maximum level")
	if len(modern) > 0 && len(modern) != 18 || len(legacy) > 0 && len(legacy) != 12 {
		return z, fmt.Errorf("invalid awakening maxima")
	}
	if len(modern)+len(legacy) > 0 {
		for branch := range 6 {
			for stage := range 3 {
				old := 0
				if stage < 2 && len(legacy) > 0 {
					old = legacy[2*branch+stage]
				}
				n := old
				if len(modern) > 0 {
					n = modern[stage*6+branch]
					if len(legacy) > 0 && stage < 2 && old != n {
						return z, fmt.Errorf("conflicting awakening maxima")
					}
				}
				z.SecondMaximum = append(z.SecondMaximum, n)
			}
		}
	}
	discount := f.rows("sp discount quest", true)
	if len(discount) > 0 {
		if len(discount) < 2 || discount[0] <= 0 || discount[0] >= 30000 || slices.ContainsFunc(discount[1:], func(n int) bool { return n < 0 }) {
			return z, fmt.Errorf("invalid skill quest discount")
		}
		z.QuestDiscount = &SkillDiscount{uint16(discount[0]), discount[1:]}
	}
	third := f.numbers("enable by third awakening quest")
	if len(third)%2 != 0 {
		return z, fmt.Errorf("incomplete third awakening requirement")
	}
	for i := 0; i < len(third); i += 2 {
		phase, v := third[i], third[i+1]
		if _, dup := z.ThirdAwakeningRequirements[phase]; dup || phase < 1 || phase > 2 || v < 0 || v > 1 {
			return z, fmt.Errorf("invalid third awakening requirement")
		}
		z.ThirdAwakeningRequirements[phase] = v == 1
	}
	if f.err != nil {
		return z, f.err
	}
	if z.Id == 0 || z.RequiredLevel < 1 || z.Interval < 0 || len(z.Maximum) == 0 || len(z.Costs) == 0 {
		return z, fmt.Errorf("incomplete skill definition")
	}
	for _, values := range [][]int{z.Maximum, z.GrowMaximum, z.SecondMaximum, z.Costs, z.TpCosts} {
		if slices.ContainsFunc(values, func(n int) bool { return n < 0 }) {
			return z, fmt.Errorf("negative skill bound or cost")
		}
	}
	if tp && (slices.ContainsFunc(z.Costs, func(n int) bool { return n != 0 }) || z.QuestDiscount != nil) {
		return z, fmt.Errorf("conflicting TP and SP costs")
	}
	if v, one := z.ThirdAwakeningRequirements[1]; one {
		second, both := z.ThirdAwakeningRequirements[2]
		if !v || !both || second {
			return z, fmt.Errorf("unsupported third awakening requirements")
		}
	}
	return z, nil
}
