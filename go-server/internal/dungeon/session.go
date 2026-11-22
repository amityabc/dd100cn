package dungeon

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"slices"
	"time"

	"s9a1/server/internal/opening"
	"s9a1/server/internal/protocol"
	"s9a1/server/internal/ruby"
	"s9a1/server/internal/storage"
)

type Session struct {
	character         uint16
	gate              uint16
	permissions       []uint32
	Run               *Run
	returning         bool
	ExitBody          []byte
	TournamentExit    *storage.TournamentWinProof
	WisdomExit        bool
	CardWindowRetired bool
}

func (s *Session) Active() bool    { return s.Run != nil }
func (s *Session) Returning() bool { return s.returning }

// GateSelection identifies the selector that currently owns a character.
func (s *Session) GateSelection(character uint16) (uint16, bool) {
	return s.gate, s.character == character && s.gate != 0 && s.Run == nil && !s.returning
}

func (r *Run) RubyClearedBosses() (keys []string, ok bool) {
	if r == nil || r.Room == nil || !r.Ready || !r.Cleared || r.PlayerDeath != nil || r.Room.X != r.Definition.Dungeon.BossX || r.Room.Y != r.Definition.Dungeon.BossY {
		return nil, false
	}
	for i, m := range r.Room.Monsters {
		actor := r.combatActorBase() + uint16(i)
		k, dead := r.Dead[actor]
		if m.BlocksRoom && !dead {
			return nil, false
		}
		if m.Type == 3 {
			if !dead || k.Key == "" {
				return nil, false
			}
			keys = append(keys, k.Key)
		}
	}
	return keys, len(keys) > 0
}

func (s *Session) Select(a protocol.Actor, gate uint16, target uint32, ids []uint32) ([]opening.Packet, bool) {
	if s.Active() || s.Returning() || gate == 0 || target != 0 && !slices.Contains(ids, target) {
		return nil, false
	}
	replay := target != 0 && s.character == a.ID && s.gate == gate && slices.Contains(s.permissions, target)
	if !replay {
		s.character, s.gate, s.permissions = a.ID, gate, ids
	}
	return Selector(a, ids, replay), true
}

func (s *Session) Permits(actor, gate uint16, id uint32) bool {
	return !s.returning && s.character == actor && s.gate == gate && slices.Contains(s.permissions, id)
}

func (s *Session) BeginReturn() {
	*s = Session{returning: true, CardWindowRetired: s.hasCardWindow()}
}

func (s *Session) BeginExit(body []byte) {
	*s = Session{returning: body[1] >= 2, ExitBody: slices.Clone(body), CardWindowRetired: s.hasCardWindow()}
}

func (s *Session) FinishReturn() {
	s.returning = false
}

func (s *Session) hasCardWindow() bool {
	return s.Run != nil && (s.Run.Settlement.Result != nil || s.Run.WisdomResultSent)
}

type cell struct{ x, y byte }
type scene struct {
	cell
	layer bool
}

type Run struct {
	Definition            Definition
	ID                    string
	Character             uint16
	Seed                  uint32
	Started               time.Time
	RewardLevel           byte
	Room                  *opening.Room
	Ready, Cleared        bool
	InLayer               bool
	Dead                  map[uint16]deathRecord
	GmApcs                []GameMasterAPC
	gmApcDead             map[uint16]bool
	NativeSummon          *storage.PotionSummonAuthorization
	nativeSummonActor     uint16
	Totals                [4]uint32
	PlayerDeath           *playerDeath
	LastRevived           bool
	DeathCycle            uint32
	Elapsed               uint32
	rooms                 map[cell]*opening.Room
	bases                 map[cell]uint16
	seen                  map[scene]bool
	visited, clearedRooms map[cell]bool
	layerSequence         *uint16
	pendingWisdomLayer    *uint16
	Requested             Entry
	Settlement            Settlement
	AbaddonEntry          *storage.AbaddonEntry
	AbaddonClock          *AbaddonClock
	AbaddonSymbols        byte
	OculusLayout          *oculusLayout
	OculusEntry           *storage.OculusEntry
	OculusGeneration      uint32
	RubyEntry             *ruby.Entry
	WisdomEntry           *storage.WisdomAdmission
	WisdomResultSent      bool
	ground                map[groundKey]GroundDrop
	picked                map[groundKey][]byte
	nextGroundSlot        uint16
	passive               map[string]passiveReceipt
	cinematicDeaths       map[uint16][]byte
}

