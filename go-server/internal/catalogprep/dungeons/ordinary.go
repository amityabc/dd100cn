package dungeons

import (
	"fmt"
	"regexp"
	"slices"
	"strings"

	"s9a1/server/internal/catalogprep/pvftext"
	"s9a1/server/internal/opening"
	"s9a1/server/internal/questwire"
)

func BuildOrdinary(a Archive, xp *Experience, quests []questwire.Quest) (Layouts, error) {
	s, err := newCatalogSource(a, xp, quests, nil)
	if err != nil {
		return Layouts{}, err
	}
	return s.ordinary()
}

func (s *catalogSource) ordinary() (Layouts, error) {
	var out Layouts
	world, err := s.worldText(2)
	if err != nil {
		return out, err
	}
	for _, id := range worldFree(world) {
		text, err := s.dungeonText(id)
		if err != nil {
			return out, err
		}
		ordinary := false
		var branches []Definition
		for maze, source := range mazeBlocks(text) {
			p := &input{}
			quests := p.questConnections(source)
			if p.err != nil {
				return out, p.err
			}
			if len(quests) == 0 && ordinary {
				continue
			}
			if len(quests) > 0 && !slices.ContainsFunc(quests, func(q uint16) bool { return q != 7939 && q != 7940 }) {
				continue
			}
			d, err := s.definition(id, text, maze, layoutOptions{plainMapText: true, objectTypes: true})
			if err != nil {
				return out, err
			}
			if id == 9 && maze == 1 {
				d, err = s.frost(d)
				if err != nil {
					return out, err
				}
			}
			d, err = s.bindCinematics(d)
			if err != nil {
				return out, err
			}
			branches = append(branches, d)
			ordinary = ordinary || len(quests) == 0
		}
		if !ordinary {
			continue
		}
		for _, d := range branches {
			l, err := s.layout(2, d, "gran", nil, nil, nil, nil)
			if err != nil {
				return out, err
			}
			out.Definitions = append(out.Definitions, l)
		}
	}
	if s.worlds[102] != "" {
		world, err := s.worldText(102)
		if err != nil {
			return out, err
		}
		for _, id := range worldFree(world) {
			text, err := s.dungeonText(id)
			if err != nil {
				return out, err
			}
			d, err := s.definition(id, text, 0, layoutOptions{plainMapText: true})
			if err != nil {
				return out, err
			}
			d.EntryFatigueCost, d.RoomFatigueCost = 0, 0
			d.UsesOrdinaryRewards = false
			d.meta.disableDrops, d.meta.disableClear, d.meta.disableExperience, d.meta.multiplier = true, true, true, "0"
			d, err = s.bindCinematics(d)
			if err != nil {
				return out, err
			}
			l, err := s.layout(102, d, "training", nil, nil, nil, nil)
			if err != nil {
				return out, err
			}
			out.Definitions = append(out.Definitions, l)
		}
	}
	sky, err := s.sky()
	if err != nil {
		return out, err
	}
	out.Definitions = append(out.Definitions, sky...)
	return out, nil
}

type skyBranch struct {
	id          uint32
	quest       uint16
	maze        int
	start, boss uint32
	count       int
	minimum     byte
}

func (s *catalogSource) sky() ([]Layout, error) {
	world, err := s.worldText(185)
	if err != nil {
		return nil, err
	}
	selectors, err := worldSelectors(world)
	if err != nil {
		return nil, err
	}
	branches := []skyBranch{{11, 3163, 2, 57879, 57887, 9, 17}, {11, 3164, 3, 57888, 57894, 7, 17},
		{12, 3168, 3, 57895, 57901, 7, 19}, {12, 3169, 4, 57903, 57911, 9, 19}, {13, 3170, 2, 57912, 57918, 7, 20}, {13, 3171, 3, 57919, 57926, 8, 20},
		{12, 0, 0, 58606, 58613, 8, 19}, {13, 0, 0, 58614, 58630, 9, 20}}
	var out []Layout
	for _, branch := range branches {
		wantQuest, inProgress := int(branch.quest), true
		if branch.quest == 0 || branch.id != 11 {
			wantQuest, inProgress = 3167, false
		}
		matching := 0
		for _, row := range selectors {
			if row.Dungeon == branch.id && row.Quest == wantQuest && row.InProgress == inProgress {
				matching++
			}
		}
		if matching != 1 {
			return nil, fmt.Errorf("Sky world185 lacks the native qualification for dungeon %d", branch.id)
		}
		if q := s.quests[branch.quest]; branch.quest != 0 {
			if !q.Supported || q.Kind != 1 || q.Subtype != -1 || q.Operations == nil || q.Operations.Grade != "epic" ||
				q.MinimumLevel != branch.minimum || !slices.Equal(q.Data, []int{int(branch.boss)}) || !slices.Equal(q.DestinationDungeons, []int{int(branch.id)}) ||
				len(q.Required) != 1 || !slices.Equal(q.Required[0], []uint16{branch.quest - 1}) {
				return nil, fmt.Errorf("Sky quest %d differs from its source qualification", branch.quest)
			}
		}
		text, err := s.dungeonText(branch.id)
		if err != nil {
			return nil, err
		}
		d, err := s.definition(branch.id, text, branch.maze, layoutOptions{plainMapText: true})
		if err != nil {
			return nil, err
		}
		eligible := []uint16{}
		if branch.quest != 0 {
			eligible = append(eligible, branch.quest)
		}
		if d.MinimumLevel != branch.minimum || d.Start.MapID != branch.start || len(d.Rooms) != branch.count || !slices.Equal(d.meta.quests, eligible) {
			return nil, fmt.Errorf("Sky dungeon %d maze %d changed its native identity", branch.id, branch.maze)
		}
		maps := []uint32{}
		for _, room := range d.Rooms {
			maps = append(maps, room.MapID)
		}
		slices.Sort(maps)
		for i, mapID := range maps {
			expected := branch.start + uint32(i)
			if branch.id == 13 && branch.quest == 0 && i == len(maps)-1 {
				expected = 58630
			}
			if expected != mapID {
				return nil, fmt.Errorf("Sky room source changed: %d != %d", mapID, expected)
			}
		}
		boss := slices.IndexFunc(d.Rooms, func(r opening.Room) bool { return r.X == d.BossX && r.Y == d.BossY })
		if boss < 0 || d.Rooms[boss].MapID != branch.boss {
			return nil, fmt.Errorf("Sky boss room changed for %d", branch.id)
		}
		var cleared []uint16
		if branch.id != 11 {
			cleared = []uint16{3167}
		}
		d, err = s.bindCinematics(d)
		if err != nil {
			return nil, err
		}
		l, err := s.layout(185, d, "sky", eligible, cleared, nil, nil)
		if err != nil {
			return nil, err
		}
		out = append(out, l)
	}
	return out, nil
}

