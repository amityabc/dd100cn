package dungeon

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math"
	"slices"
	"strconv"
	"strings"

	"s9a1/server/internal/opening"
	"s9a1/server/internal/protocol"
	"s9a1/server/internal/storage"
)

const oculusScenePolicy = "OculusRetainedScene/v1"

type OculusSceneArchive struct {
	Generation      uint32
	DefinitionHash  string
	ActorFirst      uint16
	Deaths, Passive []string
}

type OculusSceneHistory struct {
	Generation      uint32
	DefinitionHash  string
	AdmissionHash   string
	ActorFirst      uint16
	Archived        []OculusSceneArchive
	Deaths, Passive []string
	retired         map[oculusSourceActor]oculusDeath
}

type oculusSourceActor struct {
	X, Y        byte
	SourceIndex int
}

type oculusDeath struct {
	key, fingerprint string
}

type oculusDeathScope struct {
	RunId, ResourceHash     string
	Dungeon, MapId, Code    uint32
	MazeIndex, RoomX, RoomY byte
	Actor                   uint16
}

func (r *Run) matchesOculusScene(scope oculusDeathScope, room *opening.Room) bool {
	def := r.Definition.Dungeon
	return scope.RunId == r.ID && scope.ResourceHash == def.ResourceHash && scope.Dungeon == def.ID &&
		scope.MapId == room.MapID && scope.MazeIndex == def.MazeIndex && scope.RoomX == room.X && scope.RoomY == room.Y
}

func oculusHash(parts ...string) string {
	digest := sha256.New()
	for _, part := range parts {
		_, _ = digest.Write(le.AppendUint32(nil, uint32(len(part))))
		_, _ = digest.Write([]byte(part))
	}
	return strings.ToUpper(hex.EncodeToString(digest.Sum(nil)))
}

type oculusMonsterShape struct {
	Code             uint32
	Level, Type      byte
	BlocksRoom       bool
	AiScriptIndex    *byte   `json:",omitempty"`
	NativeSpawnIndex *uint32 `json:",omitempty"`
	Dummy            bool    `json:",omitempty"`
	HellPhase        uint16  `json:",omitempty"`
}

type oculusRoomShape struct {
	X, Y               byte
	MapId              uint32
	Gates              byte
	Monsters           []oculusMonsterShape
	MapAliases         []uint32
	PassiveObjectCodes []uint32
	NpcTemplates       []uint32
	Companions         []opening.Companion
	CinematicLayer     *oculusRoomShape
}

func freezeOculusRoom(r *opening.Room) *oculusRoomShape {
	if r == nil {
		return nil
	}
	out := &oculusRoomShape{X: r.X, Y: r.Y, MapId: r.MapID, Gates: r.Gates,
		Monsters: make([]oculusMonsterShape, 0, len(r.Monsters)), MapAliases: append([]uint32{}, r.MapAliases...),
		PassiveObjectCodes: append([]uint32{}, r.PassiveObjectCodes...), NpcTemplates: append([]uint32{}, r.NpcTemplates...),
		Companions: append([]opening.Companion{}, r.Companions...), CinematicLayer: freezeOculusRoom(r.CinematicLayer)}
	slices.Sort(out.MapAliases)
	slices.Sort(out.NpcTemplates)
	for _, m := range r.Monsters {
		out.Monsters = append(out.Monsters, oculusMonsterShape{m.Code, m.Level, m.Type, m.BlocksRoom,
			m.AiScriptIndex, m.NativeSpawnIndex, m.Dummy, m.HellPhase})
	}
	return out
}

func oculusDefinitionHash(d Definition, mode OculusMode) string {
	def := d.Dungeon
	rooms := make([]*oculusRoomShape, len(def.Rooms))
	for i := range def.Rooms {
		rooms[i] = freezeOculusRoom(&def.Rooms[i])
	}
	multiplier := def.ExperienceMultiplier
	if multiplier == "" {
		multiplier = "1"
	}
	difficulties := mode.SelectableDifficulties
	if difficulties == nil {
		difficulties = []uint16{4}
	}
	raw, _ := json.Marshal(struct {
		Id                                                  uint32
		MinimumLevel, MazeIndex, BossX, BossY               byte
		Start                                               *oculusRoomShape
		Rooms                                               []*oculusRoomShape
		ExperienceMultiplier                                json.Number
		ResourceHash                                        string
		QuestConnections                                    []uint16
		Difficulty                                          byte
		DisableDrops, DisableClearReward, DisableExperience bool
		UsesOrdinaryRewards                                 bool
		EntryFatigueCost, RoomFatigueCost                   uint16
		SelectableDifficulties                              []uint16
		Tournament                                          *tournamentDefinition
	}{def.ID, def.MinimumLevel, def.MazeIndex, def.BossX, def.BossY, freezeOculusRoom(&def.Start), rooms, multiplier,
		def.ResourceHash, append([]uint16{}, def.QuestConnections...), def.Difficulty, def.DisableDrops, def.DisableClearReward,
		def.DisableExperience, def.UsesOrdinaryRewards, def.EntryFatigueCost, def.RoomFatigueCost, difficulties, def.Tournament})
	return oculusHash(string(raw))
}

