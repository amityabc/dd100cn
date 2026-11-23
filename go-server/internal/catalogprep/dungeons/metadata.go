package dungeons

import (
	"slices"
	"strconv"
	"strings"

	"s9a1/server/internal/catalogprep/pvftext"
)

func (p *input) questConnections(maze string) []uint16 {
	values := p.numbers(p.field(maze, "quest connection", false))
	out := []uint16{}
	if len(values) == 0 {
		return out
	}
	if !p.require(len(values) >= 3 && values[0] == 0 && len(values)%2 == 1, "Unsupported quest-maze connection qualification") {
		return out
	}
	for i := 1; i < len(values); i += 2 {
		p.require(values[i] > 0 && values[i] < 30000 && values[i+1] == -1, "Unsupported quest-maze connection qualification")
		out = append(out, uint16(values[i]))
	}
	slices.Sort(out)
	return slices.Compact(out)
}

func (p *input) metadata(m *nativeMeta, monsterIndex map[int64]string, text, maze string, difficulty byte) {
	m.quests = p.questConnections(maze)
	if story := p.field(text, "story mode", false); story != "" && len(m.quests) > 0 {
		quests := p.numbers(p.field(story, "quest list", false))
		if slices.ContainsFunc(quests, func(q int64) bool { return q > 0 && q < 30000 && slices.Contains(m.quests, uint16(q)) }) {
			size := p.scalar(story, "difficulty size", 0)
			rates, err := pvftext.Integers(p.field(story, "increase exp rate", true))
			if p.require(err == nil && size >= 1 && size <= 5 && int64(len(rates)) >= size && int64(len(rates)) <= size+1,
				"Invalid story experience difficulty/rate table") {
				for i, v := range rates {
					p.require(v >= 0 && v <= 100000, "Invalid story experience difficulty/rate table")
					if int64(i) < size {
						m.story = append(m.story, uint32(v))
					}
				}
			}
		}
	}
	named, err := pvftext.Integers(p.field(text, "named monster", false))
	p.require(err == nil && len(named) <= 256, "Invalid named monster declaration")
	slices.Sort(named)
	for i, id := range named {
		p.require(id > 0 && monsterIndex[int64(id)] != "" && (i == 0 || named[i-1] != id), "Invalid named monster declaration")
		m.named = append(m.named, uint32(id))
	}
	for _, row := range pvftext.Blocks(text, "hell dungeon") {
		v, err := strconv.ParseInt(row, 10, 32)
		p.require(err == nil, "Dungeon hell capability requires one native integer")
		m.hell = v != 0
	}
	m.tower = tagPresent(text, "towerofgrief") || strings.Contains(strings.ToLower(text), "tower of grief")
	m.difficultyItems = p.difficultyItems(text, difficulty, m.selectable)
}

func (p *input) difficultyItems(text string, difficulty byte, selectable []byte) [][]uint32 {
	body := p.field(text, "difficulty dropitem list", false)
	out := [][]uint32{}
	if body == "" {
		return out
	}
	if len(p.numbers(p.field(text, "difficulty level", false))) == 0 {
		switch {
		case difficulty == 4 && tagPresent(text, "storm lane dungeon"):
			selectable = []byte{4}
		case difficulty == 2 && tagPresent(text, "curse of bloodyfruit dungeon"):
			selectable = []byte{2}
		default:
			p.require(false, "Dungeon difficulty drop list requires five native difficulty flags")
			return out
		}
	}
	rows := pvftext.Blocks(body, "item info")
	if !p.require(len(rows) == len(selectable) && len(rows) <= 5, "Dungeon difficulty drop list rows do not match enabled difficulties") {
		return out
	}
	out = make([][]uint32, 5)
	for i := range out {
		out[i] = []uint32{}
	}
	for i, row := range rows {
		values, err := pvftext.Integers(row)
		p.require(err == nil && len(values) > 0 && len(values) <= 256, "Dungeon difficulty drop list contains an invalid item row")
		seen := map[int]bool{}
		for _, value := range values {
			p.require(value > 0 && !seen[value], "Dungeon difficulty drop list contains a repeated or invalid item")
			seen[value] = true
			out[selectable[i]] = append(out[selectable[i]], uint32(value))
		}
	}
	return out
}
