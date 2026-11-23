package dungeons

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"hash"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"s9a1/server/internal/catalogprep/pvftext"
	"s9a1/server/internal/opening"
)

type mapSource struct {
	a                        Archive
	dungeons, maps, monsters map[int64]string
	aics                     map[int64]string
	mapText                  map[mapSourceKey]string
	objects                  map[uint32]bool
}

type mapSourceKey struct {
	id    uint32
	plain bool
}
type layoutOptions struct{ mainline, plainMapText, objectTypes bool }

func newMapSource(a Archive) (*mapSource, error) {
	s := &mapSource{a: a, mapText: map[mapSourceKey]string{}, objects: map[uint32]bool{}}
	for _, row := range []struct {
		category string
		index    *map[int64]string
	}{{"dungeon", &s.dungeons}, {"map", &s.maps}, {"monster", &s.monsters}} {
		index, err := a.ReadIndex(row.category, budget)
		if err != nil {
			return nil, fmt.Errorf("dungeon preparation %s index: %w", row.category, err)
		}
		*row.index = index
	}
	return s, nil
}

func (s *mapSource) readMap(id uint32, plain bool) (string, error) {
	key := mapSourceKey{id, plain}
	if text, ok := s.mapText[key]; ok {
		return text, nil
	}
	path := s.maps[int64(id)]
	if path == "" {
		return "", fmt.Errorf("dungeon map %d is absent from PVF", id)
	}
	read := s.a.ReadDungeonMapText
	if plain {
		read = s.a.ReadText
	}
	text, err := read(path, budget)
	if err != nil {
		return "", err
	}
	s.mapText[key] = text
	return text, nil
}

type nativeMeta struct {
	hell, tower                                   bool
	difficultyItems                               [][]uint32
	multiplier                                    json.Number
	basis                                         byte
	disableExperience, disableDrops, disableClear bool
	selectable                                    []byte
	quests                                        []uint16
	story                                         []uint32
	named                                         []uint32
}

var (
	mazeMarker    = regexp.MustCompile(`(?im)^\[maze info\]\s*$`)
	signedInteger = regexp.MustCompile(`-?\d+`)
	mapSpec       = regexp.MustCompile("`(map|boss|layered)`\\s+(\\d+)\\s+(\\d+)\\s+(\\d+(?:\\s+\\d+)*)")
	monsterRow    = regexp.MustCompile("(\\d+)\\s+(\\d+)\\s+(-?\\d+)\\s+(-?\\d+)\\s+(-?\\d+)\\s+(-?\\d+)\\s+(\\d+)\\s+(\\d+)\\s+`([^`]+)`\\s+`([^`]+)`(?:\\s+`([^`]+)`)?")
	npcRow        = regexp.MustCompile("(\\d+)\\s+`\\[(?:left|right)\\]`\\s+-?\\d+\\s+-?\\d+\\s+-?\\d+")
)

func mazeBlocks(text string) []string { return mazeMarker.Split(text, -1)[1:] }

type input struct{ err error }

func (p *input) require(ok bool, reason string) bool {
	if !ok && p.err == nil {
		p.err = fmt.Errorf("%s", reason)
	}
	return ok
}

func (p *input) number(token string, bits int) int64 {
	v, err := strconv.ParseInt(token, 10, bits)
	p.require(err == nil, "invalid PVF number "+token)
	return v
}

func (p *input) numbers(text string) []int64 {
	fields := signedInteger.FindAllString(text, -1)
	out := make([]int64, len(fields))
	for i, v := range fields {
		out[i] = p.number(v, 64)
	}
	return out
}

func (p *input) width(v int64) byte {
	p.require(v >= 0 && v <= 255, "dungeon value exceeds native U8")
	return byte(v)
}

func (p *input) id(v int64) uint32 {
	p.require(v > 0 && v <= 1<<32-1, "dungeon identity exceeds native U32")
	return uint32(v)
}