func retainedOculusDefinition(layout *oculusLayout, generation uint32, retired map[oculusSourceActor]oculusDeath) (Definition, map[cell][]int) {
	d := layout.admission
	bindings := make(map[cell][]int, len(d.Dungeon.Rooms))
	roomPlan := func(room opening.Room) opening.Room {
		spawns := make([]opening.Monster, 0, len(room.Monsters))
		indexes := make([]int, 0, len(room.Monsters))
		for i, m := range room.Monsters {
			if _, removed := retired[oculusSourceActor{room.X, room.Y, i}]; !removed {
				spawns = append(spawns, m)
				indexes = append(indexes, i)
			}
		}
		bindings[cell{room.X, room.Y}] = indexes
		room.Monsters = spawns
		return room
	}
	d.Dungeon.Start = roomPlan(d.Dungeon.Start)
	d.Dungeon.Rooms = slices.Clone(d.Dungeon.Rooms)
	for i, room := range d.Dungeon.Rooms {
		d.Dungeon.Rooms[i] = roomPlan(room)
	}
	if generation == 1 {
		return d, bindings
	}
	type row struct {
		oculusSourceActor
		Death string
	}
	killed := make([]row, 0, len(retired))
	for source, proof := range retired {
		killed = append(killed, row{source, proof.fingerprint})
	}
	slices.SortFunc(killed, func(a, b row) int {
		if a.Y != b.Y {
			return int(a.Y) - int(b.Y)
		}
		if a.X != b.X {
			return int(a.X) - int(b.X)
		}
		return a.SourceIndex - b.SourceIndex
	})
	raw, _ := json.Marshal(killed)
	d.Dungeon.ResourceHash = oculusHash(oculusScenePolicy, oculusDefinitionHash(layout.admission, layout.mode),
		strconv.FormatUint(uint64(generation), 10), string(raw))
	return d, bindings
}

func (l *oculusLayout) namedOculus(code uint32) bool {
	r := l.mode.Rules
	for _, group := range [][]OculusValue{r.GrimSeekers, r.Imposters, r.Kaiju, r.KillCoinLimitBonuses, r.KillTimeoverBonuses} {
		if slices.ContainsFunc(group, func(row OculusValue) bool { return row.Id == code }) {
			return code != 0
		}
	}
	return code != 0 && (code == r.BossMonsterId || code == r.LuiseMonsterId || code == r.RoseberyRonMonsterId ||
		code == r.ShopOpenMonsterId || slices.Contains(r.SoryunMonsterIds, code))
}

func (r *Run) OculusHistory(receipts map[string]storage.DungeonSceneReceipt) (OculusSceneHistory, error) {
	z := OculusSceneHistory{}
	if r == nil || r.OculusLayout == nil || r.OculusEntry == nil ||
		r.OculusGeneration != uint32(len(r.OculusLayout.previous))+1 || r.OculusGeneration > 129 {
		return z, storage.ErrConflict
	}
	layout, entry := r.OculusLayout, r.OculusEntry
	if entry.DungeonHash != layout.admission.Dungeon.ResourceHash || entry.RunId != r.ID || entry.Seed != r.Seed || entry.Mode != layout.mode.Mode {
		return z, storage.ErrConflict
	}
	z = OculusSceneHistory{Generation: r.OculusGeneration, DefinitionHash: r.Definition.Dungeon.ResourceHash,
		AdmissionHash: entry.DungeonHash, ActorFirst: r.FirstActor(), Archived: []OculusSceneArchive{},
		retired: map[oculusSourceActor]oculusDeath{}}
	known := make(map[string]bool, len(receipts))
	var awards [4]uint64
	var previousEnd uint32
	var previous *Run
	for i, current := range append(slices.Clone(layout.previous), r) {
		generation := uint32(i + 1)
		expected, bindings := retainedOculusDefinition(layout, generation, z.retired)
		if current == nil || current.OculusEntry != entry || current.ID != r.ID || current.Character != r.Character ||
			current.Started != r.Started || current.Seed != r.Seed || current.RewardLevel != r.RewardLevel ||
			current.OculusGeneration != generation || oculusDefinitionHash(current.Definition, layout.mode) != oculusDefinitionHash(expected, layout.mode) ||
			current.FirstActor() < 402 || generation == 1 && current.FirstActor() != entry.ActorFirst ||
			uint32(current.FirstActor()) < previousEnd || current.Room == nil || current.nextGroundSlot == 0 ||
			current != r && (!current.Ready || current.Cleared || current.PlayerDeath != nil) ||
			previous != nil && (current.DeathCycle < previous.DeathCycle || current.nextGroundSlot < previous.nextGroundSlot) {
			return OculusSceneHistory{}, storage.ErrConflict
		}
		row, end, err := current.validateOculusScene(bindings, receipts, known, &awards, z.retired)
		if err != nil {
			return OculusSceneHistory{}, err
		}
		previous, previousEnd = current, end
		if current == r {
			z.Deaths, z.Passive = row.Deaths, row.Passive
		} else {
			z.Archived = append(z.Archived, row)
		}
	}
	if len(known) != len(receipts) {
		return OculusSceneHistory{}, storage.ErrConflict
	}
	return z, nil
}

