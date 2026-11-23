package dungeons

import (
	"cmp"
	"fmt"
	"maps"
	"slices"
	"strings"

	"s9a1/server/internal/catalogprep/commerce"
	"s9a1/server/internal/catalogprep/gm"
	"s9a1/server/internal/dungeon"
)

type secondaryDropPool struct {
	Kind, Level     byte
	Rarity, Channel int
	Ids             []uint32
}

type Drops struct {
	Hash, MonsterHash   string
	Native              GroundRules
	Gold                map[byte]groundGold
	Monsters            map[uint32]monsterPools
	Templates           map[uint32]*gm.ItemTemplate
	Weights             map[uint32]int64
	Candidates          map[uint32]NativeDropCandidate
	MainPools           []mainDropPool
	SecondaryPools      []secondaryDropPool
	Secondary           []SecondaryDropExport
	EquipmentGeneration *dungeon.EquipmentGeneration
	FirstHunt           *firstHuntData
	Independent         IndependentData
	IndependentCards    IndependentCards
	PvfRewards          *CompletionDrops
}

// BuildDrops composes the frozen ground-drop sources. The original hash chain
// includes all secondary resource identities, including the ones published by
// separate runtime owners.
func BuildDrops(a Archive, items *commerce.Items, defs []Definition, candidates []NativeDropCandidate, gen *dungeon.EquipmentGeneration, luck Luck) (Drops, error) {
	var out Drops
	if items == nil || gen == nil || len(gen.Hash) != 64 || len(luck.Hash) != 64 {
		return out, fmt.Errorf("ground drop preparation requires matching items, luck and equipment generation")
	}
	candidates = slices.Clone(candidates)
	slices.SortFunc(candidates, func(a, b NativeDropCandidate) int { return cmp.Compare(a.Id, b.Id) })
	for i, c := range candidates {
		if c.Id == 0 || c.Rarity < 0 || c.Rarity > 7 || i > 0 && candidates[i-1].Id == c.Id {
			return out, fmt.Errorf("invalid shared ground drop candidate %d", c.Id)
		}
	}
	main, err := buildMainGround(a, candidates, defs)
	if err != nil {
		return out, err
	}
	out.Native, out.Gold, out.MainPools = main.Native, main.Gold, main.MainPools
	sum := main.Hash
	appendHash := func(parts ...string) { sum = hashText(sum + "\n" + strings.Join(parts, "\n")) }
	appendHash(main.Native.Hash)
	areaHash, cardArea, err := areaDropCards(a, items)
	if err != nil {
		return out, err
	}
	appendHash(areaHash)
	out.Independent, err = BuildIndependent(a, items, main.codes)
	if err != nil {
		return out, err
	}
	appendHash(out.Independent.Hash)
	var completionIDs []uint32
	out.PvfRewards, completionIDs, err = completionDrops(a, items, main.layouts)
	if err != nil {
		return out, err
	}
	if out.PvfRewards != nil {
		appendHash(out.PvfRewards.Hash)
	}
	out.IndependentCards, err = buildIndependentCards(a, items, main.codes, cardArea)
	if err != nil {
		return out, err
	}
	appendHash(out.IndependentCards.Hash)
	secondary, err := BuildSecondarySources(a)
	if err != nil {
		return out, err
	}
	hashes := make([]string, 0, len(secondary))
	for _, row := range secondary {
		hashes = append(hashes, row.Hash)
	}
	appendHash(hashes...)
	out.Secondary = SecondaryExport(secondary)
	out.SecondaryPools = secondaryDropPools(secondary, candidates)
	monsters, err := BuildMonsterDrops(a, items, main.codes)
	if err != nil {
		return out, err
	}
	out.Monsters, out.Templates, out.MonsterHash = monsters.Monsters, monsters.Templates, monsters.Hash
	appendHash(monsters.Hash)
	appendHash(groundLuckPolicy, luck.Hash)
	out.EquipmentGeneration = gen
	appendHash(gen.Hash)
	out.FirstHunt = makeFirstHunt(main.sources, monsters.Hash, main.Native.Auxiliary.FirstBossNamedMobHunting)
	if out.FirstHunt != nil {
		appendHash(out.FirstHunt.Hash)
	}
	out.Hash = sum
	used := make(map[uint32]bool)
	for _, p := range out.MainPools {
		for _, id := range p.Ids {
			used[id] = true
		}
	}
	for _, p := range out.SecondaryPools {
		for _, id := range p.Ids {
			used[id] = true
		}
	}
	weights := make(map[uint32]bool)
	out.Candidates = make(map[uint32]NativeDropCandidate, len(used))
	for _, c := range candidates {
		if used[c.Id] {
			out.Candidates[c.Id] = c
			if c.Template != nil {
				weights[c.Id] = true
			}
		}
	}
	for _, templates := range []map[uint32]*gm.ItemTemplate{out.Templates, out.Independent.Templates, out.IndependentCards.Templates} {
		for id := range templates {
			weights[id] = true
		}
	}
	for _, id := range completionIDs {
		weights[id] = true
	}
	out.Weights, err = groundCarryWeights(a, items, weights)
	return out, err
}

