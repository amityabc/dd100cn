// Package opening projects the original native tutorial dungeon entry and
// first-room lifecycle for a single admitted character.
package opening

import (
	"crypto/rand"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"math"
	"sort"
	"time"

	"s9a1/server/internal/protocol"
)

type Point struct {
	Town, Area byte
	X, Y       uint16
	Direction  byte
}

type Monster struct {
	Code              uint32
	Level, Type       byte
	BlocksRoom, Dummy bool
	AiScriptIndex     *byte
	NativeSpawnIndex  *uint32
	HellPhase         uint16
}

type Companion struct {
	Code                     uint32
	Level, Type, ScriptIndex byte
	CreditsPlayerKills       bool
}

type Room struct {
	X, Y, Gates        byte
	MapID              uint32
	Monsters           []Monster
	Companions         []Companion
	PassiveObjectCodes []uint32
	NpcTemplates       []uint32
	MapAliases         []uint32
	CinematicLayer     *Room
	CinematicDestroys  []CinematicDestroy
	ScriptTransfer     *ScriptTransfer
	StormCleanup       *StormCleanup
	StormArrival       *StormArrival
}

type CinematicDestroy struct {
	Index        byte
	Movie        uint32
	TriggerIndex *byte
	Hash         string
}

type ScriptTransfer struct {
	X, Y           byte
	Map            uint32
	Hash           string
	RequiredDeaths []byte
}

type StormCleanup struct {
	Index         byte
	Prerequisites []byte
	Hash          string
}

type StormArrival struct {
	Boss    byte
	Targets []byte
	Hash    string
}

type Dungeon struct {
	ID                                                uint32
	MinimumLevel, MazeIndex, BossX, BossY, Difficulty byte
	ChallengeStage, EntryFatigueCost, RoomFatigueCost uint16
	Start                                             Room
	Rooms                                             []Room
	ResourceHash                                      string
}

type Definition struct {
	Job         byte
	Hash        string
	ReturnPoint Point
	Dungeon     Dungeon
}

func (d Definition) Matches(job byte, id uint32, hash string, point Point) bool {
	return d.Job == job && d.Dungeon.ID != 0 && d.Dungeon.ID == id && d.Hash != "" && d.Hash == hash && d.ReturnPoint == point
}

func (d Definition) orderedRooms() ([]Room, error) {
	if d.Dungeon.ID == 0 || d.Dungeon.Start.MapID == 0 {
		return nil, errors.New("missing tutorial dungeon or start map")
	}
	rooms := []Room{d.Dungeon.Start}
	seen := map[[2]byte]bool{}
	for _, room := range d.Dungeon.Rooms {
		key := [2]byte{room.X, room.Y}
		if seen[key] || room.MapID == 0 {
			return nil, errors.New("invalid tutorial room layout")
		}
		seen[key] = true
		if room.X == d.Dungeon.Start.X && room.Y == d.Dungeon.Start.Y {
			if room.MapID != d.Dungeon.Start.MapID {
				return nil, errors.New("ambiguous tutorial start map")
			}
			continue
		}
		rooms = append(rooms, room)
	}
	rest := rooms[1:]
	sort.Slice(rest, func(i, j int) bool {
		if rest[i].Y != rest[j].Y {
			return rest[i].Y < rest[j].Y
		}
		return rest[i].X < rest[j].X
	})
	if len(rooms) > 256 {
		return nil, errors.New("too many tutorial rooms")
	}
	return rooms, nil
}

// Reserve assigns the stock connection-scoped contiguous actor range used by
// every room. Native passive object codes can occupy the same key space.
func (d Definition) Reserve(first uint16) (base, next uint16, err error) {
	return d.reserveActors(first, 0)
}

// ReserveDynamic handles actors created after room identities are reserved.
func (d Definition) ReserveDynamic(first uint16, count int) (base, next uint16, err error) {
	if count < 1 {
		return 0, 0, errors.New("Actor reservation must be positive")
	}
	return d.reserveActors(first, count)
}