func (r *Run) validateOculusScene(bindings map[cell][]int, receipts map[string]storage.DungeonSceneReceipt, known map[string]bool,
	awards *[4]uint64, retired map[oculusSourceActor]oculusDeath) (OculusSceneArchive, uint32, error) {
	out := OculusSceneArchive{Generation: r.OculusGeneration, DefinitionHash: r.Definition.Dungeon.ResourceHash,
		ActorFirst: r.FirstActor(), Deaths: []string{}, Passive: []string{}}
	end := uint32(r.FirstActor()) + 1
	var cleared bool
	seen := 0
	drops := make(map[groundKey]GroundDrop)
	fail := func() (OculusSceneArchive, uint32, error) { return OculusSceneArchive{}, 0, storage.ErrConflict }
	for key, room := range r.rooms {
		if room == nil || len(room.Monsters) != len(bindings[key]) || r.seen[scene{key, false}] && !r.visited[key] {
			return fail()
		}
		base := r.bases[key]
		end = max(end, uint32(base)+uint32(len(room.Monsters)+len(room.Companions)))
		for index, m := range room.Monsters {
			actor := base + uint16(index)
			death, committed := r.Dead[actor]
			if !committed {
				if m.BlocksRoom && r.clearedRooms[key] {
					return fail()
				}
				continue
			}
			if !r.seen[scene{key, false}] || m.Type > 3 || death.Result.Actor != actor {
				return fail()
			}
			proof, ok := frozenOculusResult(death.Key, death.Input, death.Result, receipts, known)
			var scope oculusDeathScope
			if !ok || death.Key != fmt.Sprintf("dungeon-death:%s:%d:%d:%d:%d", r.idKey(), r.Definition.Dungeon.MazeIndex, room.X, room.Y, actor) ||
				json.Unmarshal([]byte(proof.Input), &scope) != nil || !r.matchesOculusScene(scope, room) || scope.Actor != actor || scope.Code != m.Code ||
				!freezeOculusDrops(death.Result.Drops, key, r.nextGroundSlot, drops) {
				return fail()
			}
			if death.Result.DungeonCleared {
				if death.Result.ClearElapsedMilliseconds != r.Elapsed {
					return fail()
				}
				cleared = true
			}
			if r.OculusLayout.namedOculus(m.Code) {
				source := oculusSourceActor{room.X, room.Y, bindings[key][index]}
				if _, duplicate := retired[source]; duplicate {
					return fail()
				}
				retired[source] = oculusDeath{death.Key, oculusHash(death.Key, strconv.FormatInt(proof.Revision, 10), proof.Input, proof.Result)}
			}
			awards[m.Type] += uint64(death.Result.ExperienceAward)
			out.Deaths, seen = append(out.Deaths, death.Key), seen+1
		}
	}
	if seen != len(r.Dead) || cleared != r.Cleared || r.Room == nil ||
		r.rooms[cell{r.Room.X, r.Room.Y}] != r.Room || r.Ready && !r.seen[scene{cell{r.Room.X, r.Room.Y}, false}] {
		return fail()
	}
	for i, count := range awards {
		if r.Totals[i] != uint32(min(uint64(math.MaxUint32), count)) {
			return fail()
		}
	}
	for key, death := range r.passive {
		var scope oculusDeathScope
		if json.Unmarshal([]byte(death.input), &scope) != nil || scope.Code != death.result.Actor {
			return fail()
		}
		pos := cell{scope.RoomX, scope.RoomY}
		room := r.rooms[pos]
		if room == nil || !r.matchesOculusScene(scope, room) || !r.seen[scene{pos, false}] || !slices.Contains(room.PassiveObjectCodes, scope.Code) {
			return fail()
		}
		if _, ok := frozenOculusResult(key, death.input, death.result, receipts, known); !ok ||
			!freezeOculusDrops(death.result.Drops, pos, r.nextGroundSlot, drops) {
			return fail()
		}
		out.Passive = append(out.Passive, key)
	}
	for key, drop := range r.ground {
		if expected, ok := drops[key]; !ok || !sameOculusJSON(drop, expected) {
			return fail()
		}
	}
	slices.Sort(out.Deaths)
	slices.Sort(out.Passive)
	return out, end, nil
}