func (p *input) field(text, tag string, needed bool) string {
	rows := pvftext.Blocks(text, tag)
	if len(rows) == 0 {
		p.require(!needed, "dungeon resource field missing "+tag)
		return ""
	}
	return rows[0]
}

func layoutTagValue(text, tag, pattern, fallback string) string {
	row := regexp.MustCompile(`(?m)^\[` + regexp.QuoteMeta(tag) + `\]\s*(` + pattern + `)`).FindStringSubmatch(text)
	if row != nil {
		return row[1]
	}
	return fallback
}

func tagPresent(text, tag string) bool {
	return regexp.MustCompile(`(?m)^\[` + regexp.QuoteMeta(tag) + `\]`).MatchString(text)
}

func (p *input) scalar(text, tag string, dflt int64) int64 {
	return p.number(layoutTagValue(text, tag, `-?\d+`, strconv.FormatInt(dflt, 10)), 64)
}

type cell struct{ x, y int64 }
type mapAlternatives struct {
	mapID, bossID uint32
	aliases       []uint32
}

func (s *mapSource) parse(id uint32, text string, which int, options layoutOptions) (opening.Dungeon, nativeMeta, error) {
	p := &input{}
	mazes := mazeBlocks(text)
	if which < 0 || which >= len(mazes) || id == 0 || id > 1<<31-1 {
		return opening.Dungeon{}, nativeMeta{}, fmt.Errorf("dungeon %d missing maze %d", id, which)
	}
	d, meta := s.parseMaze(p, id, text, mazes[which], which, options)
	if p.err != nil {
		return opening.Dungeon{}, nativeMeta{}, fmt.Errorf("dungeon %d maze %d: %w", id, which, p.err)
	}
	return d, meta, nil
}

