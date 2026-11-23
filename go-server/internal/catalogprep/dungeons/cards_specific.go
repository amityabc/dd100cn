package dungeons

import (
	"fmt"
	"math"
	"slices"
	"strings"

	"s9a1/server/internal/catalogprep/commerce"
	"s9a1/server/internal/catalogprep/gm"
	"s9a1/server/internal/catalogprep/pvftext"
)

type specificCardRow struct{ Maximum, Item int }
type specificCardDungeon struct {
	Id   uint32
	Hash string
	Rows []specificCardRow
}
type cardLuckRecord struct {
	Item   uint32
	Grade  int
	Rarity byte
}
type cardSpecific struct {
	Dungeons  map[uint32]specificCardDungeon
	Templates map[uint32]*gm.ItemTemplate
	Luck      map[uint32]cardLuckRecord
	hash      string
}

type cardGroupItem struct{ Item, Weight, Quantity uint32 }
type cardGroup struct {
	Group        uint32
	DungeonTypes []uint32
	Items        []cardGroupItem
}
type cardGroupDungeon struct {
	Id      uint32
	Hash    string
	Type    *uint32
	RawType string
}
type cardGroups struct {
	Groups    map[uint32]cardGroup
	Dungeons  map[uint32]cardGroupDungeon
	Templates map[uint32]*gm.ItemTemplate
	Luck      map[uint32]cardLuckRecord
	hash      string
}

const cardSpecificPolicy = "DungeonSpecificFreeCard/older-item-map-reference/v1;fixed100;one-based-lower-bound;first-duplicate-key;zero-item-skipped;stop-after-over100;unavailable-no-reroll;no-event-assets"
const cardGroupPolicy = "NormalPaidGroup/v1;profile1-event-reference;fourth-column-group-reference;normal-default-type0-only;weighted-rows;retain-unavailable-no-reroll;static-intact-acquisition"

func loadCardSpecific(a Archive, items *commerce.Items) (*cardSpecific, error) {
	index, err := a.ReadIndex("dungeon")
	if err != nil {
		return nil, err
	}
	if len(index) > 4096 {
		return nil, fmt.Errorf("specific card dungeon index exceeds native bounds")
	}
	out := &cardSpecific{Dungeons: make(map[uint32]specificCardDungeon)}
	ids := make(map[uint32]bool)
	rows := []specificCardDungeon{}
	totalRows := 0
	r := &cardInput{}
	for _, entry := range orderedPairs(index) {
		if entry.Key <= 0 || entry.Key > math.MaxUint32 {
			continue
		}
		text, err := a.ReadText(entry.Value, budget)
		if err != nil {
			return nil, err
		}
		values := r.ints(r.section(text, "item"))
		if !r.width("dungeon item map", len(values), 2, 0, 256) {
			return nil, r.err
		}
		id := uint32(entry.Key)
		var choices []specificCardRow
		cumulative := 0
		for row := range slices.Chunk(values, 2) {
			item, weight := row[0], row[1]
			if item < -2 || weight < 0 {
				return nil, fmt.Errorf("dungeon %d has unsupported specific card item or weight", id)
			}
			if item == 0 {
				continue
			}
			if weight > math.MaxInt32-cumulative {
				return nil, fmt.Errorf("dungeon %d cumulative card threshold exceeds signed width", id)
			}
			cumulative += weight
			if len(choices) == 0 || choices[len(choices)-1].Maximum != cumulative {
				choices = append(choices, specificCardRow{cumulative, item})
				if item > 0 {
					ids[uint32(item)] = true
				}
			}
			if cumulative > 100 {
				break
			}
		}
		if len(choices) == 0 {
			continue
		}
		totalRows += len(choices)
		if totalRows > 65536 {
			return nil, fmt.Errorf("specific card rows exceed native bounds")
		}
		hash, err := rawHash(a, entry.Value)
		if err != nil {
			return nil, err
		}
		d := specificCardDungeon{id, hash, choices}
		out.Dungeons[id] = d
		rows = append(rows, d)
	}
	if r.err != nil {
		return nil, r.err
	}
	out.Templates, out.Luck, err = cardRewardTemplates(items, ids)
	if err != nil {
		return nil, err
	}
	identity := []any{cardSpecificPolicy, "\n", rows, "\n", orderedPairs(out.Templates)}
	if len(out.Luck) > 0 {
		identity = append(identity, "\ncard-luck-metadata|", orderedPairs(out.Luck))
	}
	out.hash, err = catalogHash(identity...)
	return out, err
}

