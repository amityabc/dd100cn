package dungeons

import (
	"fmt"
	"maps"
	"slices"
	"strconv"
	"strings"

	"s9a1/server/internal/catalogprep/commerce"
	"s9a1/server/internal/catalogprep/gm"
)

const independentCardPolicy = "IndependentDropCard/all-records;monster-owner-only;flag3-direct;flag4-weighted-list;million-denominator;independent-level-bounds;solo-attempts-then-cap-reference;per-hit-list-selection;per-record-stack-coalescing;over-denominator-threshold-retained;complete-template-acquisition;retained-zero-list-weights/v5"
const areaCardPolicy = "AreaMonsterCardGrouping/scoped-world-dungeon-lists;preserve-scope-duplicates;million-trigger/area-weighted-card/v2"

// C#'s default JSON options serialize ValueTuple fields as {} while generating
// these identities. The exported selection table retains each real weight.
type areaGroup struct {
	Scope                  string
	Index                  uint32
	BaseDropRatio          int
	Items                  []struct{}
	ExcludedDungeonIndexes []uint32
}

func prepareDropCards(items *commerce.Items, ids map[uint32]bool) (map[uint32]*gm.ItemTemplate, error) {
	templates := map[uint32]*gm.ItemTemplate{}
	for id := range ids {
		t, err := items.Prepared(id)
		if err != nil || t == nil || t.Kind != 2 || !t.MonsterCard {
			return nil, fmt.Errorf("monster card %d lacks a supported template", id)
		}
		templates[id] = t
	}
	return templates, nil
}
func areaDropCards(a Archive, items *commerce.Items) (string, []uint32, error) {
	text, err := a.ReadText("etc/areamonstercardgrouping.etc", budget)
	if err != nil {
		return "", nil, err
	}
	r := lexDrops(text)
	r.tag("[monster card group]")
	groups := []areaGroup{}
	ids := map[uint32]bool{}
	for _, scope := range []string{"world", "dungeon"} {
		plural := "[" + scope + "s]"
		close := "[/" + scope + "s]"
		r.tag(plural)
		seen := map[uint32]bool{}
		for r.more(close) {
			g := areaGroup{Scope: scope, BaseDropRatio: -1, ExcludedDungeonIndexes: []uint32{}}
			r.tag("[" + scope + "]")
			end := "[/" + scope + "]"
			for r.more(end) {
				tag := r.peek()
				r.tag(tag)
				switch tag {
				case "[index]":
					n := r.number()
					r.require(n > 0, "invalid card grouping index")
					g.Index = uint32(n)
				case "[base drop ratio]":
					g.BaseDropRatio = r.number()
					r.require(g.BaseDropRatio >= 0 && g.BaseDropRatio <= 1_000_000, "invalid card grouping base ratio")
				case "[card item list]":
					pairs := r.pairs("[/card item list]", cardIndependent)
					g.Items = make([]struct{}, len(pairs))
					for _, p := range pairs {
						r.require(p.Weight > 0, "zero area card weight")
						ids[p.Item] = true
					}
				case "[exclude dungeon index]":
					g.ExcludedDungeonIndexes = []uint32{}
					for r.more("[/exclude dungeon index]") {
						n := r.number()
						r.require(n > 0 && len(g.ExcludedDungeonIndexes) < 4096, "invalid card dungeon exclusion")
						g.ExcludedDungeonIndexes = append(g.ExcludedDungeonIndexes, uint32(n))
					}
					r.tag("[/exclude dungeon index]")
				default:
					r.require(false, "unknown area card grouping section")
				}
			}
			r.tag(end)
			r.require(len(groups) < 4096 && g.Index > 0 && !seen[g.Index] && g.BaseDropRatio >= 0 && len(g.Items) > 0, "incomplete area card group")
			seen[g.Index] = true
			groups = append(groups, g)
		}
		r.tag(close)
	}
	r.tag("[/monster card group]")
	r.require(r.n == len(r.values), "trailing area card data")
	if r.err != nil {
		return "", nil, r.err
	}
	templates, err := prepareDropCards(items, ids)
	if err != nil {
		return "", nil, err
	}
	hash, err := catalogHash(areaCardPolicy, "\n", text, "\n", groups, "\n", orderedPairs(templates))
	return hash, slices.Sorted(maps.Keys(ids)), err
}

type nativeIndependentCard struct {
	OwnerKind                          byte
	Monster, Item                      uint32
	Probabilities, AttemptsByPartySize []uint32
	ResultLimit                        uint32
	LevelMin, LevelMax, Difficulty     int
	Flag                               byte
	List                               []struct{}
}
type cardDropRow struct {
	OwnerKind                          byte
	DirectItem                         uint32
	Probabilities, AttemptsByPartySize []uint32
	ResultLimit                        uint32
	LevelMin, LevelMax, Difficulty     int
	Flag                               byte
	Main                               []weightedDrop
}
type independentCardGroup struct {
	Code    uint32
	Entries []cardDropRow
}
type IndependentCards struct {
	Hash      string
	Entries   []independentCardGroup
	Templates map[uint32]*gm.ItemTemplate
}

func buildIndependentCards(a Archive, items *commerce.Items, codes map[uint32]bool, area []uint32) (IndependentCards, error) {
	var out IndependentCards
	text, err := a.ReadText("etc/independent_drop_card.etc", budget)
	if err != nil {
		return out, err
	}
	data, hash, err := independentRecords(a, text, cardIndependent)
	if err != nil {
		return out, err
	}
	native := map[uint32][]nativeIndependentCard{}
	projected := map[uint32][]cardDropRow{}
	ids := map[uint32]bool{}
	for _, r := range data {
		if r.Flag == 4 {
			for _, p := range r.Main {
				if p.Weight > 0 {
					ids[p.Item] = true
				}
			}
		} else {
			ids[r.DirectItem] = true
		}
		native[r.Monster] = append(native[r.Monster], nativeIndependentCard{r.OwnerKind, r.Monster, r.DirectItem, r.Probabilities, r.AttemptsByPartySize, r.ResultLimit, r.LevelMin, r.LevelMax, r.Difficulty, r.Flag, make([]struct{}, len(r.Main))})
		if codes == nil || codes[r.Monster] {
			projected[r.Monster] = append(projected[r.Monster], cardDropRow{r.OwnerKind, r.DirectItem, r.Probabilities, r.AttemptsByPartySize, r.ResultLimit, r.LevelMin, r.LevelMax, r.Difficulty, r.Flag, r.Main})
		}
	}
	out.Entries = make([]independentCardGroup, 0, len(projected))
	for _, n := range slices.Sorted(maps.Keys(projected)) {
		out.Entries = append(out.Entries, independentCardGroup{n, projected[n]})
	}
	out.Templates, err = prepareDropCards(items, ids)
	if err != nil {
		return out, err
	}
	areaIDs := make([]string, len(area))
	for i, id := range area {
		areaIDs[i] = strconv.FormatUint(uint64(id), 10)
	}
	out.Hash, err = catalogHash(independentCardPolicy, "\n", hash, "\n", orderedPairs(native), "\ntemplates:", orderedPairs(out.Templates), "\narea:", strings.Join(areaIDs, ","))
	return out, err
}
