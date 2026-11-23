package dungeons

import (
	"fmt"
	"regexp"
	"slices"
	"strings"

	"s9a1/server/internal/catalogprep/pvftext"
	"s9a1/server/internal/opening"
)

const stormPolicy = "LocalStormLaneRoutes/v2"

func BuildStorm(a Archive, xp *Experience) (Layouts, error) {
	s, err := newCatalogSource(a, xp, nil, nil)
	if err != nil {
		return Layouts{}, err
	}
	return s.storm()
}

func (s *catalogSource) storm() (Layouts, error) {
	var out Layouts
	world, err := s.worldText(182)
	if err != nil {
		return out, err
	}
	raw, err := s.a.ReadText("etc/dungeonetc/stormlane.etc", budget)
	if err != nil {
		return out, err
	}
	p := &input{}
	normal := p.numbers(p.field(raw, "dungeon info", true))
	hidden := p.numbers(p.field(raw, "hidden dungeon info", true))
	if !p.require(len(normal) == 264 && len(hidden) == 24 && p.scalar(raw, "hidden dungeon rate", -1) == 10 &&
		p.scalar(raw, "recently enter dungeon count", -1) == 7, "Storm Lane route identities or history policy changed") {
		return out, p.err
	}
	ids := make([]uint32, 0, 144)
	for i, row := range [][]int64{normal, hidden} {
		for j := 0; j < len(row); j += 2 {
			expected := int64(100000139 + j/2)
			if i == 0 {
				expected = 100000007 + int64(j/2)
				if j == 0 {
					expected = 100000003
				}
			}
			p.require(row[j] == 4 && row[j+1] == expected, "Storm Lane route identities or history policy changed")
			ids = append(ids, p.id(row[j+1]))
		}
	}
	if p.err != nil {
		return out, p.err
	}
	selectors, err := worldSelectors(world)
	if err != nil {
		return out, err
	}
	for _, id := range ids {
		d, err := s.stormLayout(id, world, raw, selectors)
		if err != nil {
			return out, fmt.Errorf("storm dungeon %d: %w", id, err)
		}
		out.Definitions = append(out.Definitions, d)
	}
	return out, nil
}

func (s *catalogSource) stormLayout(id uint32, world, routes string, selectors []selectorRow) (Layout, error) {
	text, err := s.dungeonText(id)
	if err != nil {
		return Layout{}, err
	}
	p := &input{}
	var count int
	for _, q := range selectors {
		if q.Dungeon == id {
			count++
			p.require(!q.InProgress && q.Quest == 12172, "Storm Lane requires completed quest 12172")
		}
	}
	if !p.require(count == 1 && tagPresent(text, "storm lane dungeon") && p.field(text, "dungeon mode script", true) == "`battleship`" &&
		p.scalar(text, "designate dungeon difficulty", -1) == 4 && p.scalar(text, "difficulty", -1) == 4 &&
		p.scalar(text, "minimum required level", -1) == 96 && p.scalar(text, "basis level", -1) == 135 && len(mazeBlocks(text)) == 1,
		"Storm Lane lacks its fixed native entrance qualification") || p.err != nil {
		return Layout{}, p.err
	}
	d, err := s.definition(id, text, 0, layoutOptions{mainline: true})
	if err != nil {
		return Layout{}, err
	}
	if err := reachable(d.Dungeon); err != nil {
		return Layout{}, err
	}
	bossIdx := slices.IndexFunc(d.Rooms, func(r opening.Room) bool { return r.X == d.BossX && r.Y == d.BossY })
	bosses := 0
	for _, m := range d.Rooms[bossIdx].Monsters {
		if m.Type == 3 && !m.Dummy {
			bosses++
		}
	}
	if !p.require(len(d.meta.quests) == 0 && d.EntryFatigueCost == 8 && d.RoomFatigueCost == 0 && !d.meta.disableClear && bosses == 1,
		"Storm Lane maze lacks its boss and entry-only fatigue policy") {
		return Layout{}, p.err
	}
	var cleanHash, arrivalHash []string
	for i := range d.Rooms {
		r := &d.Rooms[i]
		var dummies []byte
		for i, m := range r.Monsters {
			if m.Dummy {
				dummies = append(dummies, byte(i))
			}
		}
		if len(dummies) == 0 {
			continue
		}
		if len(dummies) != 1 {
			return Layout{}, fmt.Errorf("storm room has multiple script dummies")
		}
		r.StormCleanup, err = s.stormCleanup(d.ResourceHash, *r, dummies[0])
		if err != nil {
			return Layout{}, err
		}
		cleanHash = append(cleanHash, r.StormCleanup.Hash)
	}
	if id >= 100000139 && id <= 100000150 {
		boss := &d.Rooms[bossIdx]
		boss.StormArrival, err = s.stormArrival(d, *boss, len(cleanHash))
		if err != nil {
			return Layout{}, err
		}
		arrivalHash = append(arrivalHash, boss.StormArrival.Hash)
	}
	d.Start = d.Rooms[slices.IndexFunc(d.Rooms, func(r opening.Room) bool { return r.X == d.Start.X && r.Y == d.Start.Y })]
	identity := []string{stormPolicy, world, routes, d.ResourceHash, strings.Join(cleanHash, "\n"), strings.Join(arrivalHash, "\n")}
	d.ResourceHash = hashText(strings.Join(identity, "\n"))
	d.meta.selectable = []byte{4}
	return s.layout(182, d, "storm", nil, []uint16{12172}, nil, nil)
}