func cardRewardTemplates(items *commerce.Items, ids map[uint32]bool) (map[uint32]*gm.ItemTemplate, map[uint32]cardLuckRecord, error) {
	templates := make(map[uint32]*gm.ItemTemplate)
	luck := make(map[uint32]cardLuckRecord)
	for id := range ids {
		src, err := items.Source(id)
		if err != nil || eventOwned(src.Path, false) {
			continue
		}
		t := monsterDropTemplate(items, id)
		if t == nil || hasDropLifetime(t) {
			continue
		}
		templates[id] = t
		if t.Kind != 2 {
			r := &cardInput{}
			grade := r.numbers(r.section(src.Text, "grade"), 1, 255)
			rarity := r.numbers(r.section(src.Text, "rarity"), 0, 7)
			if r.err != nil || len(grade) != 1 || len(rarity) != 1 {
				return nil, nil, fmt.Errorf("card reward %d lacks verified ordinary equipment grade/rarity", id)
			}
			luck[id] = cardLuckRecord{id, int(grade[0]), byte(rarity[0])}
		}
	}
	return templates, luck, nil
}

func loadCardGroups(a Archive, items *commerce.Items, t cardTables, definitions []Definition) (*cardGroups, error) {
	out := &cardGroups{Groups: make(map[uint32]cardGroup), Dungeons: make(map[uint32]cardGroupDungeon)}
	r := &cardInput{}
	for _, block := range pvftext.Blocks(r.section(t.clear, "specific goldcard group"), "reward group") {
		header, _, _ := strings.Cut(block, "[")
		ids := r.numbers(header, 1, 65535)
		types := r.numbers(r.section(block, "reward dungeon type list"), 0, 65535)
		items := r.numbers(r.section(block, "reward item list"), 1, math.MaxInt32)
		if len(ids) != 1 || len(types) == 0 || len(types) > 256 || out.Groups[ids[0]].Group != 0 || !r.width("group items", len(items), 3, 1, 256) {
			return nil, fmt.Errorf("ambiguous or incomplete paid card group")
		}
		var rows []cardGroupItem
		for row := range slices.Chunk(items, 3) {
			rows = append(rows, cardGroupItem{row[0], row[1], row[2]})
		}
		out.Groups[ids[0]] = cardGroup{ids[0], types, rows}
	}
	if r.err != nil {
		return nil, r.err
	}
	active := make(map[uint32]bool)
	for _, row := range t.profiles["event"] {
		if row.SpecialGroup != 0 {
			active[row.SpecialGroup] = true
		}
	}
	dungeons := []cardGroupDungeon{}
	ids := make(map[uint32]bool)
	if len(active) > 0 {
		index, err := a.ReadIndex("dungeon")
		if err != nil {
			return nil, err
		}
		seen := make(map[uint32]bool)
		ordinaryAdmitted := false
		for _, d := range definitions {
			id := d.ID
			if seen[id] {
				continue
			}
			seen[id] = true
			if index[int64(id)] == "" {
				return nil, fmt.Errorf("paid card group dungeon %d is not indexed", id)
			}
			path := index[int64(id)]
			text, err := a.ReadText(path, budget)
			if err != nil {
				return nil, err
			}
			hash, err := rawHash(a, path)
			if err != nil {
				return nil, err
			}
			kind := r.section(text, "dungeon type")
			entry := cardGroupDungeon{Id: id, Hash: hash, RawType: kind}
			if kind == "" || kind == "\x60normal\x60" || kind == "\x60[normal]\x60" {
				ordinaryAdmitted = true
				entry.Type = new(uint32)
			}
			out.Dungeons[id] = entry
			dungeons = append(dungeons, entry)
		}
		if ordinaryAdmitted {
			for id, g := range out.Groups {
				if active[id] && slices.Contains(g.DungeonTypes, 0) {
					for _, row := range g.Items {
						ids[row.Item] = true
					}
				}
			}
		}
	}
	if r.err != nil {
		return nil, r.err
	}
	var err error
	out.Templates, out.Luck, err = cardRewardTemplates(items, ids)
	if err != nil {
		return nil, err
	}
	out.hash, err = catalogHash(cardGroupPolicy, "\n", t.clearHash, "\n", orderedPairs(out.Groups), "\n", dungeons,
		"\n", orderedPairs(out.Templates), "\n", orderedPairs(out.Luck))
	return out, err
}