func (s *mapSource) parseMaze(p *input, id uint32, text, maze string, which int, options layoutOptions) (opening.Dungeon, nativeMeta) {
	var d opening.Dungeon
	var meta nativeMeta
	start, boss := p.numbers(p.field(maze, "start map", true)), p.numbers(p.field(maze, "boss map", true))
	validPair := func(a []int64) bool { return len(a) == 2 || options.mainline && len(a) > 2 && len(a)%2 == 0 }
	if !p.require(validPair(start) && validPair(boss), "invalid maze start/boss identity") {
		return d, meta
	}
	coords := []cell{}
	groups := map[cell]mapAlternatives{}
	for _, row := range mapSpec.FindAllStringSubmatch(p.field(maze, "map specification", true), -1) {
		at := cell{p.number(row[2], 64), p.number(row[3], 64)}
		ids := p.numbers(row[4])
		group := groups[at]
		if row[1] == "layered" {
			for _, v := range ids {
				group.aliases = append(group.aliases, p.id(v))
			}
		} else {
			if group.mapID == 0 {
				group.mapID = p.id(ids[0])
				coords = append(coords, at)
			}
			if row[1] == "boss" {
				group.bossID = p.id(ids[0])
			}
		}
		groups[at] = group
	}
	for _, group := range groups {
		p.require(group.mapID != 0, "layered map has no materialized room")
	}
	layout := p.numbers(p.field(maze, "size", true))
	grid := strings.NewReplacer("`", "", " ", "", "\t", "", "\r", "", "\n", "", ",", "").Replace(p.field(maze, "greed", true))
	if !p.require(len(coords) > 0 && len(coords) <= 256 && len(layout) >= 2 && layout[0] > 0 && layout[0] <= 65536 &&
		layout[1] > 0 && layout[1] <= 65536 && layout[0]*layout[1] <= 65536, "invalid maze room count or size") {
		return d, meta
	}
	width, height := layout[0], layout[1]
	stride := int64(1)
	if len(grid) == int(width*height*2) {
		stride = 2
	}
	if !p.require(len(grid) == int(width*height*stride), "invalid maze gate grid") {
		return d, meta
	}
	meta.basis = p.width(p.scalar(text, "basis level", 1))
	meta.multiplier = json.Number(layoutTagValue(text, "experience increasing point", `-?\d+(?:\.\d+)?`, "1"))
	if _, ok := decimal(string(meta.multiplier)); !ok {
		p.require(false, "negative or invalid dungeon experience multiplier")
	}
	flags := p.numbers(p.field(text, "difficulty level", false))
	p.require(len(flags) == 0 || len(flags) == 5, "dungeon difficulty requires five flags")
	meta.selectable = []byte{}
	for i, n := range flags {
		p.require(n == 0 || n == 1, "invalid dungeon difficulty flag")
		if n == 1 {
			meta.selectable = append(meta.selectable, byte(i))
		}
	}
	d = opening.Dungeon{ID: id, MazeIndex: p.width(int64(which)), MinimumLevel: p.width(p.scalar(text, "minimum required level", 1)),
		BossX: p.width(boss[0]), BossY: p.width(boss[1]), Difficulty: p.width(p.scalar(text, "designate dungeon difficulty", 0)),
		EntryFatigueCost: 1, RoomFatigueCost: 1, Rooms: make([]opening.Room, 0, len(coords))}
	if tagPresent(text, "use fatigue only start dungeon") {
		costs := p.numbers(p.field(text, "use fatigue only start dungeon", true))
		if p.require(len(costs) == 1 && costs[0] >= 0 && costs[0] <= 65535, "invalid entry-only fatigue cost") {
			d.EntryFatigueCost, d.RoomFatigueCost = uint16(costs[0]), 0
		}
	}
	meta.disableExperience = tagPresent(text, "no experience monster and result")
	meta.disableDrops = tagPresent(text, "disable drop item")
	meta.disableClear = tagPresent(text, "disable clear reward")
	p.metadata(&meta, s.monsters, text, maze, d.Difficulty)
	h := sha256.New()
	dgnHash := sha256.Sum256([]byte(text))
	h.Write(dgnHash[:])
	for _, at := range coords {
		if !p.require(at.x < width && at.y < height, "room leaves the maze grid") {
			return d, meta
		}
		group := groups[at]
		mapID := group.mapID
		if at.x == boss[0] && at.y == boss[1] && group.bossID != 0 {
			mapID = group.bossID
		}
		i := (at.y*width + at.x) * stride
		gate := grid[i]
		p.require(gate >= 'A' && gate <= 'P' && (stride == 1 || grid[i+1] == gate), "unsupported gate grid shape")
		if options.mainline && s.maps[int64(mapID)] == "" {
			p.require(false, fmt.Sprintf("Mainline map index entry is missing: %d", mapID))
			return d, meta
		}
		mapText, err := s.readMap(mapID, options.plainMapText)
		if err != nil {
			p.require(false, err.Error())
			return d, meta
		}
		appendMapHash(h, mapID, mapText)
		room := s.parseRoom(p, id, mapID, mapText, meta.basis, options)
		room.X, room.Y, room.Gates, room.MapAliases = p.width(at.x), p.width(at.y), gate-'A', group.aliases
		s.layers(p, &room, id, which, meta, options, h)
		if mapID != group.mapID {
			room.MapAliases = append(room.MapAliases, group.mapID)
		}
		room.MapAliases = slices.DeleteFunc(room.MapAliases, func(v uint32) bool { return v == mapID })
		slices.Sort(room.MapAliases)
		room.MapAliases = slices.Compact(room.MapAliases)
		d.Rooms = append(d.Rooms, room)
		if at.x == start[0] && at.y == start[1] {
			d.Start = room
		}
	}
	p.require(d.Start.MapID != 0, "Ordinary dungeon start map is missing")
	p.require(slices.ContainsFunc(d.Rooms, func(r opening.Room) bool { return r.X == d.BossX && r.Y == d.BossY }), "Ordinary dungeon boss room is missing")
	d.ResourceHash = fmt.Sprintf("%X", h.Sum(nil))
	return d, meta
}