type playerDeath struct {
	x, y, related uint16
	at            time.Time
	input         string
}

func (r *Run) EntryReplay(req Entry) bool {
	return !r.Ready && r.Requested.Dungeon == req.Dungeon && r.Definition.Dungeon.Difficulty == req.Difficulty &&
		r.Definition.Dungeon.ChallengeStage == req.ChallengeStage
}

func (r *Run) RoomCell() [2]byte { return [2]byte{r.Room.X, r.Room.Y} }

func (r *Run) PetClockSpending() bool { return r.Ready || len(r.seen) != 0 }

func (r *Run) FirstActor() uint16 {
	start := r.Definition.Dungeon.Start
	return r.bases[cell{start.X, start.Y}]
}

func (r *Run) CanChangeEquipment() bool {
	if r == nil || r.ID == "" || !r.Ready || r.InLayer || r.PlayerDeath != nil || r.Room == nil ||
		r.Definition.Dungeon.ResourceHash == "" {
		return false
	}
	k := cell{r.Room.X, r.Room.Y}
	return r.seen[scene{k, false}] && r.rooms[k] == r.Room
}

func (r *Run) scene() *opening.Room {
	if r.InLayer {
		return r.Room.CinematicLayer
	}
	return r.Room
}

func (r *Run) CurrentScene() *opening.Room { return r.scene() }

func (r *Run) actorBase() uint16 {
	base := r.bases[cell{r.Room.X, r.Room.Y}]
	if r.InLayer && sharedLayerQuest(r.Definition, r.Room) == 0 {
		base += uint16(len(r.Room.Monsters) + len(r.Room.Companions))
	}
	return base
}

func (r *Run) stage(roomScene *opening.Room, inLayer bool, transition byte) ([]byte, error) {
	key := cell{roomScene.X, roomScene.Y}
	base := r.bases[key]
	if inLayer {
		room := r.rooms[key]
		if sharedLayerQuest(r.Definition, room) == 0 && !combatStageLayer(r.Definition, room) {
			base += uint16(len(room.Monsters) + len(room.Companions))
		}
	}
	return startMap(roomScene, r.Seed, base, transition, !r.seen[scene{key, inLayer}], r.Definition.Dungeon.ID == WisdomDungeon)
}

func NewSeed() (uint32, error) {
	var raw [4]byte
	_, err := rand.Read(raw[:])
	return le.Uint32(raw[:]), err
}

