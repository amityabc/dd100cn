package dungeons

import (
	"fmt"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"s9a1/server/internal/dungeon"
	"s9a1/server/internal/opening"
)

type selectorRow struct {
	Dungeon uint32
	dungeon.SelectorRule
}

var (
	worldExtra    = regexp.MustCompile(`(?m)^\[extra map\]`)
	worldRows     = regexp.MustCompile(`(?ms)^\[dungeon\]\s*(.*?)^\[/dungeon\]`)
	selectorToken = regexp.MustCompile(`(?i)\[in progress\]|\[is clear quest\]|-?\d+`)
)

func worldMain(text string) string {
	if index := worldExtra.FindStringIndex(text); index != nil {
		return text[:index[0]]
	}
	return text
}

func worldSelectors(text string) ([]selectorRow, error) {
	if len(text) > budget {
		return nil, fmt.Errorf("Sky worldmap exceeds its parser budget")
	}
	text = worldMain(text)
	blocks := worldRows.FindAllStringSubmatch(text, -1)
	if len(blocks) != 1 || !tagPresent(text, "dungeon") || !tagPresent(text, "/dungeon") {
		return nil, fmt.Errorf("Sky worldmap requires one complete main dungeon block")
	}
	body := blocks[0][1]
	positions := selectorToken.FindAllStringIndex(body, -1)
	p := &input{}
	p.completeRows(body, positions, "Sky worldmap contains unsupported selector syntax")
	if !p.require(len(positions) > 0, "Sky worldmap contains unsupported selector syntax") || p.err != nil {
		return nil, p.err
	}
	tokens := make([]string, len(positions))
	for i, pos := range positions {
		tokens[i] = strings.ToLower(body[pos[0]:pos[1]])
	}
	var rows []selectorRow
	for i := 0; i < len(tokens); {
		id, err := strconv.ParseInt(tokens[i], 10, 32)
		i++
		if err != nil || i >= len(tokens) {
			return nil, fmt.Errorf("Sky selector requires complete integer qualification rows")
		}
		progress := tokens[i] == "[in progress]"
		qualifier := progress || tokens[i] == "[is clear quest]"
		if qualifier {
			i++
		}
		if i >= len(tokens) {
			return nil, fmt.Errorf("Sky selector requires complete integer qualification rows")
		}
		quest, err := strconv.ParseInt(tokens[i], 10, 32)
		i++
		if err != nil {
			return nil, fmt.Errorf("Sky selector requires complete integer qualification rows")
		}
		if id <= 0 || quest < -1 || quest == 0 || quest >= 30000 || qualifier && quest == -1 {
			return nil, fmt.Errorf("Sky selector dungeon or quest qualification is out of range")
		}
		rows = append(rows, selectorRow{uint32(id), dungeon.SelectorRule{Quest: int(quest), InProgress: progress}})
	}
	return rows, nil
}

func worldFree(text string) []uint32 {
	p := &input{}
	body := p.field(text, "dungeon", true)
	main, _, _ := strings.Cut(body, "[in progress]")
	values := p.numbers(main)
	seen := map[uint32]bool{}
	var ids []uint32
	for i := 0; i+1 < len(values); i += 2 {
		u := uint32(values[i])
		if values[i] > 0 && values[i] <= 1<<31-1 && values[i+1] == -1 && !seen[u] {
			ids = append(ids, u)
			seen[u] = true
		}
	}
	slices.Sort(ids)
	return ids
}

func reachable(d opening.Dungeon) error {
	loc := func(r opening.Room) [2]int { return [2]int{int(r.X), int(r.Y)} }
	rooms := map[[2]int]opening.Room{}
	for _, r := range d.Rooms {
		k := loc(r)
		if rooms[k].MapID != 0 {
			return fmt.Errorf("Dungeon room coordinates are ambiguous")
		}
		rooms[k] = r
	}
	visited := map[[2]int]bool{loc(d.Start): true}
	queue := []opening.Room{d.Start}
	moves := [4]struct {
		x, y        int
		exit, entry byte
	}{{1, 0, 1, 4}, {0, -1, 2, 8}, {-1, 0, 4, 1}, {0, 1, 8, 2}}
	for i := 0; i < len(queue); i++ {
		r := queue[i]
		for _, m := range moves {
			k := [2]int{int(r.X) + m.x, int(r.Y) + m.y}
			t, ok := rooms[k]
			if ok && r.Gates&m.exit != 0 && t.Gates&m.entry != 0 && !visited[k] {
				visited[k] = true
				queue = append(queue, t)
			}
		}
	}
	if len(visited) != len(rooms) {
		return fmt.Errorf("Mainline maze contains rooms unreachable through reciprocal gates")
	}
	return nil
}
