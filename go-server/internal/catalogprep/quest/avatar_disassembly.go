package quest

import (
	"fmt"
	"math"
	"slices"
	"strconv"
	"strings"

	"s9a1/server/internal/catalogprep/gm"
	"s9a1/server/internal/pvf"
	"s9a1/server/internal/storage"
)

type avatarDisassemblyTarget struct {
	ResourceHash string
	Points       int
}
type avatarDisassemblyTier struct {
	Minimum, Maximum int
	Boosters         []uint32
}
type avatarDisassembly struct {
	Targets map[uint32]avatarDisassemblyTarget
	Emblems map[uint32]int
	Tiers   []avatarDisassemblyTier
	Nodes   map[uint32]storage.RewardGraphNode
}
type disassemblyRules struct {
	avatarDisassembly
	grades map[uint32]int
}

func disassemblyPoints(s *itemScript, tag string) map[uint32]int {
	values := s.numbers(s.get(tag))
	out := make(map[uint32]int)
	if len(values) == 0 || len(values)%2 != 0 {
		s.fail("incomplete avatar disassembly points")
		return out
	}
	for row := range slices.Chunk(values, 2) {
		if _, dup := out[row[0]]; dup || row[1] > 100000 {
			s.fail("ambiguous avatar disassembly points")
		}
		out[row[0]] = int(row[1])
	}
	return out
}

func loadAvatarDisassembly(a *pvf.Archive, items ItemLookup, emblems storage.EmblemCatalog) (*disassemblyRules, error) {
	text, err := a.ReadText("etc/avatardisjointinfo_chn.etc", budget)
	if err != nil {
		return nil, err
	}
	s := readItemScript(text)
	out := &disassemblyRules{
		avatarDisassembly: avatarDisassembly{Targets: make(map[uint32]avatarDisassemblyTarget), Emblems: make(map[uint32]int)},
		grades:            disassemblyPoints(s, "avatar grade point"),
	}
	emblemGrades := disassemblyPoints(s, "emblem grade point")
	for id, src := range emblems.Compound.Sources {
		if points, ok := emblemGrades[uint32(src.Grade)]; ok {
			out.Emblems[id] = points
		}
	}
	var roots []uint32
	for _, entry := range stringRE.FindAllStringSubmatch(s.get("result info file list"), -1) {
		data, err := a.ReadText("etc/"+strings.ToLower(entry[1]), budget)
		if err != nil {
			return nil, err
		}
		tier := readItemScript(data)
		r := tier.numbers(tier.get("min max value"))
		boosters := tier.numbers(tier.get("result booster"))
		weight, err := strconv.ParseFloat(tier.get("weight value"), 64)
		if err != nil || tier.err != nil || len(r) != 2 || r[0] > r[1] || r[1] > 100000 || len(boosters) == 0 ||
			math.IsNaN(weight) || math.IsInf(weight, 0) || weight <= 0 {
			return nil, fmt.Errorf("invalid avatar disassembly tier")
		}
		out.Tiers = append(out.Tiers, avatarDisassemblyTier{int(r[0]), int(r[1]), boosters})
		roots = append(roots, boosters...)
	}
	if s.err != nil || len(out.Tiers) == 0 {
		return nil, fmt.Errorf("missing avatar disassembly tier list: %v", s.err)
	}
	ordered := slices.Clone(out.Tiers)
	slices.SortFunc(ordered, func(a, b avatarDisassemblyTier) int { return a.Minimum - b.Minimum })
	for i := 1; i < len(ordered); i++ {
		if ordered[i-1].Maximum+1 != ordered[i].Minimum {
			return nil, fmt.Errorf("avatar disassembly point tiers overlap or have gaps")
		}
	}
	out.Nodes, err = readRewardGraph(items, roots, false)
	if err != nil {
		return nil, err
	}
	return out, nil
}

func (r *disassemblyRules) add(src gm.ItemSource, product *gm.SpecialProduct, grade uint32, base *itemScript) {
	points, ok := r.grades[grade]
	if !ok || product.Template.Kind != 0 {
		return
	}
	s := *base
	disjoint := s.number("disjoint", 1, math.MaxUint32, false)
	cannot := s.number("cannot disjoint", 0, math.MaxUint32, false)
	if s.err == nil && disjoint != 0 && cannot == 0 &&
		!strings.Contains(strings.ToLower(s.get("impossible contents")), "disjoint") {
		r.Targets[src.Id] = avatarDisassemblyTarget{src.Hash, points}
	}
}

func (r *disassemblyRules) trim() {
	if len(r.Targets) > 32768 {
		for _, id := range sortedTemplateIDs(r.Targets)[32768:] {
			delete(r.Targets, id)
		}
	}
}
