package dungeons

import (
	"errors"
	"fmt"
	"maps"
	"regexp"
	"slices"
	"strings"

	"s9a1/server/internal/catalogprep/pvftext"
	"s9a1/server/internal/dungeon"
	"s9a1/server/internal/opening"
	"s9a1/server/internal/questwire"
	"s9a1/server/internal/ruby"
)

func BuildMainline(a Archive, xp *Experience, quests []questwire.Quest, issues map[uint16]string) (Layouts, error) {
	s, err := newCatalogSource(a, xp, quests, issues)
	if err != nil {
		return Layouts{}, err
	}
	return s.mainline()
}

func (s *catalogSource) worldCatalog() ([]int, error) {
	ids := []int{}
	for _, key := range slices.Sorted(maps.Keys(s.worlds)) {
		if key < 0 || key > 1<<31-1 {
			return nil, fmt.Errorf("native world id out of range: %d", key)
		}
		id := int(key)
		if _, err := s.worldText(id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, nil
}

var epicGrade = regexp.MustCompile("(?m)^\\[grade\\]\\s*`\\[epic\\]`")

func (s *catalogSource) mainlineQuestScope(worldIDs []int) (map[uint32]bool, map[uint16]bool, error) {
	destinations := map[uint32]bool{}
	connections := map[uint16]bool{}
	add := func(q questwire.Quest) {
		for _, id := range q.DestinationDungeons {
			if id >= 0 {
				destinations[uint32(id)] = true
			}
		}
	}
	questIndex, err := s.a.ReadIndex("quest", budget)
	if err != nil {
		return nil, nil, err
	}
	p := &input{}
	for id, q := range s.quests {
		if q.Operations != nil && q.Operations.Grade == "epic" {
			add(q)
			connections[id] = true
		}
		if !q.Supported && questIndex[int64(id)] != "" {
			script, err := s.a.ReadText(questIndex[int64(id)], budget)
			if err != nil {
				return nil, nil, err
			}
			if epicGrade.MatchString(script) {
				connections[id] = true
				for _, block := range pvftext.Blocks(script, "dungeon info") {
					ns := p.numbers(block)
					for i := 0; i+1 < len(ns); i += 2 {
						if ns[i] > 0 {
							destinations[uint32(ns[i])] = true
						}
					}
				}
			}
		}
	}
	queue := slices.Sorted(maps.Keys(connections))
	for i := 0; i < len(queue); i++ {
		q, ok := s.quests[queue[i]]
		if !ok {
			continue
		}
		add(q)
		appendQuest := func(id uint16) {
			if !connections[id] {
				connections[id] = true
				queue = append(queue, id)
			}
		}
		for _, id := range q.Children {
			appendQuest(id)
		}
		for _, group := range q.Required {
			for _, id := range group {
				appendQuest(id)
			}
		}
		for _, answer := range q.Answers {
			appendQuest(answer.Quest)
		}
	}
	for _, lesson := range []struct {
		id      uint16
		dungeon uint32
		mapID   int
		prior   uint16
	}{{12174, 100000195, 100000482, 12173}, {12176, 100000197, 100000656, 12175}} {
		q := s.quests[lesson.id]
		if q.Supported && q.Operations != nil && q.Operations.Grade == "episode" && q.Kind == 1 && q.Subtype == -1 &&
			q.MinimumLevel == 96 && slices.Equal(q.Data, []int{lesson.mapID}) && slices.Equal(q.DestinationDungeons, []int{int(lesson.dungeon)}) &&
			len(q.Required) == 1 && slices.Equal(q.Required[0], []uint16{lesson.prior}) {
			destinations[lesson.dungeon], connections[lesson.id] = true, true
		}
	}
	for _, gate := range worldIDs {
		rows, err := worldSelectors(s.worldTexts[gate])
		if err != nil {
			continue
		}
		for _, row := range rows {
			q := s.quests[uint16(row.Quest)]
			if row.InProgress && q.Supported && q.Kind == 4 && q.Operations != nil && q.Operations.Grade == "mission" &&
				slices.Contains(q.DestinationDungeons, int(row.Dungeon)) &&
				slices.ContainsFunc(q.DropRules, func(rule questwire.QuestDropRule) bool { return rule.Dungeon == -1 || rule.Dungeon == int(row.Dungeon) }) {
				destinations[row.Dungeon], connections[q.Id] = true, true
			}
		}
	}
	return destinations, connections, p.err
}

func (s *catalogSource) legacyDestinations() (map[uint32]bool, error) {
	ids := map[uint32]bool{}
	for _, gate := range []int{2, 102} {
		text, err := s.worldText(gate)
		if err != nil {
			return nil, err
		}
		for _, id := range worldFree(text) {
			ids[id] = true
		}
	}
	for _, id := range []uint32{11, 12, 13, 7123, 5100, 100000194, 100000325, 100000163, 100000164, 100000165, 100000175,
		291100433, 291100434, 100000006, 100000156, 123, 124, 100000196,
		100000002, 100000176, 100000177, 100000178, 100000179, 100000180, 100000181, 100000214, 100000212, 100000215,
		100000199, 100000209, 100000210, 100000211, 100000003} {
		ids[id] = true
	}
	for id := uint32(100000008); id <= 100000150; id++ {
		ids[id] = true
	}
	for id := uint32(100000157); id <= 100000161; id++ {
		ids[id] = true
	}
	for id := uint32(100000640); id <= 100000650; id++ {
		ids[id] = true
	}
	for floor := byte(1); floor <= 60; floor++ {
		ids[ruby.FloorID(floor)] = true
	}
	return ids, nil
}

func (s *catalogSource) mainline() (Layouts, error) {
	var result Layouts
	result.ResourceIssues = []dungeon.ResourceIssue{}
	worldIDs, err := s.worldCatalog()
	if err != nil {
		return result, err
	}
	destinations, connections, err := s.mainlineQuestScope(worldIDs)
	if err != nil {
		return result, err
	}
	legacy, err := s.legacyDestinations()
	if err != nil {
		return result, err
	}
	for id := range legacy {
		delete(destinations, id)
	}
	selected := map[int]map[uint32][]dungeon.SelectorRule{}
	optional := map[uint32]bool{}
	for _, gate := range worldIDs {
		rows, err := worldSelectors(s.worldTexts[gate])
		if err != nil || !slices.ContainsFunc(rows, func(r selectorRow) bool { return destinations[r.Dungeon] }) {
			continue
		}
		for _, r := range rows {
			if !r.InProgress && r.Quest > 0 && connections[uint16(r.Quest)] && !legacy[r.Dungeon] && !destinations[r.Dungeon] &&
				strings.Contains(strings.ToLower(s.dungeons[int64(r.Dungeon)]), "ringofcirculation") {
				optional[r.Dungeon], destinations[r.Dungeon] = true, true
			}
		}
	}
	seen := map[uint32]bool{}
	for _, gate := range worldIDs {
		text := s.worldTexts[gate]
		if !tagPresent(text, "dungeon") {
			continue
		}
		body := worldRows.FindStringSubmatch(worldMain(text))
		if len(body) > 0 && strings.TrimSpace(body[1]) == "" {
			continue
		}
		rows, err := worldSelectors(text)
		if err != nil {
			result.ResourceIssues = append(result.ResourceIssues, dungeon.ResourceIssue{Maze: -1, Reason: fmt.Sprintf("worldmap%d: %s", gate, err)})
			continue
		}
		for _, r := range rows {
			if !destinations[r.Dungeon] {
				continue
			}
			if selected[gate] == nil {
				selected[gate] = map[uint32][]dungeon.SelectorRule{}
			}
			selected[gate][r.Dungeon] = append(selected[gate][r.Dungeon], r.SelectorRule)
			seen[r.Dungeon] = true
		}
	}
	for _, id := range slices.Sorted(maps.Keys(destinations)) {
		if !seen[id] {
			result.ResourceIssues = append(result.ResourceIssues, dungeon.ResourceIssue{Dungeon: id, Maze: -1, Reason: "Mainline dungeon has no main worldmap selector"})
		}
	}
	dungeons := map[uint32][]Definition{}
	blocked := map[uint32]map[uint16]bool{}
	addIssue := func(id uint32, maze int, err error, linked []uint16) {
		if !optional[id] {
			result.ResourceIssues = append(result.ResourceIssues, dungeon.ResourceIssue{Dungeon: id, Maze: maze, Reason: err.Error()})
		}
		for _, q := range linked {
			if blocked[id] == nil {
				blocked[id] = map[uint16]bool{}
			}
			blocked[id][q] = true
		}
	}
	for _, id := range slices.Sorted(maps.Keys(seen)) {
		text, err := s.dungeonText(id)
		if err != nil {
			addIssue(id, -1, err, nil)
			continue
		}
		ordinary := false
		for maze, source := range mazeBlocks(text) {
			p := &input{}
			values := p.numbers(p.field(source, "quest connection", false))
			var linked []uint16
			for i := 1; i < len(values); i += 2 {
				q := values[i]
				if q > 0 && q < 30000 && connections[uint16(q)] {
					linked = append(linked, uint16(q))
				}
			}
			if len(values) == 0 && ordinary || len(values) > 0 && len(linked) == 0 {
				continue
			}
			d, err := s.mainlineMaze(id, text, maze, linked)
			if err != nil {
				addIssue(id, maze, err, linked)
				continue
			}
			dungeons[id] = append(dungeons[id], d)
			if len(values) == 0 {
				ordinary = true
			}
		}
	}
	for _, gate := range worldIDs {
		for _, id := range slices.Sorted(maps.Keys(selected[gate])) {
			blocks := list(slices.Sorted(maps.Keys(blocked[id])))
			for _, d := range dungeons[id] {
				d, err = s.bindCinematics(d)
				if err != nil {
					return result, err
				}
				l, err := s.layout(uint16(gate), d, "mainline", nil, nil, selected[gate][id], blocks)
				if err != nil {
					return result, err
				}
				result.Definitions = append(result.Definitions, l)
			}
		}
	}
	return result, nil
}

func (s *catalogSource) mainlineMaze(id uint32, text string, maze int, linked []uint16) (Definition, error) {
	for _, q := range linked {
		if !s.quests[q].Supported {
			reason := s.questIssues[q]
			if reason == "" {
				reason = "quest is unprepared"
			}
			return Definition{}, fmt.Errorf("Quest%d: %s", q, reason)
		}
	}
	d, err := s.definition(id, text, maze, layoutOptions{mainline: true, objectTypes: true})
	if err != nil {
		if cause := errors.Unwrap(err); cause != nil {
			err = cause
		}
		return d, err
	}
	if err := reachable(d.Dungeon); err != nil {
		return d, err
	}
	p := &input{}
	if !p.require(d.Difficulty <= 4, "Mainline dungeon difficulty exceeds the native range") {
		return d, p.err
	}
	if len(d.meta.quests) == 0 && d.Difficulty == 0 {
		rows := pvftext.Blocks(text, "difficulty level")
		if len(rows) > 0 {
			base := p.numbers(rows[0])
			for _, other := range rows[1:] {
				p.require(slices.Equal(base, p.numbers(other)), "Mainline ordinary difficulty flags conflict")
			}
			p.require(len(d.meta.selectable) > 0, "Mainline ordinary difficulty flags enable no selectable mode")
		}
	}
	if d.Difficulty != 0 {
		d.UsesOrdinaryRewards, d.UsesOrdinaryMonsterDrops = false, !d.meta.disableDrops
	}
	if id == 100000195 || id == 100000197 {
		quest, start := uint16(12174), uint32(100000480)
		if id == 100000197 {
			quest, start = 12176, 100000483
		}
		p.require(maze == 0 && d.MinimumLevel == 96 && d.meta.basis == 135 && d.Difficulty == 0 && d.meta.disableDrops && d.Start.MapID == start &&
			slices.Equal(d.meta.quests, []uint16{quest}) && d.BossX == 2 && d.BossY == 0 && len(d.Rooms) == 3, "Wisdom introduction quest maze declaration changed")
	} else {
		for _, room := range d.Rooms {
			p.require(!slices.ContainsFunc(room.Monsters, func(m opening.Monster) bool { return m.Level < 3 || m.Level > 100 }),
				"Mainline monster level is outside prepared rewards")
		}
	}
	return d, p.err
}
