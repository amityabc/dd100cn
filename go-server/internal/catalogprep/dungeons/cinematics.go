package dungeons

import (
	"errors"
	"fmt"
	"io/fs"
	"maps"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"s9a1/server/internal/catalogprep/pvftext"
	"s9a1/server/internal/opening"
)

type cinematicKey struct {
	mapID uint32
	upper bool
}

var (
	basicAction         = regexp.MustCompile("(?m)^\\[basic action\\]\\s*`([^`]+)`")
	placements          = regexp.MustCompile(`(?ms)^\[monster\]\s*(.*?)^\[/monster\]`)
	cinematicMonsterRow = regexp.MustCompile("(\\d+)\\s+(\\d+)\\s+(-?\\d+)\\s+(-?\\d+)\\s+(-?\\d+)\\s+(-?\\d+)\\s+(\\d+)\\s+(\\d+)\\s+(?:`[^`]+`\\s*)+")
	entryTrigger        = regexp.MustCompile(`^\s*(?:\[LIMIT\]\s*1\s*)?\[ON START MAP\]\s*\[DO BEHAVIOR\]\s*\[ME\]\s*(\d+)\s*$`)
	deadTrigger         = regexp.MustCompile(`^\s*(?:\[LIMIT\]\s*1\s*)?\[WHICH\]\s*\[MONSTER\]\s*\[CHECKUP\]\s*\[IS INDEX\]\s*(\d+)\s*\[/IS INDEX\]\s*\[HP\]\s*\[=\]\s*0\s*\[%\]\s*\[/CHECKUP\]\s*\[DO BEHAVIOR\]\s*\[ME\]\s*(\d+)\s*$`)
	characterTrigger    = regexp.MustCompile(`^\s*\[ON START MAP\]\s*\[LIMIT\]\s*1\s*\[WHICH\]\s*\[CHARACTER\]\s*\[DO BEHAVIOR\]\s*\[CHECKUP OBJECT\]\s*(\d+)\s*$`)
	delayedTrigger      = regexp.MustCompile(`^\s*\[LIMIT\]\s*1\s*\[DELAY DO BEHAVIOR\]\s*\[ME\]\s*(\d+)\s+100\s*$`)
	groupTrigger        = regexp.MustCompile(`^\s*\[ON START MAP\]\s*\[LIMIT\]\s*1\s*\[WHICH\]\s*\[CHARACTER\]\s*\[DO BEHAVIOR\]\s*\[ME\]\s*(\d+)\s*$`)
	hpTrigger           = regexp.MustCompile(`^\s*(?:\[LIMIT\]\s*1\s*)?\[WHICH\]\s*\[MONSTER\]\s*\[CHECKUP\]\s*\[IS INDEX\]\s*(\d+)\s*\[/IS INDEX\]\s*\[HP\]\s*\[<=\]\s*(\d+)\s*\[POINT\]\s*\[/CHECKUP\]\s*\[DO BEHAVIOR\]\s*\[ME\]\s*(\d+)\s*$`)
	groupBehavior       = regexp.MustCompile(`^\s*\[CINEMATIC\]\s*(\d+)\s*$`)
	entryBehavior       = regexp.MustCompile(`^\s*\[CINEMATIC\]\s*(\d+)\s*\[NAME HIDE ON\]\s*$`)
	singleBehavior      = regexp.MustCompile(`^\s*\[CINEMATIC\]\s*(\d+)\s*(?:\[KILL ALL MONSTER NOT BOSS\]\s*)?$`)
	movieMap            = regexp.MustCompile(`(?m)^\[MAP\]\s*(\d+)\s*`)
	groupDestroy        = regexp.MustCompile(`(?s)\[ACTOR\]\s*\[TYPE\]\s*\[GROUP\](.*?)\[GROUP INFO\](.*?)\[/GROUP INFO\](.*?)\[/ACTOR\](.*?)\[DESTROY\]\s*\[IS SHOW DIE\]\s*([01])\s*(?:\[IS REWARD\]\s*[01]\s*)?\[/DESTROY\]`)
	groupMember         = regexp.MustCompile(`\[ACTOR\]\s*\[TYPE\]\s*\[MONSTER\]\s*\[INDEX\]\s*(\d+)`)
	singleDestroy       = regexp.MustCompile(`\[ACTOR\]\s*\[TYPE\]\s*\[MONSTER\]\s*\[INDEX\]\s*(\d+)\s*\[POS\]\s*(-?\d+)\s+(-?\d+)\s*\[/ACTOR\]\s*\[DESTROY\]\s*\[IS SHOW DIE\]\s*([01])\s*(?:\[IS REWARD\]\s*[01]\s*)?\[/DESTROY\]`)
)

