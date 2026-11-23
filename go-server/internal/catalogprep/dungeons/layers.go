package dungeons

import (
	"hash"
	"slices"
	"strings"

	"s9a1/server/internal/opening"
)

type layerKey struct {
	dungeon, mapID uint32
	maze           int
	x, y           byte
}

type sceneryBinding struct {
	mapID                        uint32
	other                        uint32
	mainline, exact, zeroPassive bool
}

var scenery = map[layerKey]sceneryBinding{
	{53, 76383, 2, 1, 2}:             {mapID: 76384, mainline: true, exact: true, zeroPassive: true},
	{7157, 70192, 0, 6, 0}:           {mapID: 70350, exact: true, zeroPassive: true},
	{193, 70484, 3, 3, 1}:            {mapID: 70487, mainline: true, exact: true, zeroPassive: true},
	{9609, 655500, 0, 1, 0}:          {mapID: 655501},
	{9610, 655507, 0, 1, 0}:          {mapID: 655508, exact: true},
	{9611, 655571, 0, 2, 0}:          {mapID: 655572, exact: true},
	{100000326, 100001167, -1, 0, 0}: {mapID: 100001168},
}

var lateScenery = map[layerKey]sceneryBinding{
	{100000006, 292106929, -1, 4, 2}: {mapID: 100000295},
	{291100432, 292106830, -1, 1, 2}: {mapID: 100000294},
	{291100429, 100000489, -1, 4, 1}: {mapID: 100000446, other: 292106782},
	{291100428, 292106756, -1, 0, 0}: {mapID: 100000506, other: 100000507},
}

func (s *mapSource) layers(p *input, r *opening.Room, id uint32, maze int, meta nativeMeta, opt layoutOptions, h hash.Hash) {
	k := layerKey{id, r.MapID, maze, r.X, r.Y}
	wild := layerKey{id, r.MapID, -1, r.X, r.Y}
	if id == 9610 && maze == 0 && r.MapID == 655507 || id == 9611 && maze == 0 && r.MapID == 655571 {
		for i := range r.Monsters {
			m := &r.Monsters[i]
			if m.Type == 3 && m.Dummy {
				m.BlocksRoom = false
			}
		}
	}
	if id == 100002476 && maze == 0 && r.MapID == 100003288 && p.require(len(r.Monsters) > 0, "missing fixed room object") {
		r.Monsters[0].BlocksRoom = false
	}
	if !opt.mainline {
		s.skyCompanions(p, r, id, maze, meta.basis, opt.plainMapText)
	}
	bind, ok := scenery[k]
	if !ok {
		bind, ok = scenery[wild]
	}
	if ok && (!bind.mainline || opt.mainline) {
		if !bind.exact || p.require(slices.Equal(r.MapAliases, []uint32{bind.mapID}), "cinematic layer declaration changed") {
			if slices.Contains(r.MapAliases, bind.mapID) {
				text := s.makeLayer(p, r, h, opt, bind.mapID, bind.zeroPassive, false, 0)
				if id == 7157 || id == 193 {
					p.require(strings.TrimSpace(p.field(text, "monster", false)) == "" &&
						slices.Contains(r.CinematicLayer.PassiveObjectCodes, 69292), "terminal scene passive object declaration changed")
				}
			}
		}
	}
	switch {
	case opt.mainline && k == (layerKey{100000197, 100000485, 0, 2, 0}):
		if p.require(slices.Equal(r.MapAliases, []uint32{100000656}) && len(r.Monsters) == 1 && r.Monsters[0].Code == 109011083,
			"Wisdom story battle declaration changed") {
			text := s.makeLayer(p, r, h, opt, 100000656, true, false, 0)
			s.scriptTerminal(p, r, text, 107002770, 135, true)
		}
	case opt.mainline && k == (layerKey{239, 70168, 2, 5, 0}):
		if p.require(slices.Equal(r.MapAliases, []uint32{70171}) && len(r.Monsters) == 1 && r.Monsters[0].Code == 70150,
			"Noire cinematic base actor declaration changed") {
			text := s.makeLayer(p, r, h, opt, 70171, true, true, meta.basis)
			s.scriptTerminal(p, r, text, 70150, r.Monsters[0].Level, false)
			p.require(len(r.CinematicLayer.Companions) == 1 && r.CinematicLayer.Companions[0].Code == 6517, "Noire layer companion declaration changed")
			r.MapAliases = append(r.MapAliases, 70171)
		}
	case k == (layerKey{100000194, 100000355, 0, 1, 0}):
		if p.require(slices.Equal(r.MapAliases, []uint32{100000442}) && len(r.Monsters) == 1 && r.Monsters[0].Code == 107009972,
			"Awakening layer terminal declaration changed") {
			text := s.makeLayer(p, r, h, opt, 100000442, true, false, 0)
			s.scriptTerminal(p, r, text, 107009972, r.Monsters[0].Level, true)
			r.MapAliases = append(r.MapAliases, 100000442)
		}
	case opt.mainline && id == 24 && maze == 2 && r.MapID == 53480:
		if p.require(slices.Equal(r.MapAliases, []uint32{53501}), "White Night cinematic layer declaration changed") {
			text := s.makeLayer(p, r, h, opt, 53501, true, true, meta.basis)
			c := r.CinematicLayer.Companions
			p.require(strings.TrimSpace(p.field(text, "monster", false)) == "" && len(c) == 2 && c[0].Code == 55423 && c[1].Code == 2010,
				"White Night cinematic must contain only its two friendly APCs")
		}
	case id == 7123 && r.X == 3 && r.Y == 0 && r.MapID != 57792 && len(r.MapAliases) == 1:
		if p.require(r.MapAliases[0] == 53663, "First awakening transition layer declaration changed") {
			s.makeLayer(p, r, h, opt, 53663, true, false, 0)
		}
	case wild == (layerKey{100000326, 100001177, -1, 2, 0}) && slices.Contains(r.MapAliases, 100001178):
		text := s.makeLayer(p, r, h, opt, 100001178, false, false, 0)
		// The native story layer normalizes [dummy] and [displayhuntdummy] out of the final spawn.
		room := s.parseRoom(p, id, 100001178, text, meta.basis, layoutOptions{mainline: true, objectTypes: opt.objectTypes})
		for _, m := range room.Monsters {
			if m.AiScriptIndex == nil && (m.Type == 0 || m.Type == 3) {
				m.Dummy = false
				r.CinematicLayer.Monsters = append(r.CinematicLayer.Monsters, m)
			}
		}
		p.require(len(r.CinematicLayer.Monsters) > 0, "Dungeon story layer has no monsters")
	}
	if id >= 100000326 && r.CinematicLayer == nil && len(r.MapAliases) > 0 {
		s.makeLayer(p, r, h, opt, r.MapAliases[0], false, false, 0)
	}
	if opt.mainline && k == (layerKey{156, 70193, 1, 0, 4}) {
		aliases := slices.Clone(r.MapAliases)
		slices.Sort(aliases)
		if p.require(slices.Equal(aliases, []uint32{70202, 70351}), "Mushroom Garden cinematic layer declaration changed") {
			s.makeLayer(p, r, h, opt, 70202, true, false, 0)
			dst := r.CinematicLayer
			s.makeLayer(p, r, h, opt, 70351, true, false, 0)
			dst.PassiveObjectCodes = append(dst.PassiveObjectCodes, r.CinematicLayer.PassiveObjectCodes...)
			dst.NpcTemplates = append(dst.NpcTemplates, r.CinematicLayer.NpcTemplates...)
			slices.Sort(dst.PassiveObjectCodes)
			slices.Sort(dst.NpcTemplates)
			dst.PassiveObjectCodes = slices.Compact(dst.PassiveObjectCodes)
			dst.NpcTemplates = slices.Compact(dst.NpcTemplates)
			r.CinematicLayer = dst
		}
	}
	if bind, ok := lateScenery[wild]; ok && slices.Contains(r.MapAliases, bind.mapID) {
		s.makeLayer(p, r, h, opt, bind.mapID, false, false, 0)
		r.MapAliases = slices.DeleteFunc(r.MapAliases, func(v uint32) bool { return v == bind.other })
	}
}