func Prepare(d Definition, req Entry, a protocol.Actor, first uint16, seed uint32) (*Run, uint16, error) {
	if d.Dungeon.Tournament != nil {
		var err error
		d, err = freezeTournament(d, seed)
		if err != nil {
			return nil, 0, err
		}
	}
	def := d.Dungeon.Dungeon
	reserved := def
	reserved.Start = excludeSharedLayerActors(d, def.Start)
	reserved.Rooms = slices.Clone(def.Rooms)
	for i, room := range reserved.Rooms {
		reserved.Rooms[i] = excludeSharedLayerActors(d, room)
	}
	base, next, err := (opening.Definition{Dungeon: reserved}).Reserve(first)
	if err != nil {
		return nil, 0, err
	}
	if a.Level < def.MinimumLevel || a.ID == 0 {
		return nil, 0, errors.New("dungeon level or character changed")
	}
	raw := make([]byte, 16)
	if _, err := rand.Read(raw); err != nil {
		return nil, 0, err
	}
	raw[6], raw[8] = raw[6]&15|64, raw[8]&63|128
	id := hex.EncodeToString(raw)
	id = id[:8] + "-" + id[8:12] + "-" + id[12:16] + "-" + id[16:20] + "-" + id[20:]
	r := &Run{Definition: d, ID: id, Seed: seed, Character: a.ID, Requested: req,
		Started: time.Now(), RewardLevel: a.Level, Room: &def.Start, Dead: make(map[uint16]deathRecord),
		rooms: make(map[cell]*opening.Room), bases: make(map[cell]uint16), seen: make(map[scene]bool),
		visited: make(map[cell]bool), clearedRooms: make(map[cell]bool), ground: make(map[groundKey]GroundDrop),
		picked: make(map[groundKey][]byte), nextGroundSlot: 1, passive: make(map[string]passiveReceipt), cinematicDeaths: make(map[uint16][]byte)}
	rooms := []*opening.Room{r.Room}
	others := make([]*opening.Room, 0, len(def.Rooms))
	for i := range def.Rooms {
		row := &def.Rooms[i]
		if row.X == r.Room.X && row.Y == r.Room.Y {
			continue
		}
		others = append(others, row)
	}
	slices.SortFunc(others, func(a, b *opening.Room) int {
		if a.Y != b.Y {
			return int(a.Y) - int(b.Y)
		}
		return int(a.X) - int(b.X)
	})
	rooms = append(rooms, others...)
	for _, room := range rooms {
		key := cell{room.X, room.Y}
		r.rooms[key], r.bases[key] = room, base
		base += uint16(len(room.Monsters) + len(room.Companions))
		if layer := room.CinematicLayer; layer != nil {
			base += uint16(len(layer.Companions))
			if sharedLayerQuest(d, room) == 0 && !combatStageLayer(d, room) {
				base += uint16(len(layer.Monsters))
			}
		}
	}
	r.visited[cell{r.Room.X, r.Room.Y}] = true
	return r, next, nil
}

func excludeSharedLayerActors(d Definition, room opening.Room) opening.Room {
	if sharedLayerQuest(d, &room) != 0 {
		layer := *room.CinematicLayer
		layer.Monsters = nil
		room.CinematicLayer = &layer
	}
	return room
}

func (r *Run) EntryFrames(a protocol.Actor, native140, nativeParty bool) ([]opening.Packet, error) {
	before, err := protocol.Mode0(a, native140)
	if err != nil {
		return nil, err
	}
	stats, err := protocol.Mode1(a, false)
	if err != nil {
		return nil, err
	}
	full, err := protocol.Mode1(a, true)
	if err != nil {
		return nil, err
	}
	var frames []opening.Packet
	if !nativeParty {
		frames = append(frames, notice(9, protocol.PartyRemove(a.ID, a.Group, a.Channel, false)))
	}
	d := r.Definition.Dungeon.Dungeon
	frames = append(frames, reply(16, EntryAccepted(a.ID)), notice(5, permissions([]uint32{d.ID})), notice(2, before), notice(2, stats))
	if r.TournamentReward() != 0 {
		frames = append(frames, r.tournamentFrames()...)
	} else {
		start, err := r.stage(r.Room, false, 0)
		if err != nil {
			return nil, err
		}
		body := info(d)
		if reward := r.Definition.WisdomReward; reward != nil {
			kind := reward.display()
			body[10], body[11], body[12] = d.BossX, d.BossY, kind
			frames = append(frames, notice(28, body), notice(1774, []byte{kind, kind}))
		} else {
			frames = append(frames, notice(28, body))
		}
		frames = append(frames, notice(29, start))
	}
	frames = append(frames, notice(36, protocol.Fatigue(a)), notice(2, full))
	if !nativeParty {
		display, err := displayAdd(a)
		if err != nil {
			return nil, err
		}
		frames = append(frames, notice(9, display))
	}
	return append(frames, notice(120, []byte{0, 0})), nil
}

func (r *Run) Load(a protocol.Actor, sp, tp uint16) []opening.Packet {
	if r.Ready {
		return []opening.Packet{reply(37, protocol.Ack())}
	}
	r.Ready = true
	k := cell{r.Room.X, r.Room.Y}
	r.seen[scene{k, r.InLayer}] = true
	return loaded(a, sp, tp)
}

func combatStageLayer(d Definition, room *opening.Room) bool {
	return d.Dungeon.ID == 100000326 && room.MapID == 100001177 && room.X == 2 && room.Y == 0 &&
		room.CinematicLayer != nil && room.CinematicLayer.MapID == 100001178
}