func ruleText(r opening.CinematicDestroy) string {
	trigger := ""
	if r.TriggerIndex != nil {
		trigger = strconv.Itoa(int(*r.TriggerIndex))
	}
	return fmt.Sprintf("LocalDungeonCinematicDestroyRule { Index = %d, Movie = %d, TriggerIndex = %s, Hash = %s }", r.Index, r.Movie, trigger, r.Hash)
}

func (s *catalogSource) bindCinematics(d Definition) (Definition, error) {
	if len(d.meta.quests) == 0 || d.ID == 100000194 || d.ID == 100000325 {
		return d, nil
	}
	var identity []string
	for i := range d.Rooms {
		r := &d.Rooms[i]
		var bindings []opening.CinematicDestroy
		var err error
		switch {
		case d.ID == 21 && d.MazeIndex == 2 && slices.Equal(d.meta.quests, []uint16{3198}) && r.MapID == 53363 && r.X == 3 && r.Y == 0:
			bindings, err = s.captiveBeliever(*r)
		case d.ID == 239 && d.MazeIndex == 2 && r.MapID == 70168 && r.CinematicLayer != nil && r.CinematicLayer.MapID == 70171:
			bindings, err = s.prepareCinematics(*r.CinematicLayer, false)
			if err == nil && (len(bindings) != 1 || bindings[0].Index != 0 || bindings[0].Movie != 426 || bindings[0].TriggerIndex != nil) {
				err = fmt.Errorf("Noire cinematic layer lacks movie426 destruction")
			}
		case len(r.Monsters) == 0 || len(r.MapAliases) > 0 || r.CinematicLayer != nil && r.ScriptTransfer == nil ||
			slices.ContainsFunc(r.Monsters, func(m opening.Monster) bool { return m.Code == 107000909 }):
			continue
		default:
			upper := d.ID == 100000175 && d.MazeIndex == 1 && slices.Equal(d.meta.quests, []uint16{6413}) && r.MapID == 100000443 && r.X == 0 && r.Y == 0
			key := cinematicKey{r.MapID, upper}
			var ok bool
			bindings, ok = s.cinematics[key]
			if !ok {
				bindings, err = s.prepareCinematics(*r, upper)
				if err == nil {
					s.cinematics[key] = bindings
				}
			}
		}
		if err != nil {
			return d, err
		}
		if len(bindings) == 0 {
			continue
		}
		r.CinematicDestroys = bindings
		if r.X == d.Start.X && r.Y == d.Start.Y {
			d.Start = *r
		}
		for _, b := range bindings {
			identity = append(identity, fmt.Sprintf("%d,%d,%d:%s", r.X, r.Y, r.MapID, ruleText(b)))
		}
	}
	if len(identity) > 0 {
		d.ResourceHash = hashText(d.ResourceHash + "\n" + strings.Join(identity, "\n"))
	}
	return d, nil
}

func actionPath(mapPath, relative string) string {
	return mapPath[:strings.LastIndex(mapPath, "/")+1] + relative
}

func cinematicHash(mapPath, m, actionPath, action, moviePath, movie string) string {
	return hashText(strings.Join([]string{mapPath, hashText(m), actionPath, hashText(action), moviePath, hashText(movie)}, "\n"))
}