func appendMapHash(h hash.Hash, id uint32, text string) {
	h.Write(binary.LittleEndian.AppendUint32(nil, id))
	digest := sha256.Sum256([]byte(text))
	h.Write(digest[:])
}

func (s *mapSource) parseRoom(p *input, dungeonID, id uint32, text string, basis byte, options layoutOptions) opening.Room {
	r := opening.Room{MapID: id}
	teams := p.numbers(p.field(text, "monster team", false))
	body := p.field(text, "monster", false)
	expression := monsterRow
	if options.mainline {
		expression = mainlineMonsterRow
	}
	rows := expression.FindAllStringSubmatchIndex(body, -1)
	if options.mainline {
		p.completeRows(body, rows, "Mainline monster placement has unsupported conditional syntax")
	}
	for i, at := range rows {
		row := matchFields(body, at)
		code := p.id(p.number(row[1], 64))
		level := p.number(row[3], 64)
		if p.number(row[2], 64) != 0 {
			level += int64(basis)
		}
		if level <= 0 {
			level = int64(basis)
		}
		p.require(s.monsters[int64(code)] != "", "unknown dungeon monster code")
		tag := strings.Trim(row[10], "[]")
		dummy, displayDummy := tag == "dummy", false
		if options.mainline {
			if row[9] != "" {
				p.number(row[9], 32)
			}
			dummy, displayDummy = row[10] != "" || row[11] != "", row[11] != ""
			tag = strings.Trim(row[12]+row[13], "[]")
		} else if dummy {
			tag = strings.Trim(row[11], "[]")
			p.require(slices.Contains([]string{"normal", "boss", "champion", "super champion"}, tag), "dummy monster missing classification")
		} else {
			p.require(row[11] == "", "unsupported monster classification tail")
		}
		typ := map[string]byte{"boss": 3, "champion": 1, "super champion": 2}[tag]
		blocks := (i >= len(teams) || teams[i] != 0) && !(displayDummy && dungeonID >= 100000000) &&
			(!options.objectTypes || !s.monsterObject(code))
		r.Monsters = append(r.Monsters, opening.Monster{Code: code, Level: p.width(level), Type: typ, Dummy: dummy, BlocksRoom: blocks})
	}
	if options.mainline {
		r.Companions = s.companions(p, &r, text, basis)
	}
	p.require(len(r.Monsters)+len(r.Companions) <= 255, "map actor count exceeds native width")
	r.PassiveObjectCodes = p.passives(text, options.mainline)
	r.NpcTemplates = p.npcs(text)
	return r
}

var (
	mainlineMonsterRow = regexp.MustCompile("(\\d+)\\s+(\\d+)\\s+(-?\\d+)\\s+(-?\\d+)\\s+(-?\\d+)\\s+(-?\\d+)\\s+(\\d+)\\s+(\\d+)\\s+" +
		"`\\[fixed\\]`\\s+(?:`\\[NPC\\]`\\s+(\\d+)\\s+)?(`\\[dummy\\]`\\s+)?(`\\[displayhuntdummy\\]`\\s+)?" +
		"(?:`(\\[(?:normal|boss)\\])`|`(\\[(?:champion|super champion)\\])`\\s+0)\\s*")
	aicRow        = regexp.MustCompile("(\\d+)\\s+(-?\\d+)\\s+(-?\\d+)\\s+(-?\\d+)\\s+`\\[(character|monster)\\]`\\s+`\\[(normal|boss)\\]`\\s+(-?\\d+)\\s+(-?\\d+)\\s*")
	monsterObject = regexp.MustCompile("(?m)^\\[monster object type\\]\\s*\\r?\\n\\s*`([^`]+)`")
)

func matchFields(text string, indices []int) []string {
	fields := make([]string, len(indices)/2)
	for i := range fields {
		if indices[2*i] >= 0 {
			fields[i] = text[indices[2*i]:indices[2*i+1]]
		}
	}
	return fields
}