// PrepareLoadedStage promotes the two story layers that become ordinary
// loaded rooms. Empty-room completion and later combat see the new map.
func (r *Run) PrepareLoadedStage() {
	if r.Ready || !r.InLayer || r.Room.CinematicLayer == nil {
		return
	}
	if combatStageLayer(r.Definition, r.Room) || (r.Definition.Dungeon.ID == 100002482 && r.Room.CinematicLayer.MapID == 100003347) {
		r.Room = r.Room.CinematicLayer
		r.InLayer = false
	}
}

type Move struct {
	room     *opening.Room
	layer    bool
	seq      *uint16
	Start    []byte
	Cost     uint16
	Replayed bool
	Deferred bool
	Proof    []string
}

func (r *Run) PlanMove(x, y byte, layer bool, sequence uint16) (Move, error) {
	if r.PlayerDeath != nil || r.AbaddonFailed() || r.Settlement.Confirmation != nil || r.TournamentReward() != 0 {
		return Move{}, nil
	}
	src := cell{r.Room.X, r.Room.Y}
	dst := cell{x, y}
	if layer {
		if r.OculusLayout != nil {
			return Move{}, nil
		}
		id := r.Definition.Dungeon.ID
		repeat := id == 100000006 || id >= 100000326
		if src != dst || r.Room.CinematicLayer == nil || !r.Ready || r.layerSequence != nil && *r.layerSequence == sequence ||
			!r.seen[scene{src, false}] || !r.InLayer && r.seen[scene{src, true}] && !repeat {
			return Move{}, nil
		}
		target, transition := r.Room.CinematicLayer, byte(1)
		if r.InLayer {
			target, transition = r.Room, 2
		}
		var proof []string
		if !r.InLayer && sharedLayerQuest(r.Definition, r.Room) == 6422 {
			actors, ok := r.awakeningPredecessors()
			if !ok {
				return Move{}, nil
			}
			for _, actor := range actors {
				proof = append(proof, r.Dead[actor].Key)
			}
		}
		if !r.InLayer && r.wisdomStoryRoom() {
			_, key, qualified := r.wisdomBattleProof()
			if !qualified {
				if r.pendingWisdomLayer == nil {
					r.pendingWisdomLayer = &sequence
				}
				return Move{Deferred: true}, nil
			}
			proof = append(proof, key)
		}
		if r.Room.ScriptTransfer != nil {
			if r.InLayer || !r.scriptTransferValid() {
				return Move{}, nil
			}
			for _, index := range r.Room.ScriptTransfer.RequiredDeaths {
				death, dead := r.Dead[r.actorBase()+uint16(index)]
				if !dead {
					return Move{}, nil
				}
				proof = append(proof, death.Key)
			}
		}
		start, err := r.stage(target, !r.InLayer, transition)
		return Move{room: r.Room, layer: !r.InLayer, seq: &sequence, Start: start, Proof: proof}, err
	}
	if r.InLayer {
		if r.scriptTransferValid() {
			t := r.Room.ScriptTransfer
			d := r.rooms[dst]
			if !r.Ready || !r.seen[scene{src, true}] || x != t.X || y != t.Y || d == nil || d.MapID != t.Map {
				return Move{}, nil
			}
			return r.prepareMoveRoom(d, dst)
		}
		if !r.layerCanExitByDoor() {
			return Move{}, nil
		}
	}
	if src == dst && !r.Ready {
		return Move{Replayed: true}, nil
	}
	if src == dst || !r.Ready || r.Cleared ||
		!r.clearedRooms[src] && slices.ContainsFunc(r.Room.Monsters, func(m opening.Monster) bool { return m.BlocksRoom }) {
		return Move{}, nil
	}
	d := r.rooms[dst]
	if r.OculusLayout != nil && d != nil && oculusFloor(src.x, src.y) != oculusFloor(dst.x, dst.y) {
		proof, ok := r.oculusStairs(src, dst)
		if !ok {
			return Move{}, nil
		}
		m, err := r.prepareMoveRoom(d, dst)
		m.Proof = proof
		return m, err
	}
	if d != nil && r.Definition.Dungeon.Abaddon && slices.Contains(r.Definition.Dungeon.Warps,
		struct{ SourceX, SourceY, DestinationX, DestinationY byte }{src.x, src.y, x, y}) {
		return r.prepareMoveRoom(d, dst)
	}
	var exit, entrance byte
	switch [2]int{int(x) - int(src.x), int(y) - int(src.y)} {
	case [2]int{1, 0}:
		exit, entrance = 1, 4
	case [2]int{0, -1}:
		exit, entrance = 2, 8
	case [2]int{-1, 0}:
		exit, entrance = 4, 1
	case [2]int{0, 1}:
		exit, entrance = 8, 2
	}
	if d == nil || r.Room.Gates&exit == 0 || d.Gates&entrance == 0 {
		return Move{}, nil
	}
	return r.prepareMoveRoom(d, dst)
}