func normalizeScript(text string) string {
	return strings.Join(strings.Fields(text), " ")
}

func (s *catalogSource) stormCleanup(hash string, r opening.Room, index byte) (*opening.StormCleanup, error) {
	text, err := s.readMap(r.MapID, false)
	if err != nil {
		return nil, err
	}
	path := actionPath(s.maps[int64(r.MapID)], "Action/Dummy_Boss_Kill.act")
	action, err := s.a.ReadText(path, budget)
	if err != nil {
		return nil, err
	}
	const excluded = "107000911 61150 61192 69037 109010780 70034 251 17 61143 61770 61750 61128"
	const expected = "[TRIGGER] [CHECK TIME] 500 [WHICH] [MONSTER] [CHECKUP] [IS INDEX] " + excluded +
		" [/IS INDEX] [/CHECKUP] [CHECKED NO] [=] 0 [DO BEHAVIOR] [ME] 0 [/TRIGGER]" +
		" [BEHAVIOR] [TRIGGER] 1 [ON] [/BEHAVIOR] [TRIGGER] [ENABLE] [OFF] [WHICH] [MONSTER]" +
		" [CHECKUP] [IS TEAM] [MONSTER TEAM] [/IS TEAM] [/CHECKUP] [DO BEHAVIOR] [CHECKUP OBJECT] 1 [/TRIGGER]" +
		" [BEHAVIOR] [DESTROY] [TRIGGER] 1 [OFF] [/BEHAVIOR]"
	p := &input{}
	mon := r.Monsters[index]
	ignored := p.numbers(excluded)
	p.require(mon.Type == 3 && mon.BlocksRoom && mon.AiScriptIndex == nil && slices.Contains(ignored, int64(mon.Code)) &&
		normalizeScript(action) == expected && p.field(text, "basic action", true) == "`Action/Dummy_Boss_Kill.act`",
		"Storm Lane dummy cleanup differs from its bound ACT trigger")
	var required []byte
	for i, m := range r.Monsters {
		if i == int(index) {
			continue
		}
		p.require(!m.Dummy && !slices.Contains(ignored, int64(m.Code)), "Storm Lane dummy cleanup suppresses another actor")
		required = append(required, byte(i))
	}
	return &opening.StormCleanup{Index: index, Prerequisites: required, Hash: hashText(hash + "\n" + action)}, p.err
}

var behaviorRange = regexp.MustCompile(`(?ms)^\[BEHAVIOR\]\s*(.*?)^\[/BEHAVIOR\]`)