func (d Definition) reserveActors(first uint16, dynamicCount int) (base, next uint16, err error) {
	if first < 402 {
		if dynamicCount != 0 {
			return 0, 0, errors.New("Dungeon actor allocator does not belong to this connection")
		}
		return 0, 0, errors.New("tutorial actor range starts below the native minimum")
	}
	rooms, err := d.orderedRooms()
	if err != nil {
		return 0, 0, err
	}
	var count int
	var passives []uint32
	for _, room := range rooms {
		for _, scene := range []*Room{&room, room.CinematicLayer} {
			if scene == nil {
				continue
			}
			if dynamicCount == 0 {
				spawns := len(scene.Monsters) + len(scene.Companions)
				if spawns > 255 {
					return 0, 0, errors.New("tutorial room exceeds the native spawn count")
				}
				count += spawns
			}
			passives = append(passives, scene.PassiveObjectCodes...)
		}
	}
	if dynamicCount != 0 {
		count = dynamicCount
	} else if count == 0 {
		count = 1
	}
	offset := uint32(first)
	sort.Slice(passives, func(i, j int) bool { return passives[i] < passives[j] })
	for _, code := range passives {
		if code < offset {
			continue
		}
		if uint64(code) >= uint64(offset)+uint64(count) {
			break
		}
		offset = code + 1
	}
	if uint64(offset)+uint64(count) > math.MaxUint16 {
		if dynamicCount != 0 {
			return 0, 0, errors.New("This connection has exhausted its dungeon actor identities")
		}
		return 0, 0, errors.New("tutorial actor range is exhausted")
	}
	return uint16(offset), uint16(offset + uint32(count)), nil
}

type Packet struct {
	Class  byte
	Opcode uint16
	Body   []byte
}

func notice(op uint16, b []byte) Packet { return Packet{0, op, b} }
func reply(op uint16, b []byte) Packet  { return Packet{1, op, b} }

type Run struct {
	definition  Definition
	character   uint16
	seed        uint32
	firstActor  uint16
	ready       bool
	runID       string
	started     time.Time
	rewardLevel byte
	room        Room
	rooms       []Room
	seen        map[[2]byte]bool
	cleared     map[[2]byte]bool
	deaths      map[uint16]deathRecord
	totals      [4]uint32
}

func (r *Run) Ready() bool { return r.ready }

func (r *Run) WearScene() (string, uint16, Room, bool) {
	return r.runID, r.character, r.room, r.ready
}

// Start projects entry after the owner has committed fatigue and verified
// the persisted tutorial identity. firstActor is the start returned by Reserve.
func Start(def Definition, actor protocol.Actor, seed uint32, firstActor uint16, native140, nativeParty bool) (*Run, []Packet, error) {
	if actor.Job != def.Job || def.Dungeon.MinimumLevel > actor.Level {
		return nil, nil, errors.New("character is ineligible for this tutorial definition")
	}
	if firstActor < 402 || def.Dungeon.ID == 0 || def.Dungeon.Start.MapID == 0 {
		return nil, nil, errors.New("invalid tutorial room admission")
	}
	m0, err := protocol.Mode0(actor, native140)
	if err != nil {
		return nil, nil, err
	}
	m1, err := protocol.Mode1(actor, false)
	if err != nil {
		return nil, nil, err
	}
	full, err := protocol.Mode1(actor, true)
	if err != nil {
		return nil, nil, err
	}
	start, err := startMap(def.Dungeon.Start, seed, firstActor)
	if err != nil {
		return nil, nil, err
	}
	packets := []Packet{notice(27, make([]byte, 47))}
	if !nativeParty {
		packets = append(packets, notice(9, actorDisplay(actor, true)))
	}
	permissions := []byte{1, 0}
	permissions = binary.LittleEndian.AppendUint32(permissions, def.Dungeon.ID)
	permissions = append(permissions, 1)
	packets = append(packets, notice(5, permissions), notice(2, m0), notice(2, m1),
		notice(28, info(def.Dungeon)), notice(29, start), notice(36, protocol.Fatigue(actor)), notice(2, full))
	if !nativeParty {
		packets = append(packets, notice(9, actorDisplay(actor, false)))
	}
	packets = append(packets, notice(120, []byte{0, 0}))
	rooms, err := def.orderedRooms()
	if err != nil {
		return nil, nil, err
	}
	id := make([]byte, 16)
	rand.Read(id)
	id[6] = (id[6] & 15) | 64
	id[8] = (id[8] & 63) | 128
	raw := hex.EncodeToString(id)
	runID := raw[:8] + "-" + raw[8:12] + "-" + raw[12:16] + "-" + raw[16:20] + "-" + raw[20:]
	return &Run{definition: def, character: actor.ID, seed: seed, firstActor: firstActor, runID: runID, started: time.Now(), rewardLevel: actor.Level,
		room: def.Dungeon.Start, rooms: rooms, seen: map[[2]byte]bool{}, cleared: map[[2]byte]bool{}, deaths: map[uint16]deathRecord{}}, packets, nil
}

