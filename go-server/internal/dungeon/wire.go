package dungeon

import (
	"encoding/binary"
	"errors"
	"math"

	"s9a1/server/internal/opening"
	"s9a1/server/internal/protocol"
)

var le = binary.LittleEndian

type Entry struct {
	Dungeon, QuestHint, Field11         uint32
	Difficulty, Field7, Field8, Field15 byte
	ChallengeStage, Field9              uint16
	Flag                                byte
	NativeExtension                     *uint32
}

func ReadSelector(b []byte) (uint32, error) {
	if len(b) != 4 && len(b) != 6 || len(b) == 6 && le.Uint16(b[4:]) != 0 {
		return 0, errors.New("invalid dungeon selector body")
	}
	target := le.Uint32(b)
	if target > math.MaxInt32 {
		return 0, errors.New("selector target exceeds native signed width")
	}
	return target, nil
}

func ReadEntry(b []byte) (Entry, error) {
	if len(b) != 21 && len(b) != 25 {
		return Entry{}, errors.New("dungeon entry must be 21 or 25 bytes")
	}
	e := Entry{
		Dungeon: le.Uint32(b), Difficulty: b[4], ChallengeStage: le.Uint16(b[5:]),
		Field7: b[7], Field8: b[8], Field9: le.Uint16(b[9:]), Field11: le.Uint32(b[11:]),
		Field15: b[15], QuestHint: le.Uint32(b[16:]), Flag: b[20],
	}
	if len(b) == 25 {
		v := le.Uint32(b[21:])
		e.NativeExtension = &v
	}
	return e, nil
}

func (e Entry) Supported() bool {
	if e.Dungeon == WisdomDungeon && e.Difficulty <= 1 && e.ChallengeStage == 0 &&
		(e.Field7 == 0 && e.Field8 <= 1 || e.Field7 == 1 && e.Field8 == 0) {
		e.Field7, e.Field8 = 0, 0
	}
	return e.Dungeon <= math.MaxInt32 && e.Difficulty <= 4 && e.ChallengeStage <= 5 &&
		(e.Field7 == 0 || e.Difficulty == 4 && e.Field7 == 6) && e.Field8 == 0 &&
		(e.Field9 == 0 || e.Field9 == math.MaxUint16) && e.Field11 == 0 && e.Field15 == 0 &&
		e.Flag == 0 && (e.NativeExtension == nil || *e.NativeExtension == 0)
}

func ReadMove(b []byte) (x, y byte, layer bool, err error) {
	if len(b) != 64 && len(b) != 100 || b[10] > 1 {
		return 0, 0, false, errors.New("invalid native room move")
	}
	return b[0], b[1], b[10] == 1, nil
}

func notice(op uint16, body []byte) opening.Packet {
	return opening.Packet{Class: 0, Opcode: op, Body: body}
}

func reply(op uint16, body []byte) opening.Packet {
	return opening.Packet{Class: 1, Opcode: op, Body: body}
}

func permissions(ids []uint32) []byte {
	body := le.AppendUint16(make([]byte, 0, 2+len(ids)*5), uint16(len(ids)))
	for _, id := range ids {
		body = append(le.AppendUint32(body, id), 1)
	}
	return body
}

func EntryAccepted(actor uint16) []byte {
	return le.AppendUint16([]byte{1, 3}, actor)
}

func Selector(a protocol.Actor, ids []uint32, replay bool) []opening.Packet {
	frames := []opening.Packet{reply(15, protocol.Ack())}
	if replay {
		return frames
	}
	return append(frames, notice(36, protocol.Fatigue(a)), notice(5, permissions(ids)), notice(27, SelectorPreparation()))
}

func SelectorPreparation() []byte { return make([]byte, 47) }

func info(d opening.Dungeon) []byte {
	body := make([]byte, 36)
	le.PutUint32(body, d.ID)
	body[4] = d.Difficulty
	le.PutUint16(body[5:], d.ChallengeStage)
	body[7], body[8], body[9], body[10], body[11] = d.MazeIndex, d.BossX, d.BossY, 255, 255
	body[16] = 12
	le.PutUint32(body[20:], math.MaxUint32)
	return body
}