func (r *Run) prepareMoveRoom(d *opening.Room, dst cell) (Move, error) {
	var cost uint16
	if !r.visited[dst] {
		cost = r.Definition.Dungeon.RoomFatigueCost
	}
	d, ok := r.memoryLandBossRoom(d)
	if !ok {
		return Move{}, nil
	}
	start, err := r.stage(d, false, 0)
	return Move{room: d, Start: start, Cost: cost}, err
}

func (r *Run) scriptTransferValid() bool {
	def := r.Definition.Dungeon
	t := r.Room.ScriptTransfer
	if def.ID != 7123 || len(def.QuestConnections) == 0 || t == nil || !isHash(t.Hash) ||
		r.Room.X != 3 || r.Room.Y != 0 || r.Room.MapID != 57792 || r.Room.CinematicLayer == nil || r.Room.CinematicLayer.MapID != 53663 ||
		t.X != 3 || t.Y != 1 || t.Map != 53545 || !slices.Equal(t.RequiredDeaths, []byte{0, 1, 3}) {
		return false
	}
	required := [...]uint32{65455, 65456, 63821, 65455}
	if len(r.Room.Monsters) != len(required) {
		return false
	}
	for i, m := range r.Room.Monsters {
		if m.Code != required[i] {
			return false
		}
	}
	return true
}

func (r *Run) layerCanExitByDoor() bool {
	def := r.Definition.Dungeon
	return def.ID == 53 && def.MazeIndex == 2 && r.Room.X == 1 && r.Room.Y == 2 && r.Room.MapID == 76383 ||
		def.ID == 9609 || def.ID == 100000006 || def.ID >= 100000326 ||
		def.ID == 7123 && r.Room.X == 3 && r.Room.Y == 0 && r.Room.CinematicLayer.MapID == 53663
}

func (r *Run) ApplyMove(m Move, a protocol.Actor) []opening.Packet {
	if m.Replayed {
		return []opening.Packet{reply(45, protocol.Ack())}
	}
	if m.room == nil {
		return []opening.Packet{reply(45, []byte{0, 4, 0})}
	}
	r.Room, r.InLayer, r.Ready = m.room, m.layer, false
	r.AbaddonClock = nil
	r.pendingWisdomLayer = nil
	r.Settlement.Pending = nil
	clear(r.Settlement.pendingByActor)
	clear(r.cinematicDeaths)
	if m.seq != nil {
		r.layerSequence = m.seq
	}
	r.visited[cell{r.Room.X, r.Room.Y}] = true
	frames := []opening.Packet{reply(45, protocol.Ack())}
	if r.OculusLayout != nil {
		frames = append(frames, notice(1770, r.OculusRooms()))
	}
	if reward := r.Definition.WisdomReward; reward != nil && !r.seen[scene{cell{r.Room.X, r.Room.Y}, r.InLayer}] {
		kind := reward.display()
		frames = append(frames, notice(1774, []byte{kind, kind}))
	}
	return append(frames, notice(29, m.Start), notice(36, protocol.Fatigue(a)))
}

func (r *Run) SceneControl(body []byte) ([]opening.Packet, error) {
	if len(body) != 2 {
		return nil, errors.New("dungeon scene control requires two bytes")
	}
	if r.PlayerDeath != nil || body[0] > 1 || body[1] != 0 && body[1] != 3 {
		return nil, nil
	}
	return []opening.Packet{notice(170, append(le.AppendUint16(nil, r.Character), body...))}, nil
}