func (p *input) completeRows(body string, rows [][]int, reason string) {
	end := 0
	for _, at := range rows {
		p.require(strings.TrimSpace(body[end:at[0]]) == "", reason)
		end = at[1]
	}
	p.require(strings.TrimSpace(body[end:]) == "", reason)
}

func (s *mapSource) monsterObject(code uint32) bool {
	if object, ok := s.objects[code]; ok {
		return object
	}
	text, err := s.a.ReadText(s.monsters[int64(code)], 64*1024)
	object := err == nil && monsterObject.MatchString(text)
	s.objects[code] = object
	return object
}

func (s *mapSource) companions(p *input, r *opening.Room, text string, basis byte) []opening.Companion {
	if !tagPresent(text, "ai character") {
		return nil
	}
	blocks := pvftext.Blocks(text, "ai character")
	if !p.require(len(blocks) == 1 && tagPresent(text, "/ai character"), "Mainline AIC requires one complete placement block") {
		return nil
	}
	rows := aicRow.FindAllStringSubmatchIndex(blocks[0], -1)
	p.completeRows(blocks[0], rows, "Mainline AIC has unsupported placement semantics")
	p.require(len(rows) <= 64 && (len(rows) == 0 || basis > 0), "Mainline AIC level or capacity is invalid")
	all := make([]opening.Companion, 0, len(rows))
	for i, at := range rows {
		row := matchFields(blocks[0], at)
		code := p.id(p.number(row[1], 64))
		p.require(s.aics[int64(code)] != "", "Mainline AIC identity, coordinates or native byte fields are invalid")
		for _, coord := range row[2:5] {
			p.number(coord, 32)
		}
		p.width(p.number(row[7], 64))
		p.width(p.number(row[8], 64))
		if row[5] == "monster" {
			slot := byte(i)
			var typ byte
			if row[6] == "boss" {
				typ = 3
			}
			r.Monsters = append(r.Monsters, opening.Monster{Code: code, Level: basis, Type: typ, BlocksRoom: true, AiScriptIndex: &slot})
		} else {
			p.require(row[6] != "boss", "Friendly boss AIC semantics are not supported")
			all = append(all, opening.Companion{Code: code, Level: basis, Type: 5, ScriptIndex: byte(i), CreditsPlayerKills: true})
		}
	}
	return all
}

func (p *input) passives(text string, allowZero bool) []uint32 {
	values := strings.Fields(p.field(text, "passive object", tagPresent(text, "passive object")))
	if !p.require(len(values)%4 == 0 && len(values) <= 4096, "incomplete passive object coordinates") {
		return nil
	}
	codes := make([]uint32, 0, len(values)/4)
	for i := 0; i < len(values); i += 4 {
		id, err := strconv.ParseUint(values[i], 10, 32)
		p.require(err == nil && (allowZero || id != 0), "invalid passive object identity")
		for _, v := range values[i+1 : i+4] {
			p.number(v, 32)
		}
		codes = append(codes, uint32(id))
	}
	return codes
}

func (p *input) npcs(text string) []uint32 {
	npcs := pvftext.Blocks(text, "NPC")
	var ids []uint32
	if len(npcs) != 1 {
		return ids
	}
	end := 0
	for _, at := range npcRow.FindAllStringSubmatchIndex(npcs[0], -1) {
		if strings.TrimSpace(npcs[0][end:at[0]]) != "" {
			return nil
		}
		id, err := strconv.ParseUint(npcs[0][at[2]:at[3]], 10, 32)
		p.require(err == nil, "invalid unconditional NPC ID")
		ids = append(ids, uint32(id))
		end = at[1]
	}
	if strings.TrimSpace(npcs[0][end:]) != "" {
		return nil
	}
	slices.Sort(ids)
	return slices.Compact(ids)
}
