package fate

import (
	"crypto/sha256"
	"encoding/binary"
	"fmt"
	"regexp"
	"slices"
	"strings"

	"s9a1/server/internal/opening"
)

type loadingDungeon struct {
	Id                       uint32
	MinimumLevel, MazeIndex  byte
	Start                    loadingRoom
	Rooms                    []loadingRoom
	BossX, BossY, Difficulty byte
	ChallengeStage           uint16
	ResourceHash             string
}

type loadingRoom struct {
	X, Y                                         byte
	MapId                                        uint32
	Monsters                                     []loadingMonster
	Gates                                        byte
	ScriptTransfer                               *opening.ScriptTransfer
	StormCleanup                                 *opening.StormCleanup
	StormArrival                                 *opening.StormArrival
	CinematicDestroys                            []opening.CinematicDestroy
	CinematicLayer                               *loadingRoom
	MapAliases, PassiveObjectCodes, NpcTemplates []uint32
	Companions                                   []opening.Companion
}

type loadingMonster struct {
	Code        uint32
	Level, Type byte
	BlocksRoom  bool
	Dummy       bool `json:",omitempty"`
}

var (
	mazeMarker  = regexp.MustCompile(`(?im)^\[maze info\][ \t]*$`)
	bossMapSpec = regexp.MustCompile("`boss`\\s+0\\s+0\\s+(\\d+)")
	monsterRow  = regexp.MustCompile("(\\d+)\\s+(\\d+)\\s+(-?\\d+)\\s+(-?\\d+)\\s+(-?\\d+)\\s+(-?\\d+)\\s+(\\d+)\\s+(\\d+)\\s+`([^`]+)`\\s+`([^`]+)`(?:\\s+`([^`]+)`)?")
	npcRow      = regexp.MustCompile("(\\d+)\\s+`\\[(?:left|right)\\]`\\s+-?\\d+\\s+-?\\d+\\s+-?\\d+")
)

func (s *source) loading(r *Catalog, dgn string) []uint32 {
	maze := mazeMarker.Split(dgn, -1)
	if !s.require(len(maze) > 1, "activity loading maze missing") {
		return nil
	}
	rows := bossMapSpec.FindAllStringSubmatch(s.block(maze[1], "map specification"), -1)
	if !s.require(len(rows) == 1, "ambiguous activity loading map") {
		return nil
	}
	id := s.id(s.integer(rows[0][1]))
	start, boss := s.numbers(s.block(maze[1], "start map")), s.numbers(s.block(maze[1], "boss map"))
	layout := s.numbers(s.section(maze[1], "size", false))
	grid := strings.NewReplacer("`", "", " ", "", "\t", "", "\r", "", "\n", "", ",", "").Replace(s.section(maze[1], "greed", false))
	if !s.require(id == 400005147 && slices.Equal(start, []int{0, 0}) && slices.Equal(boss, []int{0, 0}) &&
		len(layout) >= 2 && layout[0] == 1 && layout[1] == 1 && (len(grid) == 1 || len(grid) == 2 && grid[0] == grid[1]) && grid[0] >= 'A' && grid[0] <= 'P',
		"unsupported activity loading maze") {
		return nil
	}
	s.index("monster")
	mapText := s.resolveText("map", id)
	phases := blocks(s.block(mapText, "phase shift map"), "phase")
	if !s.require(len(phases) == 19, "activity layered map list requires nineteen phases") {
		return nil
	}
	layerMaps := make([]uint32, 0, len(phases))
	seen := map[uint32]bool{id: true}
	for at, phase := range phases {
		mapID := s.scalar(phase, "map index")
		if at == 0 {
			s.require(mapID == -1, "invalid first layered map identity")
			layerMaps = append(layerMaps, id)
		} else {
			v := s.id(mapID)
			s.require(!seen[v], "duplicate activity layered map")
			seen[v] = true
			layerMaps = append(layerMaps, v)
		}
	}
	basis := 1
	if v := s.section(dgn, "basis level", true); v != "" {
		basis = s.one(v)
	}
	initial := s.loadingRoom(id, mapText, basis, grid[0]-'A')
	if !s.require(len(initial.Monsters) == 2 && slices.ContainsFunc(initial.Monsters, func(m loadingMonster) bool { return m.Code == 407003951 }) &&
		slices.ContainsFunc(initial.Monsters, func(m loadingMonster) bool { return m.Code == 500000186 }) &&
		slices.Contains(initial.PassiveObjectCodes, 490020148), "activity loading actor or passive object missing") {
		return nil
	}
	for _, mapID := range layerMaps {
		room := initial
		if mapID != id {
			room = s.loadingRoom(mapID, s.resolveText("map", mapID), basis, grid[0]-'A')
		}
		for _, actor := range room.Monsters {
			s.resolveText("monster", actor.Code)
		}
		for _, npc := range room.NpcTemplates {
			s.resolveText("npc", npc)
		}
		for _, obj := range room.PassiveObjectCodes {
			s.resolveText("passiveobject", obj)
		}
	}
	initial.MapAliases = append([]uint32{}, layerMaps[1:]...)
	slices.Sort(initial.MapAliases)
	difficulty := 0
	if v := s.section(dgn, "designate dungeon difficulty", true); v != "" {
		difficulty = s.one(v)
	}
	s.require(difficulty >= 0 && difficulty <= 255, "invalid activity loading difficulty")
	h := sha256.New()
	dgnHash, mapHash := sha256.Sum256([]byte(dgn)), sha256.Sum256([]byte(mapText))
	h.Write(dgnHash[:])
	h.Write(binary.LittleEndian.AppendUint32(nil, id))
	h.Write(mapHash[:])
	r.LoadingDungeon = loadingDungeon{Id: r.DungeonId, MinimumLevel: r.MinimumLevel, Start: initial, Rooms: []loadingRoom{initial},
		Difficulty: byte(difficulty), ResourceHash: fmt.Sprintf("%X", h.Sum(nil))}
	room := opening.Room{MapID: id, Gates: initial.Gates, MapAliases: initial.MapAliases,
		PassiveObjectCodes: initial.PassiveObjectCodes, NpcTemplates: initial.NpcTemplates, Companions: initial.Companions,
		CinematicDestroys: initial.CinematicDestroys}
	for _, m := range initial.Monsters {
		room.Monsters = append(room.Monsters, opening.Monster{Code: m.Code, Level: m.Level, Type: m.Type,
			BlocksRoom: m.BlocksRoom, Dummy: m.Dummy})
	}
	r.Definition.LoadingDungeon = opening.Dungeon{ID: r.DungeonId, MinimumLevel: r.MinimumLevel, Difficulty: byte(difficulty),
		Start: room, Rooms: []opening.Room{room}, ResourceHash: r.LoadingDungeon.ResourceHash}
	return layerMaps
}