func actorDisplay(a protocol.Actor, remove bool) []byte {
	w := []byte{1, 0, 0x0f, 0x27}
	w = binary.LittleEndian.AppendUint16(w, a.ID)
	w = append(w, a.Group, a.Channel, 0)
	if remove {
		return append(w, 3)
	}
	w = append(w, 0, 0, 0)
	name, _ := protocol.EncodeText(a.Name, 30) // validated by Mode0
	w = binary.LittleEndian.AppendUint32(w, uint32(len(name)))
	w = append(w, name...)
	w = append(w, a.Job, a.Level, 0, 0, 0, 0, a.GrowType)
	return append(w, make([]byte, 11)...)
}

func info(d Dungeon) []byte {
	w := make([]byte, 36)
	binary.LittleEndian.PutUint32(w, d.ID)
	w[4] = d.Difficulty
	binary.LittleEndian.PutUint16(w[5:], d.ChallengeStage)
	w[7], w[8], w[9], w[10], w[11] = d.MazeIndex, d.BossX, d.BossY, 255, 255
	w[16] = 12
	binary.LittleEndian.PutUint32(w[20:], math.MaxUint32)
	return w
}

func startMap(room Room, seed uint32, first uint16) ([]byte, error) {
	count := len(room.Monsters) + len(room.Companions)
	if count > 255 || count > 0 && (first < 402 || int(first)+count > math.MaxUint16) {
		return nil, errors.New("tutorial room actors exceed native range")
	}
	w := []byte{room.X, room.Y, 0}
	w = binary.LittleEndian.AppendUint32(w, seed)
	w = append(w, 2, 0, 1, 0, 0, 0, 1)
	w = binary.LittleEndian.AppendUint32(w, room.MapID)
	w = append(w, byte(count))
	spawn := func(i int, index uint32, code uint32, level, category byte) {
		w = append(w, 0, 0)
		w = binary.LittleEndian.AppendUint32(w, index)
		w = binary.LittleEndian.AppendUint16(w, first+uint16(i))
		w = binary.LittleEndian.AppendUint32(w, code)
		w = append(w, level, category, 0, 0, 0, 0, 0, 0, 0)
	}
	for i, m := range room.Monsters {
		if m.HellPhase != 0 || m.Type > 3 {
			return nil, errors.New("unexpected tutorial monster category")
		}
		index, category := uint32(i), m.Type
		if m.AiScriptIndex != nil {
			if *m.AiScriptIndex >= 64 || m.NativeSpawnIndex != nil || m.Code == 0 || m.Level == 0 || m.Type != 0 && m.Type != 3 {
				return nil, errors.New("invalid tutorial APC spawn")
			}
			index = uint32(*m.AiScriptIndex)
			category = 5
			if m.Type == 3 {
				category = 8
			}
		} else if m.NativeSpawnIndex != nil {
			if *m.NativeSpawnIndex < 10000 || *m.NativeSpawnIndex >= 20000 || m.Code == 0 || m.Level == 0 {
				return nil, errors.New("invalid native tutorial spawn index")
			}
			index = *m.NativeSpawnIndex
		}
		spawn(i, index, m.Code, m.Level, category)
	}
	for i, c := range room.Companions {
		if c.Code == 0 || c.Level == 0 || c.Type != 5 || c.ScriptIndex >= 64 {
			return nil, errors.New("invalid tutorial companion spawn")
		}
		spawn(i+len(room.Monsters), uint32(c.ScriptIndex), c.Code, c.Level, c.Type)
	}
	return append(w, 0, 0, 0, 255), nil
}

