package dungeons

import "s9a1/server/internal/opening"

type nativeDungeon struct {
	Id                                                uint32
	MinimumLevel, MazeIndex, BossX, BossY             byte
	Start                                             nativeRoom
	Rooms                                             []nativeRoom
	Difficulty                                        byte
	ChallengeStage, EntryFatigueCost, RoomFatigueCost uint16
	ResourceHash                                      string
}

type nativeRoom struct {
	X, Y                                         byte
	MapId                                        uint32
	Monsters                                     []nativeMonster
	Gates                                        byte
	ScriptTransfer                               *nativeTransfer
	StormCleanup                                 *nativeCleanup
	StormArrival                                 *nativeArrival
	CinematicDestroys                            []opening.CinematicDestroy
	CinematicLayer                               *nativeRoom
	MapAliases, PassiveObjectCodes, NpcTemplates []uint32
	Companions                                   []opening.Companion
}

type nativeTransfer struct {
	X, Y           byte
	Map            uint32
	Hash           string
	RequiredDeaths []uint16
}
type nativeCleanup struct {
	Index         byte
	Prerequisites []uint16
	Hash          string
}
type nativeArrival struct {
	Boss    byte
	Targets []uint16
	Hash    string
}

func byteValues(values []byte) []uint16 {
	out := make([]uint16, len(values))
	for i, v := range values {
		out[i] = uint16(v)
	}
	return out
}

type nativeMonster struct {
	Code             uint32
	Level, Type      byte
	BlocksRoom       bool
	AiScriptIndex    *byte   `json:",omitempty"`
	NativeSpawnIndex *uint32 `json:",omitempty"`
	Dummy            bool    `json:",omitempty"`
	HellPhase        uint16  `json:",omitempty"`
}

func wireRoom(room opening.Room) nativeRoom {
	out := nativeRoom{X: room.X, Y: room.Y, MapId: room.MapID, Gates: room.Gates, CinematicDestroys: list(room.CinematicDestroys),
		MapAliases: list(room.MapAliases), PassiveObjectCodes: list(room.PassiveObjectCodes), NpcTemplates: list(room.NpcTemplates),
		Companions: list(room.Companions), Monsters: make([]nativeMonster, 0, len(room.Monsters))}
	if t := room.ScriptTransfer; t != nil {
		out.ScriptTransfer = &nativeTransfer{t.X, t.Y, t.Map, t.Hash, byteValues(t.RequiredDeaths)}
	}
	if v := room.StormCleanup; v != nil {
		out.StormCleanup = &nativeCleanup{v.Index, byteValues(v.Prerequisites), v.Hash}
	}
	if v := room.StormArrival; v != nil {
		out.StormArrival = &nativeArrival{v.Boss, byteValues(v.Targets), v.Hash}
	}
	for _, m := range room.Monsters {
		out.Monsters = append(out.Monsters, nativeMonster{Code: m.Code, Level: m.Level, Type: m.Type, BlocksRoom: m.BlocksRoom,
			AiScriptIndex: m.AiScriptIndex, NativeSpawnIndex: m.NativeSpawnIndex, Dummy: m.Dummy, HellPhase: m.HellPhase})
	}
	if room.CinematicLayer != nil {
		child := wireRoom(*room.CinematicLayer)
		out.CinematicLayer = &child
	}
	return out
}

func wireDungeon(d opening.Dungeon) nativeDungeon {
	return nativeDungeon{Id: d.ID, MinimumLevel: d.MinimumLevel, MazeIndex: d.MazeIndex, BossX: d.BossX, BossY: d.BossY,
		Start: wireRoom(d.Start), Rooms: wireRooms(d.Rooms), Difficulty: d.Difficulty, ChallengeStage: d.ChallengeStage,
		EntryFatigueCost: d.EntryFatigueCost, RoomFatigueCost: d.RoomFatigueCost, ResourceHash: d.ResourceHash}
}

func wireRooms(rooms []opening.Room) []nativeRoom {
	out := make([]nativeRoom, len(rooms))
	for i, r := range rooms {
		out[i] = wireRoom(r)
	}
	return out
}

func list[T any](values []T) []T {
	if values == nil {
		return []T{}
	}
	return values
}