func (s *catalogSource) captiveBeliever(room opening.Room) ([]opening.CinematicDestroy, error) {
	if len(room.Monsters) != 10 {
		return nil, nil
	}
	last := room.Monsters[9]
	if last.Code != 61103 || last.Type != 0 || last.Dummy || last.AiScriptIndex != nil {
		return nil, nil
	}
	mapPath := s.maps[int64(room.MapID)]
	text, err := s.readMap(room.MapID, false)
	if err != nil {
		return nil, err
	}
	ref := basicAction.FindAllStringSubmatch(text, -1)
	if len(ref) != 1 || strings.ReplaceAll(ref[0][1], "\\", "/") != "Action/53363.act" {
		return nil, nil
	}
	path := actionPath(mapPath, "Action/53363.act")
	action, err := s.a.ReadText(path, budget)
	if err != nil {
		return nil, err
	}
	const expected = "[TRIGGER] [LIMIT] 1 [DELAY DO BEHAVIOR] [ME] 0 100 [/TRIGGER] " +
		"[BEHAVIOR] [CINEMATIC] 202 [/BEHAVIOR] [TRIGGER] [ON END CINEMATIC] 202 " +
		"[WHICH] [MONSTER] [CHECKUP] [IS INDEX] 61103 [/IS INDEX] [/CHECKUP] " +
		"[DO BEHAVIOR] [CHECKUP OBJECT] 1 [/TRIGGER] [BEHAVIOR] [DESTROY] [/BEHAVIOR]"
	if strings.Join(strings.Fields(action), " ") != expected {
		return nil, nil
	}
	moviePath := s.movies[202]
	movie, err := s.a.ReadText(moviePath, budget)
	if err != nil {
		return nil, err
	}
	return []opening.CinematicDestroy{{Index: 9, Movie: 202, Hash: cinematicHash(mapPath, text, path, action, moviePath, movie)}}, nil
}

func uintToken(s string, bits int) uint64 {
	n, _ := strconv.ParseUint(s, 10, bits)
	return n
}