func (s *catalogSource) stormArrival(d Definition, boss opening.Room, cleanups int) (*opening.StormArrival, error) {
	p := &input{}
	if !p.require(len(d.Rooms) == 6 && boss.MapID == 100000218 && len(boss.Monsters) == 7 && cleanups == 1 &&
		slices.ContainsFunc(d.Rooms, func(r opening.Room) bool { return r.MapID == 100000447+d.ID-100000139 && r.StormCleanup != nil }),
		"Storm Lane hidden route lacks its fourth-room Prion") {
		return nil, p.err
	}
	tail := 0
	for _, r := range d.Rooms {
		if r.MapID >= 100000218 && r.MapID <= 100000220 {
			p.require(slices.Equal(r.PassiveObjectCodes, []uint32{109051345}), "Prion map passive source changed")
		}
		if r.MapID == 100000219 || r.MapID == 100000220 {
			p.require(r.X == boss.X+byte(r.MapID-100000218) && r.Y == boss.Y && len(r.Monsters) == 0 && len(r.Companions) == 0,
				"Prion native tail is not empty")
			tail++
		}
	}
	p.require(tail == 2, "missing Prion native tail")
	for i, m := range boss.Monsters {
		ok := m.Code == 63035 || m.Code == 63036
		if i == 6 {
			ok = m.Code == 109010261 && m.Type == 3
		} else {
			ok = ok && m.Type == 0
		}
		p.require(ok && !m.Dummy && m.AiScriptIndex == nil, "Prion native actor source changed")
	}
	const root = "monster/kcontents3/stormlane/prion/"
	monster, err := s.a.ReadText(root+"prion.mob", budget)
	if err != nil {
		return nil, err
	}
	arrival, err := s.a.ReadText(root+"action/incomming_1.act", budget)
	if err != nil {
		return nil, err
	}
	intro, err := s.a.ReadText(root+"action/incomming.act", budget)
	if err != nil {
		return nil, err
	}
	behaviors := pvftext.Blocks(arrival, "BEHAVIOR")
	conditions := behaviorRange.ReplaceAllString(arrival, "")
	triggers := pvftext.Blocks(conditions, "TRIGGER")
	trigger := "[FRAME] 2 2 [WHICH] [ALL MONSTER TEAM] [CHECKUP] [DISTANCE] [>=] 10 [IS NOT INDEX] 109010261" +
		" [/IS NOT INDEX] [/CHECKUP] [DO BEHAVIOR] [CHECKUP OBJECT] 2 [DO BEHAVIOR] [ME] 3"
	p.require(normalizeScript(p.field(monster, "etc action", true)) == "`Action/Incomming.act` `Action/Incomming_1.act`" &&
		len(behaviors) == 4 && normalizeScript(behaviors[2]) == "[SET HP] 0" &&
		len(triggers) == 3 && normalizeScript(triggers[2]) == trigger, "Prion arrival trigger or zero-HP behavior changed")
	introB := pvftext.Blocks(intro, "BEHAVIOR")
	introT := pvftext.Blocks(intro, "TRIGGER")
	behavior := 2
	if boss.X == 3 {
		behavior = 3
	} else if boss.Y == 1 {
		behavior = 4
	}
	cell := fmt.Sprintf("%d %d", boss.X, boss.Y)
	wantTrigger := fmt.Sprintf("[LIMIT] 1 [IS GRID] %s [/IS GRID] [DO BEHAVIOR] [ME] %d", cell, behavior)
	wantBehavior := "[SET MINIMAP ICON] " + cell + " [IMAGE] `Interface2/minimap/dungeon/TerritoryCombat.img` 22" +
		" [SET MINIMAP ICON] 4 1 [NONE] [SET MINIMAP ICON] 4 0 [NONE] [SET MINIMAP ICON] 5 0 [NONE]"
	contains := func(rows []string, expected string) bool {
		return slices.ContainsFunc(rows, func(r string) bool { return normalizeScript(r) == expected })
	}
	p.require(len(introB) == 5 && len(introT) == 5 && contains(introT, wantTrigger) && contains(introB, wantBehavior), "Prion intro marker no longer matches its native cell")
	text, err := s.readMap(100000218, false)
	if err != nil {
		return nil, err
	}
	const spawns = "63035 1 0 381 206 0 1 1 `[fixed]` `[normal]` 63036 1 0 481 196 0 1 1 `[fixed]` `[normal]`" +
		" 63035 1 0 653 198 0 1 1 `[fixed]` `[normal]` 63036 1 0 517 361 0 1 1 `[fixed]` `[normal]`" +
		" 63036 1 0 671 369 0 1 1 `[fixed]` `[normal]` 63035 1 0 440 368 0 1 1 `[fixed]` `[normal]`" +
		" 109010261 1 0 504 275 0 1 1 `[fixed]` `[boss]`"
	p.require(normalizeScript(p.field(text, "monster", true)) == spawns, "Prion targets differ from ten-pixel exclusion")
	return &opening.StormArrival{Boss: 6, Targets: []byte{0, 1, 2, 3, 4, 5}, Hash: hashText(strings.Join([]string{d.ResourceHash, monster, arrival, intro}, "\n"))}, p.err
}
