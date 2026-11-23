package dungeons

import (
	"cmp"
	"fmt"
	"maps"
	"slices"
	"strings"

	"s9a1/server/internal/catalogprep/commerce"
	"s9a1/server/internal/catalogprep/gm"
)

const independentPolicy = "PVFIndependentDrops/complete-direct-creation-copy-reference;solo-attempts-then-cap-reference;per-hit-list-selection;per-record-stack-coalescing;typed-mon-and-aic-owners;direct-event-items-deferred;flag1-million;flag2-hundred-million;flag5-record-local-base-and-challenge-replacement;flag1-independent;complete-candidate-weights;ordinary-intact-acquisition;death-frozen-lifetime;resource-action-sources;over-denominator-threshold-retained;independent-level-bounds;eligible-replacement;qualified-aic-owner;event-aic-outcomes-deferred;retained-zero-list-weights/v13"

type independentEntry struct {
	OwnerKind                          byte
	Monster, DirectItem                uint32
	Probabilities, AttemptsByPartySize []uint32
	ResultLimit                        uint32
	LevelMin, LevelMax                 int
	Difficulty                         int
	Flag                               byte
	Main                               []weightedDrop
	DungeonConditions                  []uint32
	Replacement                        []weightedDrop
}
type independentOwner struct {
	Owner byte
	Code  uint32
}
type independentGroup struct {
	independentOwner
	Entries []independentEntry
}
type IndependentData struct {
	Hash                                                   string
	Entries                                                []independentGroup
	Templates                                              map[uint32]*gm.ItemTemplate
	AllowedDirectItems, AllowedApcItems, AllowedApcSources []uint32
}

func independentRecords(a Archive, source string, grammar dropGrammar) ([]independentEntry, string, error) {
	r := lexDrops(source)
	r.tag("[independent drop]")
	rows := []independentEntry{}
	var external strings.Builder
	var externalIndex map[int64]string
	var indexHash string
	type list struct {
		rows []weightedDrop
		hash string
	}
	cache := map[int]list{}
	for r.more("[/independent drop]") {
		var h [17]int
		for i := range h {
			h[i] = r.number()
		}
		r.require(len(rows) < 4096 && h[0] >= 0 && h[0] <= 1 && h[1] > 0 && h[2] >= 0 && slices.Min(h[3:15]) >= 0 && h[15] >= -1 && h[15] <= 4, "invalid independent header")
		if grammar == cardIndependent {
			r.require(h[16] == 3 || h[16] == 4, "invalid card flag")
		} else {
			r.require(h[16] == 0 || h[16] == 1 || h[16] == 2 || h[16] == 5, "invalid drop flag")
			if grammar == monsterIndependent {
				r.require(h[13] <= 255 && h[14] <= 255, "invalid level filter")
			}
		}
		e := independentEntry{OwnerKind: byte(h[0]), Monster: uint32(h[1]), DirectItem: uint32(h[2]), Probabilities: make([]uint32, 5), AttemptsByPartySize: make([]uint32, 4), ResultLimit: uint32(h[12]), LevelMin: h[13], LevelMax: h[14], Difficulty: h[15], Flag: byte(h[16]), Main: []weightedDrop{}, Replacement: []weightedDrop{}, DungeonConditions: []uint32{}}
		for i := range e.Probabilities {
			e.Probabilities[i] = uint32(h[3+i])
		}
		for i := range e.AttemptsByPartySize {
			e.AttemptsByPartySize[i] = uint32(h[8+i])
		}
		if e.Flag == 1 || e.Flag == 5 || e.Flag == 4 {
			r.tag("[list]")
			e.Main = r.pairs("[/list]", grammar)
		}
		if e.Flag == 2 && r.peek() == "[list]" {
			r.tag("[list]")
			count := 0
			for r.more("[/list]") {
				id := r.number()
				count++
				r.require(id >= 0 && count <= 256, "invalid external list reference")
				if grammar == worldIndependent || r.err != nil {
					continue
				}
				v, ok := cache[id]
				if !ok {
					if externalIndex == nil {
						idx, ok := a.(interface {
							ReadIndependentDropIndex() (map[int64]string, error)
						})
						if !ok {
							return nil, "", fmt.Errorf("external drop index is unavailable")
						}
						var err error
						externalIndex, err = idx.ReadIndependentDropIndex()
						if err != nil {
							return nil, "", err
						}
						indexHash, err = rawHash(a, "etc/independentdrop.lst")
						if err != nil {
							return nil, "", err
						}
					}
					p := externalIndex[int64(id)]
					if p == "" {
						return nil, "", fmt.Errorf("external drop reference %d is missing", id)
					}
					t, err := a.ReadText(p, budget)
					if err != nil {
						return nil, "", err
					}
					digest, err := rawHash(a, p)
					if err != nil {
						return nil, "", err
					}
					tokens := lexDrops(t)
					tokens.tag("[list]")
					v.rows = tokens.pairs("[/list]", cardIndependent)
					tokens.require(tokens.peek() == "", "trailing external pool")
					if tokens.err != nil {
						return nil, "", tokens.err
					}
					v.hash = hashText(fmt.Sprintf("IndependentExternalSource/v1\n%s\n%d|%s|%s", indexHash, id, p, digest)) + "|" + hashText(t)
					cache[id] = v
				}
				r.require(len(e.Main)+len(v.rows) <= 4096, "expanded external pool exceeds 4096")
				e.Main = append(e.Main, v.rows...)
				fmt.Fprintf(&external, "%d|%s\n", id, v.hash)
			}
			r.tag("[/list]")
			r.require(count > 0, "empty external references")
		}
		if e.Flag == 5 {
			r.tag("[replace list]")
			r.tag("[dungeon condition]")
			n := r.number()
			r.require(n >= 1 && n <= 256, "invalid replacement condition count")
			for j := 0; j < n && r.err == nil; j++ {
				id := r.number()
				r.require(id > 0, "invalid replacement dungeon")
				e.DungeonConditions = append(e.DungeonConditions, uint32(id))
			}
			slices.Sort(e.DungeonConditions)
			e.DungeonConditions = slices.Compact(e.DungeonConditions)
			r.require(r.number() == 1 && r.number() == 2, "unexpected replacement condition")
			e.Replacement = r.pairs("[/replace list]", grammar)
		}
		if e.Flag == 3 || e.Flag == 4 {
			r.require(e.Flag == 3 && e.DirectItem > 0 || e.Flag == 4 && e.DirectItem == 0, "invalid card direct/list source")
		}
		if grammar == monsterIndependent && e.Flag == 2 {
			r.require(len(e.Main) > 0 || e.DirectItem != 0, "external row has no item")
		}
		rows = append(rows, e)
	}
	r.tag("[/independent drop]")
	r.require(r.n == len(r.values), "trailing independent entries")
	hash := hashText(source)
	if external.Len() != 0 {
		hash = hashText("IndependentExternalLists/v1;native-reference-id-expansion;ordered-repeated-references;retained-weights\n" + hash + "\n" + external.String())
	}
	return rows, hash, r.err
}