func (s *source) loadingRoom(id uint32, text string, basis int, gates byte) loadingRoom {
	room := loadingRoom{MapId: id, Gates: gates, Monsters: []loadingMonster{}, MapAliases: []uint32{}, PassiveObjectCodes: []uint32{},
		NpcTemplates: []uint32{}, Companions: []opening.Companion{}, CinematicDestroys: []opening.CinematicDestroy{}}
	teams := s.numbers(s.section(text, "monster team", true))
	for i, row := range monsterRow.FindAllStringSubmatch(s.optionalBlock(text, "monster"), -1) {
		id := s.id(s.integer(row[1]))
		level := s.integer(row[3])
		if s.integer(row[2]) != 0 {
			level += basis
		}
		if level <= 0 {
			level = basis
		}
		s.require(s.index("monster")[int64(id)] != "" && level >= 0 && level <= 255, "invalid activity monster code or level")
		tag := strings.Trim(row[10], "[]")
		dummy := tag == "dummy"
		if dummy {
			tag = strings.Trim(row[11], "[]")
			s.require(slices.Contains([]string{"boss", "normal", "champion", "super champion"}, tag), "invalid activity dummy classification")
		} else {
			s.require(row[11] == "", "unexpected activity monster classification")
		}
		typ := map[string]byte{"champion": 1, "super champion": 2, "boss": 3}[tag]
		room.Monsters = append(room.Monsters, loadingMonster{Code: id, Level: byte(level), Type: typ, BlocksRoom: i >= len(teams) || teams[i] != 0, Dummy: dummy})
	}
	passive := s.numbers(s.optionalBlock(text, "passive object"))
	if !s.require(len(passive)%4 == 0 && len(passive) <= 4096, "invalid activity passive object rows") {
		return room
	}
	for at := 0; at < len(passive); at += 4 {
		room.PassiveObjectCodes = append(room.PassiveObjectCodes, s.id(passive[at]))
	}
	if npcs := blocks(text, "NPC"); len(npcs) == 1 {
		matched := npcRow.FindAllStringIndex(npcs[0], -1)
		end := 0
		for _, at := range matched {
			if strings.TrimSpace(npcs[0][end:at[0]]) != "" {
				room.NpcTemplates = []uint32{}
				return room
			}
			row := npcRow.FindStringSubmatch(npcs[0][at[0]:at[1]])
			room.NpcTemplates = append(room.NpcTemplates, s.id(s.integer(row[1])))
			end = at[1]
		}
		if strings.TrimSpace(npcs[0][end:]) != "" {
			room.NpcTemplates = []uint32{}
		}
		slices.Sort(room.NpcTemplates)
		room.NpcTemplates = slices.Compact(room.NpcTemplates)
	}
	return room
}

func (s *source) optionalBlock(text, tag string) string {
	if !has(text, tag) {
		return ""
	}
	return s.block(text, tag)
}