func startMap(room *opening.Room, seed uint32, base uint16, transition byte, create, wisdom bool) ([]byte, error) {
	count := len(room.Monsters) + len(room.Companions)
	if transition > 2 || count > 255 || count > 0 && (base < 402 || int(base)+count > math.MaxUint16) {
		return nil, errors.New("invalid dungeon scene projection")
	}
	body := make([]byte, 0, 23+count*21)
	body = le.AppendUint32(append(body, room.X, room.Y, transition), seed)
	body = append(body, 2, 0, 1, 0, 0, 0)
	if !create {
		return append(body, 0, 0, 255), nil
	}
	body = le.AppendUint32(append(body, 1), room.MapID)
	body = append(body, byte(count))
	spawn := func(i int, phase uint16, index, code uint32, level, category byte) {
		body = le.AppendUint16(body, phase)
		body = le.AppendUint32(body, index)
		body = le.AppendUint16(body, base+uint16(i))
		body = le.AppendUint32(body, code)
		body = append(body, level, category, 0)
		if phase != 0 {
			body = le.AppendUint32(append(body, 1), 100)
		} else {
			body = le.AppendUint32(append(body, 0), 0)
		}
		body = append(body, 0)
	}
	for i, m := range room.Monsters {
		if m.Type > 3 || m.HellPhase != 0 && (!wisdom || m.AiScriptIndex != nil || m.NativeSpawnIndex != nil ||
			m.HellPhase != 1 && m.HellPhase != 2 && m.HellPhase != 3 && m.HellPhase != 8) {
			return nil, errors.New("unsupported ordinary spawn category or Hell phase")
		}
		index, category := uint32(i), m.Type
		switch {
		case m.AiScriptIndex != nil:
			if *m.AiScriptIndex >= 64 || m.NativeSpawnIndex != nil || m.Code == 0 || m.Level == 0 || m.Type != 0 && m.Type != 3 {
				return nil, errors.New("invalid dungeon APC spawn")
			}
			category = 5
			if m.Type == 3 {
				category = 8
			}
			index = uint32(*m.AiScriptIndex)
		case m.NativeSpawnIndex != nil:
			if *m.NativeSpawnIndex < 10000 || *m.NativeSpawnIndex >= 20000 || m.Code == 0 || m.Level == 0 {
				return nil, errors.New("invalid dungeon native spawn index")
			}
			index = *m.NativeSpawnIndex
		}
		spawn(i, m.HellPhase, index, m.Code, m.Level, category)
	}
	for i, c := range room.Companions {
		if c.Code == 0 || c.Level == 0 || c.Type != 5 || c.ScriptIndex >= 64 {
			return nil, errors.New("invalid dungeon companion")
		}
		spawn(i+len(room.Monsters), 0, uint32(c.ScriptIndex), c.Code, c.Level, c.Type)
	}
	return append(body, 0, 0, 0, 255), nil
}

func loaded(a protocol.Actor, sp, tp uint16) []opening.Packet {
	w := make([]byte, 91)
	w[0] = a.Level
	le.PutUint64(w[1:], a.Experience)
	le.PutUint16(w[13:], sp)
	le.PutUint16(w[15:], sp)
	le.PutUint16(w[17:], tp)
	le.PutUint16(w[19:], tp)
	state := append(le.AppendUint16([]byte{1}, a.ID), 1)
	return []opening.Packet{reply(37, protocol.Ack()), notice(37, w), notice(30, make([]byte, 5)),
		notice(120, []byte{0, 0}), notice(36, protocol.Fatigue(a)), notice(3, state)}
}

func displayAdd(a protocol.Actor) ([]byte, error) {
	name, err := protocol.EncodeText(a.Name, 30)
	if err != nil {
		return nil, err
	}
	w := le.AppendUint16([]byte{1, 0, 0x0f, 0x27}, a.ID)
	w = append(w, a.Group, a.Channel, 0, 0, 0, 0)
	w = le.AppendUint32(w, uint32(len(name)))
	w = append(w, name...)
	w = append(w, a.Job, a.Level, 0, 0, 0, 0, a.GrowType)
	return append(w, make([]byte, 11)...), nil
}