func (s *catalogSource) prepareCinematics(room opening.Room, upper bool) ([]opening.CinematicDestroy, error) {
	mapPath := s.maps[int64(room.MapID)]
	text, err := s.readMap(room.MapID, false)
	if err != nil {
		return nil, err
	}
	references := basicAction.FindAllStringSubmatch(text, -1)
	if len(references) != 1 {
		return nil, nil
	}
	relative := strings.ReplaceAll(references[0][1], "\\", "/")
	if strings.Contains(relative, ":") || slices.ContainsFunc(strings.Split(relative, "/"), func(part string) bool { return part == "" || part == "." || part == ".." }) {
		return nil, nil
	}
	path := actionPath(mapPath, relative)
	action, err := s.a.ReadText(path, budget)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	blocks := placements.FindAllStringSubmatch(text, -1)
	if len(blocks) != 1 {
		return nil, nil
	}
	rows := cinematicMonsterRow.FindAllStringSubmatch(blocks[0][1], -1)
	n := 0
	for _, m := range room.Monsters {
		if m.AiScriptIndex == nil {
			n++
		}
	}
	if len(rows) != n {
		return nil, nil
	}
	behaviors := pvftext.Blocks(action, "BEHAVIOR")
	rules := map[byte]opening.CinematicDestroy{}
	ambiguous := map[byte]bool{}
	addRule := func(index byte, movieID uint32, trigger *byte, hash string) {
		rule := opening.CinematicDestroy{Index: index, Movie: movieID, TriggerIndex: trigger, Hash: hash}
		if previous, ok := rules[index]; ok && ruleText(previous) != ruleText(rule) {
			ambiguous[index] = true
			return
		}
		rules[index] = rule
	}
	eligibleIndex := func(token string) (byte, bool) {
		n, err := strconv.ParseUint(token, 10, 8)
		if err != nil || int(n) >= len(rows) {
			return 0, false
		}
		m := room.Monsters[n]
		return byte(n), m.AiScriptIndex == nil && m.Code != 107000909 && strconv.FormatUint(uint64(m.Code), 10) == rows[n][1]
	}
	for _, trigger := range pvftext.Blocks(action, "TRIGGER") {
		start, dead := entryTrigger.FindStringSubmatch(trigger), deadTrigger.FindStringSubmatch(trigger)
		delayed, group, hp := delayedTrigger.FindStringSubmatch(trigger), groupTrigger.FindStringSubmatch(trigger), hpTrigger.FindStringSubmatch(trigger)
		var character []string
		if room.MapID == 57791 || room.MapID == 57792 {
			character = characterTrigger.FindStringSubmatch(trigger)
		}
		n := 0
		for _, m := range [][]string{start, dead, delayed, group, hp, character} {
			if m != nil {
				n++
			}
		}
		if n != 1 {
			continue
		}
		var dependency *byte
		var id uint64
		behavior := singleBehavior
		isGroup := group != nil || hp != nil
		switch {
		case group != nil:
			id = uintToken(group[1], 31)
			behavior = groupBehavior
		case hp != nil:
			code := uint32(uintToken(hp[1], 32))
			if code == 0 || !slices.ContainsFunc(room.Monsters, func(m opening.Monster) bool { return m.Code == code && m.AiScriptIndex == nil }) {
				continue
			}
			id = uintToken(hp[3], 31)
			behavior = groupBehavior
		case dead != nil:
			code := uint32(uintToken(dead[1], 32))
			targets := []byte{}
			for i, m := range room.Monsters {
				if m.Code == code && m.Type == 3 && !m.Dummy && m.AiScriptIndex == nil {
					targets = append(targets, byte(i))
				}
			}
			if len(targets) != 1 {
				continue
			}
			dependency = &targets[0]
			id = uintToken(dead[2], 31)
		case character != nil:
			id = uintToken(character[1], 31)
			behavior = entryBehavior
		case delayed != nil:
			id = uintToken(delayed[1], 31)
		case start != nil:
			id = uintToken(start[1], 31)
		}
		if id >= uint64(len(behaviors)) {
			continue
		}
		ref := behavior.FindStringSubmatch(behaviors[id])
		if ref == nil {
			continue
		}
		movieID := uint32(uintToken(ref[1], 32))
		moviePath := s.movies[int64(movieID)]
		if movieID == 0 || moviePath == "" {
			continue
		}
		movie, err := s.a.ReadText(moviePath, budget)
		if err != nil {
			return nil, err
		}
		maps := movieMap.FindAllStringSubmatch(movie, -1)
		if len(maps) != 1 || uintToken(maps[0][1], 32) != uint64(room.MapID) {
			continue
		}
		hash := cinematicHash(mapPath, text, path, action, moviePath, movie)
		if isGroup {
			for _, g := range groupDestroy.FindAllStringSubmatch(movie, -1) {
				if g[5] != "0" {
					continue
				}
				for _, member := range groupMember.FindAllStringSubmatch(g[2], -1) {
					if index, ok := eligibleIndex(member[1]); ok {
						addRule(index, movieID, nil, hash)
					}
				}
			}
		}
		for _, row := range singleDestroy.FindAllStringSubmatch(movie, -1) {
			if (isGroup || character != nil || delayed != nil) && row[4] != "0" {
				continue
			}
			index, ok := eligibleIndex(row[1])
			if !ok || dependency != nil && *dependency == index {
				continue
			}
			m := room.Monsters[index]
			if !isGroup && m.Code == 107009972 && !(upper && index == 0 && movieID == 13803 && dependency == nil && m.Level == 100 && m.Type == 3 && m.Dummy && m.BlocksRoom) {
				continue
			}
			addRule(index, movieID, dependency, hash)
		}
	}
	rulesOut := []opening.CinematicDestroy{}
	for _, index := range slices.Sorted(maps.Keys(rules)) {
		if !ambiguous[index] {
			rulesOut = append(rulesOut, rules[index])
		}
	}
	return rulesOut, nil
}
