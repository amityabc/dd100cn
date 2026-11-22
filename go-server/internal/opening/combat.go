package opening

import (
	"context"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"slices"
	"strings"
	"time"

	"s9a1/server/internal/protocol"
	"s9a1/server/internal/savejson"
	"s9a1/server/internal/storage"
)

type legacyString = savejson.String

type receiptInput struct {
	Source, Version         string
	Account                 legacyString
	OwnerGuid               string
	Epoch                   int64
	Character               uint16
	RunId                   string
	Dungeon                 uint32
	MazeIndex, RoomX, RoomY byte
	MapId                   uint32
	Maps                    []uint32
	Actor                   uint16
	Code                    uint32
	Level, Type             byte
	BlocksRoom              bool
	ResourceHash            string
	ExperienceMultiplier    json.Number
	RewardLevel             byte
	ExperienceHash          string
	ExperienceAward         uint32
	Class                   byte
	Opcode                  uint16
	Body                    legacyString
	AiScriptIndex           *byte `json:",omitempty"`
	Dummy                   bool  `json:",omitempty"`
}

type receiptResult struct {
	Actor                       uint16
	ExperienceAward             uint32
	RoomCleared, DungeonCleared bool
	Maps                        []uint32
	ClearElapsedMilliseconds    uint32
	Totals                      [4]uint32
	Drops                       []struct{}
	DropRuleHash                string
}

type deathRecord struct {
	input, result string
	fields        receiptResult
}

type deathStore interface {
	CommitTutorialDeath(context.Context, storage.Lease, storage.TutorialDeath) (storage.TutorialDeathResult, error)
	CompleteTutorial(context.Context, storage.Lease, uint16, int64, storage.TutorialDefinition) (storage.Character, error)
}

func (r *Run) proof() storage.TutorialDefinition {
	p := r.definition.ReturnPoint
	return storage.TutorialDefinition{Job: r.definition.Job, Dungeon: r.definition.Dungeon.ID, ResourceHash: r.definition.Hash,
		ReturnPoint: storage.Position{Town: uint16(p.Town), Area: uint16(p.Area), X: p.X, Y: p.Y, Direction: p.Direction}}
}

func (r *Run) result(actor uint16, actual uint32, monster Monster, maps []uint32) receiptResult {
	base := r.actorBase(r.room)
	blocked, hasType3, deadType3, dummy, allFake, fakeCode, allDead := false, false, false, false, true, false, true
	for i, m := range r.room.Monsters {
		_, dead := r.deaths[base+uint16(i)]
		dead = dead || base+uint16(i) == actor
		blocked = blocked || m.BlocksRoom && !dead
		deadType3 = deadType3 || m.Type == 3 && dead
		hasType3 = hasType3 || m.Type == 3
		dummy = dummy || m.BlocksRoom && m.Dummy
		allFake = allFake && (!m.BlocksRoom || m.Type != 3 || m.Dummy)
		fakeCode = fakeCode || m.Code == 75100
		allDead = allDead && dead
	}
	already := r.cleared[[2]byte{r.room.X, r.room.Y}]
	cleared := !blocked && !already
	final := r.finalRoom()
	win := cleared && final && (deadType3 || dummy && allFake || !hasType3 && fakeCode && allDead)
	result := receiptResult{Actor: actor, ExperienceAward: actual, RoomCleared: cleared, DungeonCleared: win, Maps: maps, Totals: r.totals, Drops: []struct{}{}}
	if win {
		result.ClearElapsedMilliseconds = uint32(min(int64(math.MaxUint32), max(int64(0), time.Since(r.started).Milliseconds())))
	}
	kind := min(monster.Type, byte(3))
	result.Totals[kind] = uint32(min(uint64(math.MaxUint32), uint64(result.Totals[kind])+uint64(actual)))
	return result
}

func (r *Run) finalRoom() bool {
	return r.room.X == r.definition.Dungeon.BossX && r.room.Y == r.definition.Dungeon.BossY
}