var hiddenFrostRemoval = regexp.MustCompile(`\[ACTOR\]\s*\[TYPE\]\s*\[MONSTER\]\s*\[INDEX\]\s*0\s*\[POS\]\s*855\s+233\s*\[/ACTOR\]\s*\[DESTROY\]\s*\[IS SHOW DIE\]\s*0\s*\[/DESTROY\]`)

func (s *catalogSource) frost(d Definition) (Definition, error) {
	mapPath, moviePath, monsterPath := s.maps[76155], s.movies[7], s.monsters[107000909]
	m, err := s.readMap(76155, true)
	if err != nil {
		return d, err
	}
	ref := basicAction.FindAllStringSubmatch(m, -1)
	if len(ref) != 1 || strings.ReplaceAll(ref[0][1], "\\", "/") != "Action/Map_76155.act" {
		return d, fmt.Errorf("Frost gate map action binding changed")
	}
	path := actionPath(mapPath, ref[0][1])
	act, err := s.a.ReadText(path, budget)
	if err != nil {
		return d, err
	}
	movie, err := s.a.ReadText(moviePath, budget)
	if err != nil {
		return d, err
	}
	monster, err := s.a.ReadText(monsterPath, budget)
	if err != nil {
		return d, err
	}
	i := slices.IndexFunc(d.Rooms, func(r opening.Room) bool { return r.X == 2 && r.Y == 1 && r.MapID == 76155 })
	if i < 0 || !slices.Equal(d.meta.quests, []uint16{3148}) || d.BossX != 3 || d.BossY != 1 {
		return d, fmt.Errorf("Frost gate requires its exact quest maze")
	}
	room := d.Rooms[i]
	if len(room.Monsters) != 1 || room.Monsters[0] != (opening.Monster{Code: 107000909, Level: 11, Type: 3, Dummy: true, BlocksRoom: true}) || len(room.MapAliases) != 0 {
		return d, fmt.Errorf("Frost gate requires its exact controller")
	}
	triggers, behaviors := pvftext.Blocks(act, "TRIGGER"), pvftext.Blocks(act, "BEHAVIOR")
	normalized := func(t string) string { return strings.Join(strings.Fields(t), " ") }
	p := &input{}
	if len(triggers) != 2 || len(behaviors) != 2 || normalized(triggers[0]) != "[ON START MAP] [DO BEHAVIOR] [ME] 0" ||
		normalized(behaviors[0]) != "[CINEMATIC] 7" ||
		normalized(triggers[1]) != "[ON END CINEMATIC] 7 [WHICH] [PLAYER] [DELAY DO BEHAVIOR] [CHECKUP OBJECT] 1 100" ||
		normalized(behaviors[1]) != "[MOVE MAP] 3 1 130 210 0 [IS STATE CHECK] 0 [/MOVE MAP]" ||
		!slices.Equal(p.numbers(p.field(movie, "MAP", true)), []int64{76155}) || !hiddenFrostRemoval.MatchString(movie) ||
		!slices.Equal(p.numbers(p.field(m, "monster", true)), []int64{107000909, 1, 0, 855, 233, 0, 1, 1}) || p.err != nil {
		return d, fmt.Errorf("Frost gate cinematic completion or controller DESTROY binding changed")
	}
	d.ResourceHash = hashText(strings.Join([]string{"FrostCinematicGate/v1", d.ResourceHash, mapPath, hashText(m), path, hashText(act),
		moviePath, hashText(movie), monsterPath, hashText(monster)}, "\n"))
	return d, nil
}