func BuildIndependent(a Archive, items *commerce.Items, codes map[uint32]bool) (IndependentData, error) {
	var out IndependentData
	text, err := a.ReadText("etc/independent_drop.etc", budget)
	if err != nil {
		return out, err
	}
	rows, resource, err := independentRecords(a, text, monsterIndependent)
	if err != nil {
		return out, err
	}
	equip, err := a.ReadIndex("equipment")
	if err != nil {
		return out, err
	}
	stack, err := a.ReadIndex("stackable")
	if err != nil {
		return out, err
	}
	aics, err := a.ReadIndex("aicharacter")
	if err != nil {
		return out, err
	}
	allowed := func(id uint32) bool {
		path := stack[int64(id)]
		if path == "" {
			path = equip[int64(id)]
		}
		return path != "" && !eventOwned(path, false)
	}
	ids, direct, apc, sources, other := map[uint32]bool{}, map[uint32]bool{}, map[uint32]bool{}, map[uint32]bool{}, map[uint32]bool{}
	groups := map[independentOwner][]independentEntry{}
	for _, r := range rows {
		pools := slices.Concat(r.Main, r.Replacement)
		if r.Flag == 0 && r.DirectItem > 0 && r.DirectItem != 3051 && r.DirectItem != 3052 && allowed(r.DirectItem) {
			direct[r.DirectItem] = true
		}
		if r.Flag == 0 || r.Flag == 2 && len(r.Main) == 0 {
			if r.DirectItem > 0 {
				ids[r.DirectItem] = true
			}
		}
		if r.Flag == 2 && len(r.Main) == 0 {
			other[r.DirectItem] = true
		}
		for _, c := range pools {
			if c.Weight > 0 {
				ids[c.Item] = true
			}
			if r.Flag != 0 {
				other[c.Item] = true
			}
		}
		if r.OwnerKind == 1 {
			if p := aics[int64(r.Monster)]; p != "" && !eventOwned(p, false) {
				sources[r.Monster] = true
			}
			if r.DirectItem > 0 && allowed(r.DirectItem) {
				apc[r.DirectItem] = true
			}
			for _, c := range pools {
				if c.Item > 0 && allowed(c.Item) {
					apc[c.Item] = true
				}
			}
		}
		key := independentOwner{r.OwnerKind, r.Monster}
		if codes == nil || codes[r.Monster] {
			groups[key] = append(groups[key], r)
		}
	}
	out.Templates = map[uint32]*gm.ItemTemplate{}
	for id := range ids {
		if direct[id] || other[id] {
			if t := monsterDropTemplate(items, id); t != nil {
				out.Templates[id] = t
			}
		}
	}
	out.AllowedDirectItems = slices.Sorted(maps.Keys(direct))
	out.AllowedApcItems = slices.Sorted(maps.Keys(apc))
	out.AllowedApcSources = slices.Sorted(maps.Keys(sources))
	out.Entries = make([]independentGroup, 0, len(groups))
	for k, v := range groups {
		out.Entries = append(out.Entries, independentGroup{k, v})
	}
	slices.SortFunc(out.Entries, func(a, b independentGroup) int {
		return cmp.Or(cmp.Compare(a.Owner, b.Owner), cmp.Compare(a.Code, b.Code))
	})
	slices.SortStableFunc(rows, func(a, b independentEntry) int {
		return cmp.Or(cmp.Compare(a.Monster, b.Monster), cmp.Compare(a.Flag, b.Flag))
	})
	out.Hash, err = catalogHash(independentPolicy, "\n", resource, "\n", rows, "\n", orderedPairs(out.Templates), "\n", out.AllowedDirectItems, "\nAIC-items:", out.AllowedApcItems, "\nAIC-sources:", out.AllowedApcSources)
	return out, err
}