// Handle owns the first-room load transition. State changes in this method are
// transient; returning to town or completing the tutorial is a separate durable operation.
func (r *Run) Handle(op uint16, body []byte, actor protocol.Actor, sp, tp uint16) ([]Packet, error) {
	if r == nil || actor.ID == 0 || r.character != actor.ID {
		return nil, errors.New("tutorial belongs to a different character")
	}
	switch op {
	case 37:
		if len(body) != 8 && len(body) != 16 {
			return nil, errors.New("tutorial loading request requires eight or sixteen bytes")
		}
		packets := []Packet{reply(37, protocol.Ack())}
		if r.ready {
			return packets, nil
		}
		loaded := loadedBody(actor.Level, actor.Experience, sp, tp)
		state := []byte{1}
		state = binary.LittleEndian.AppendUint16(state, actor.ID)
		state = append(state, 1)
		r.ready = true
		key := [2]byte{r.room.X, r.room.Y}
		r.seen[key] = true
		blocked := false
		for _, m := range r.room.Monsters {
			blocked = blocked || m.BlocksRoom
		}
		if !blocked {
			r.cleared[key] = true
		}
		return append(packets, notice(37, loaded), notice(30, make([]byte, 5)), notice(120, []byte{0, 0}), notice(36, protocol.Fatigue(actor)), notice(3, state)), nil
	case 191:
		if len(body) != 2 {
			return nil, errors.New("tutorial scene control requires two bytes")
		}
		if body[0] > 1 || body[1] != 0 && body[1] != 3 {
			return nil, nil
		}
		state := binary.LittleEndian.AppendUint16(nil, actor.ID)
		return []Packet{notice(170, append(state, body...))}, nil
	case 120:
		if len(body) != 0 {
			return nil, errors.New("tutorial sync requires an empty body")
		}
		return []Packet{reply(120, []byte{0, 0})}, nil
	case 143:
		if len(body) != 6 {
			return nil, errors.New("tutorial bootstrap requires six bytes")
		}
		return []Packet{reply(143, []byte{1, 0})}, nil
	case 45:
		return r.move(body, actor)
	}
	return nil, errors.New("unhandled tutorial command")
}

func loadedBody(level byte, experience uint64, sp, tp uint16) []byte {
	w := make([]byte, 91)
	w[0] = level
	binary.LittleEndian.PutUint64(w[1:], experience)
	binary.LittleEndian.PutUint16(w[13:], sp)
	binary.LittleEndian.PutUint16(w[15:], sp)
	binary.LittleEndian.PutUint16(w[17:], tp)
	binary.LittleEndian.PutUint16(w[19:], tp)
	return w
}

func (r *Run) actorBase(room Room) uint16 {
	n := int(r.firstActor)
	for _, row := range r.rooms {
		if row.X == room.X && row.Y == room.Y {
			return uint16(n)
		}
		n += len(row.Monsters) + len(row.Companions)
	}
	return 0
}

func (r *Run) move(body []byte, actor protocol.Actor) ([]Packet, error) {
	if len(body) != 64 && len(body) != 100 || body[10] > 1 {
		return nil, errors.New("invalid tutorial room movement body")
	}
	reject := []Packet{reply(45, []byte{0, 4, 0})}
	x, y := body[0], body[1]
	if body[10] != 0 {
		return reject, nil
	}
	if x == r.room.X && y == r.room.Y {
		if !r.ready {
			return []Packet{reply(45, protocol.Ack())}, nil
		}
		return reject, nil
	}
	if !r.ready || !r.cleared[[2]byte{r.room.X, r.room.Y}] {
		return reject, nil
	}
	var exit, entry byte
	switch [2]int{int(x) - int(r.room.X), int(y) - int(r.room.Y)} {
	case [2]int{1, 0}:
		exit, entry = 1, 4
	case [2]int{0, -1}:
		exit, entry = 2, 8
	case [2]int{-1, 0}:
		exit, entry = 4, 1
	case [2]int{0, 1}:
		exit, entry = 8, 2
	default:
		return reject, nil
	}
	for _, destination := range r.rooms {
		if destination.X != x || destination.Y != y {
			continue
		}
		if r.room.Gates&exit == 0 || destination.Gates&entry == 0 {
			return reject, nil
		}
		var payload []byte
		if r.seen[[2]byte{x, y}] {
			payload = []byte{x, y, 0}
			payload = binary.LittleEndian.AppendUint32(payload, r.seed)
			payload = append(payload, 2, 0, 1, 0, 0, 0, 0, 0, 255)
		} else {
			if r.definition.Dungeon.RoomFatigueCost != 0 {
				return nil, errors.New("new tutorial room requires a durable fatigue debit")
			}
			var err error
			payload, err = startMap(destination, r.seed, r.actorBase(destination))
			if err != nil {
				return nil, err
			}
		}
		r.room, r.ready = destination, false
		return []Packet{reply(45, protocol.Ack()), notice(29, payload), notice(36, protocol.Fatigue(actor))}, nil
	}
	return reject, nil
}