func (s *mapSource) makeLayer(p *input, r *opening.Room, h hash.Hash, opt layoutOptions, id uint32, allowZero, withCompanions bool, basis byte) string {
	text, err := s.readMap(id, opt.plainMapText)
	if err != nil {
		p.require(false, err.Error())
	}
	appendMapHash(h, id, text)
	child := &opening.Room{X: r.X, Y: r.Y, MapID: id, PassiveObjectCodes: p.passives(text, allowZero || opt.mainline), NpcTemplates: p.npcs(text)}
	if withCompanions {
		child.Companions = s.companions(p, child, text, basis)
		p.require(len(child.Monsters) == 0, "cinematic companions must not create hostile AIC")
		for i := range child.Companions {
			child.Companions[i].CreditsPlayerKills = false
		}
	}
	r.CinematicLayer = child
	r.MapAliases = slices.DeleteFunc(r.MapAliases, func(v uint32) bool { return v == id })
	return text
}

func (s *mapSource) scriptTerminal(p *input, r *opening.Room, body string, code uint32, level byte, dummy bool) {
	room := s.parseRoom(p, 0, r.CinematicLayer.MapID, body, level, layoutOptions{mainline: true})
	p.require(len(room.Monsters) == 1 && room.Monsters[0].Code == code && room.Monsters[0].Type == 3 &&
		room.Monsters[0].Dummy == dummy, "cinematic shared terminal source changed")
	r.CinematicLayer.Monsters = []opening.Monster{{Code: code, Level: level, Type: 3, Dummy: dummy, BlocksRoom: true}}
}

func (s *mapSource) skyCompanions(p *input, r *opening.Room, dungeon uint32, maze int, basis byte, plain bool) {
	var expected []uint32
	switch [3]uint32{dungeon, uint32(maze), r.MapID} {
	case [3]uint32{11, 2, 57879}, [3]uint32{11, 3, 57894}:
		expected = []uint32{65006}
	case [3]uint32{12, 4, 57911}:
		expected = []uint32{55417}
	case [3]uint32{13, 2, 57912}, [3]uint32{13, 2, 57918}:
		expected = []uint32{2012}
	case [3]uint32{13, 3, 57926}:
		expected = []uint32{6114, 2012}
	default:
		return
	}
	text, err := s.readMap(r.MapID, plain)
	if err != nil {
		p.require(false, err.Error())
		return
	}
	body := p.field(text, "ai character", true)
	rows := aicRow.FindAllStringSubmatchIndex(body, -1)
	p.completeRows(body, rows, "Sky companion requires one complete explicit AIC placement block")
	if !p.require(len(rows) == len(expected) && basis != 0, "Sky AIC rows differ from the valid placements") {
		return
	}
	for i, at := range rows {
		row := matchFields(body, at)
		p.require(p.id(p.number(row[1], 64)) == expected[i] && row[5] == "character" && row[6] == "normal" &&
			row[7] == "0" && row[8] == "0", "Sky AIC rows differ from the valid placements")
		r.Companions = append(r.Companions, opening.Companion{Code: expected[i], Level: basis, Type: 5, ScriptIndex: byte(i)})
	}
}