func frozenOculusResult(key, input string, result any, receipts map[string]storage.DungeonSceneReceipt, known map[string]bool) (storage.DungeonSceneReceipt, bool) {
	proof, ok := receipts[key]
	raw, err := json.Marshal(result)
	if !ok || known[key] || proof.Revision <= 0 || proof.Input != input || err != nil || proof.Result != string(raw) {
		return proof, false
	}
	known[key] = true
	return proof, true
}

func sameOculusJSON(left, right any) bool {
	a, e1 := json.Marshal(left)
	b, e2 := json.Marshal(right)
	return e1 == nil && e2 == nil && string(a) == string(b)
}

func freezeOculusDrops(rows []GroundDrop, room cell, end uint16, out map[groundKey]GroundDrop) bool {
	for _, row := range rows {
		key := groundKey{room, row.ObjectKey}
		if _, duplicate := out[key]; duplicate || row.RoomX != room.x || row.RoomY != room.y ||
			row.SceneSlot == 0 || row.SceneSlot >= end {
			return false
		}
		out[key] = row
	}
	return true
}

func (r *Run) OculusProofKeys() []string {
	if r == nil || r.OculusLayout == nil {
		return nil
	}
	var keys []string
	for _, scene := range append(slices.Clone(r.OculusLayout.previous), r) {
		for _, death := range scene.Dead {
			keys = append(keys, death.Key)
		}
		for key := range scene.passive {
			keys = append(keys, key)
		}
	}
	slices.Sort(keys)
	return keys
}

func (r *Run) OculusDeathResourceHash(deathKey string) string {
	if r == nil || r.OculusLayout == nil {
		return ""
	}
	for _, scene := range append(slices.Clone(r.OculusLayout.previous), r) {
		for _, death := range scene.Dead {
			if death.Key == deathKey {
				return scene.Definition.Dungeon.ResourceHash
			}
		}
	}
	return ""
}

func (r *Run) resumeOculus(first uint16, a protocol.Actor, history OculusSceneHistory) (*Run, uint16, error) {
	layout := *r.OculusLayout
	layout.previous = append(slices.Clone(layout.previous), r)
	layout.retired = make(map[oculusCell]string)
	for _, item := range layout.cells {
		for key, death := range history.retired {
			if item.Position.X != key.X || item.Position.Y != key.Y {
				continue
			}
			for _, room := range layout.admission.Dungeon.Rooms {
				if room.X == key.X && room.Y == key.Y && room.Monsters[key.SourceIndex].Code == item.Monster {
					layout.retired[item] = death.key
				}
			}
		}
	}
	d, _ := retainedOculusDefinition(&layout, r.OculusGeneration+1, history.retired)
	resumed, next, err := Prepare(d, r.Requested, a, first, r.Seed)
	if err != nil {
		return nil, 0, err
	}
	floor := oculusFloor(r.Room.X, r.Room.Y)
	var start *opening.Room
	for _, position := range layout.mode.StartMaps {
		if position.Floor == floor {
			if start != nil {
				return nil, 0, storage.ErrConflict
			}
			start = resumed.rooms[cell{position.X, position.Y}]
		}
	}
	if start == nil {
		return nil, 0, storage.ErrConflict
	}
	resumed.Room, resumed.visited = start, map[cell]bool{{start.X, start.Y}: true}
	for key, room := range resumed.rooms {
		if !slices.ContainsFunc(room.Monsters, func(m opening.Monster) bool { return m.BlocksRoom }) {
			resumed.clearedRooms[key] = true
		}
	}
	resumed.ID, resumed.Started, resumed.RewardLevel, resumed.Totals = r.ID, r.Started, r.RewardLevel, r.Totals
	resumed.OculusLayout, resumed.OculusEntry, resumed.OculusGeneration = &layout, r.OculusEntry, r.OculusGeneration+1
	resumed.DeathCycle, resumed.nextGroundSlot = r.DeathCycle, r.nextGroundSlot
	return resumed, next, nil
}