func completionDrops(a Archive, items *commerce.Items, defs []Definition) (*CompletionDrops, []uint32, error) {
	frozen := make([]Definition, 0, len(defs))
	for _, d := range defs {
		if d.ID != dungeon.WisdomDungeon {
			frozen = append(frozen, d)
		}
	}
	return BuildCompletionDrops(a, items, frozen)
}

func secondaryDropPools(sources []SecondaryDropSource, candidates []NativeDropCandidate) []secondaryDropPool {
	type window struct{ from, to, rarity, channel int }
	var groups [8][5][]NativeDropCandidate
	for _, c := range candidates {
		if c.CreationWeight == 0 {
			continue
		}
		if c.Equipment {
			groups[c.Rarity][2] = append(groups[c.Rarity][2], c)
			if c.Creature {
				groups[c.Rarity][4] = append(groups[c.Rarity][4], c)
			}
		} else {
			groups[c.Rarity][1] = append(groups[c.Rarity][1], c)
			if c.Recipe {
				groups[c.Rarity][3] = append(groups[c.Rarity][3], c)
			}
		}
	}
	out := []secondaryDropPool{}
	windows := make(map[window][]uint32)
	for _, source := range sources {
		if source.Kind != SecondaryMonsterExtra && source.Kind != SecondaryObject {
			continue
		}
		for _, row := range source.ItemDropWindows {
			from, to := max(1, int(row.Level)-int(row.Down)), min(201, int(row.Level)+int(row.Up))
			for rarity, channels := range groups {
				for channel := 1; channel <= 4; channel++ {
					k := window{from, to, rarity, channel}
					ids, found := windows[k]
					if !found {
						for _, c := range channels[channel] {
							if c.Grade >= from && c.Grade < to {
								ids = append(ids, c.Id)
							}
						}
						windows[k] = ids
					}
					if len(ids) > 0 {
						out = append(out, secondaryDropPool{source.Kind, row.Level, rarity, channel, ids})
					}
				}
			}
		}
	}
	slices.SortFunc(out, func(a, b secondaryDropPool) int {
		return cmp.Or(cmp.Compare(a.Kind, b.Kind), cmp.Compare(a.Level, b.Level), cmp.Compare(a.Rarity, b.Rarity), cmp.Compare(a.Channel, b.Channel))
	})
	return out
}

func groundCarryWeights(a Archive, items *commerce.Items, ids map[uint32]bool) (map[uint32]int64, error) {
	out := make(map[uint32]int64, len(ids))
	physical, ok := a.(interface{ PhysicalIndex(string) (int, int, bool) })
	if !ok {
		for _, id := range slices.Sorted(maps.Keys(ids)) {
			if weight, err := items.Weight(id); err == nil {
				out[id] = weight
			}
		}
		return out, nil
	}
	equipment, err := a.ReadIndex("equipment")
	if err != nil {
		return nil, err
	}
	stackable, err := a.ReadIndex("stackable")
	if err != nil {
		return nil, err
	}
	type member struct {
		id            uint32
		chunk, offset int
	}
	order := make([]member, 0, len(ids))
	for id := range ids {
		path := equipment[int64(id)]
		if path == "" {
			path = stackable[int64(id)]
		}
		if chunk, offset, ok := physical.PhysicalIndex(path); ok {
			order = append(order, member{id, chunk, offset})
		}
	}
	slices.SortFunc(order, func(a, b member) int {
		return cmp.Or(cmp.Compare(a.chunk, b.chunk), cmp.Compare(a.offset, b.offset), cmp.Compare(a.id, b.id))
	})
	for _, m := range order {
		if weight, err := items.Weight(m.id); err == nil {
			out[m.id] = weight
		}
	}
	return out, nil
}