// Death is the entry point for the native player-attributed C39 report. The
// locked store validates the lease, receipt and character revision together.
// A failed final return can be retried with the same committed death receipt.
func (r *Run) Death(ctx context.Context, db deathStore, l storage.Lease, c storage.Character, body []byte, cat *CombatCatalog, project func(storage.Character) (protocol.Actor, error)) ([]Packet, bool, error) {
	if len(body) < 74 || len(body) != 74+14*int(body[30]) {
		return nil, false, errors.New("malformed C39 death report")
	}
	target, owner := binary.LittleEndian.Uint32(body), binary.LittleEndian.Uint16(body[4:])
	reject := func() ([]Packet, bool, error) {
		b := binary.LittleEndian.AppendUint16([]byte{0}, uint16(min(uint32(math.MaxUint16), target)))
		return []Packet{reply(39, b)}, false, nil
	}
	if target == 0 && owner == math.MaxUint16 {
		return nil, false, nil
	}
	if r == nil || !r.ready || c.ID != r.character || owner != c.ID || target < 402 || target >= math.MaxUint16 {
		return reject()
	}
	idx := int(target) - int(r.actorBase(r.room))
	if idx < 0 || idx >= len(r.room.Monsters) {
		return reject()
	}
	if cat == nil || len(cat.ExperienceHash) != 64 {
		return nil, false, errors.New("tutorial experience catalog is unavailable")
	}
	job, ok := cat.Jobs[c.Job]
	if !ok {
		return nil, false, errors.New("missing tutorial job experience")
	}
	actor := uint16(target)
	monster := r.room.Monsters[idx]
	award, err := job.award(monster, r.rewardLevel)
	if err != nil {
		return nil, false, err
	}
	maps := append(slices.Clone(r.room.MapAliases), r.room.MapID)
	slices.Sort(maps)
	maps = slices.Compact(maps)
	input := receiptInput{Source: "ClientDelegated", Version: "TrustedLocalClientCombat/v1", Account: legacyString(l.Account), OwnerGuid: l.Owner, Epoch: l.Epoch, Character: c.ID,
		RunId: r.runID, Dungeon: r.definition.Dungeon.ID, MazeIndex: r.definition.Dungeon.MazeIndex, RoomX: r.room.X, RoomY: r.room.Y, MapId: r.room.MapID, Maps: maps, Actor: actor, Code: monster.Code,
		Level: monster.Level, Type: monster.Type, BlocksRoom: monster.BlocksRoom, ResourceHash: r.definition.Dungeon.ResourceHash, ExperienceMultiplier: job.ExperienceMultiplier,
		RewardLevel: r.rewardLevel, ExperienceHash: cat.ExperienceHash, ExperienceAward: award, Class: 1, Opcode: 39,
		Body: legacyString(base64.StdEncoding.EncodeToString(body)), AiScriptIndex: monster.AiScriptIndex, Dummy: monster.Dummy}
	wire, err := json.Marshal(input)
	if err != nil {
		return nil, false, err
	}
	record, prior := r.deaths[actor]
	next := c
	var p progress
	if prior {
		if record.input != string(wire) {
			return reject()
		}
	} else {
		var actual uint32
		next, p, actual, err = cat.apply(c, award)
		if err != nil {
			return nil, false, err
		}
		result := r.result(actor, actual, monster, maps)
		encoded, err := json.Marshal(result)
		if err != nil {
			return nil, false, err
		}
		record = deathRecord{input: string(wire), result: string(encoded), fields: result}
	}
	key := fmt.Sprintf("dungeon-death:%s:%d:%d:%d:%d", strings.ReplaceAll(r.runID, "-", ""), r.definition.Dungeon.MazeIndex, r.room.X, r.room.Y, actor)
	saved, err := db.CommitTutorialDeath(ctx, l, storage.TutorialDeath{CharacterID: c.ID, Revision: c.Revision, Opening: r.proof(), Level: next.Level, Stats: next.Stats, Gameplay: next.Gameplay,
		ReceiptKey: key, Input: record.input, Result: record.result, DungeonResourceHash: r.definition.Dungeon.ResourceHash, ExperienceHash: cat.ExperienceHash})
	if err != nil {
		return nil, false, err
	}
	r.deaths[actor] = record
	if !prior {
		r.totals = record.fields.Totals
	}
	if record.fields.RoomCleared {
		r.cleared[[2]byte{r.room.X, r.room.Y}] = true
	}
	b := binary.LittleEndian.AppendUint16(nil, actor)
	b = append(b, 0, 0, 0, 0, 255, 0)
	packets := []Packet{notice(38, b)}
	if !prior && !saved.Replayed && (record.fields.ExperienceAward > 0 || saved.Character.Level != c.Level) {
		packets = append(packets, notice(37, loadedBody(saved.Character.Level, p.Experience, p.Sp, p.Tp)))
	}
	if !r.finalRoom() || !r.cleared[[2]byte{r.room.X, r.room.Y}] {
		return packets, false, nil
	}
	// At least one committed blocker receipt must establish clear in the
	// terminal room. The stock tutorial finish uses this separate proof even
	// where the room contains no type-three boss.
	hasProof := false
	base := r.actorBase(r.room)
	for i, m := range r.room.Monsters {
		death, ok := r.deaths[base+uint16(i)]
		hasProof = hasProof || m.BlocksRoom && ok && death.fields.RoomCleared
	}
	if !hasProof {
		return packets, false, nil
	}
	final, err := db.CompleteTutorial(ctx, l, c.ID, saved.Character.Revision, r.proof())
	if err != nil {
		return packets, false, err
	}
	a, err := project(final)
	if err != nil {
		return packets, true, err
	}
	full, err := protocol.Mode1(a, true)
	if err != nil {
		return packets, true, err
	}
	return append(packets, notice(2, full), notice(23, protocol.Area(a)), notice(24, protocol.Town(a))), true, nil
}
