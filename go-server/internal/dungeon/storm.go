package dungeon

import (
	"errors"
	"fmt"
	"math"
	"slices"
)

const StormGate uint16 = 182

func IsStormDungeon(id uint32) bool { return id == 100000003 || id >= 100000008 && id <= 100000150 }

func (s *Session) ChooseStorm(seed uint32, permitted, recent []uint32) (uint32, error) {
	var normal, hidden []uint32
	for _, id := range permitted {
		if !slices.Contains(s.permissions, id) || slices.Contains(recent, id) {
			continue
		}
		switch {
		case id == 100000003 || id >= 100000008 && id <= 100000138:
			normal = append(normal, id)
		case id >= 100000139 && id <= 100000150:
			hidden = append(hidden, id)
		}
	}
	if len(normal) == 0 && len(hidden) == 0 {
		return 0, errors.New("no current Storm Lane route outside recent runs")
	}
	if len(normal) == 0 {
		return hidden[seed%uint32(len(hidden))], nil
	}
	if len(hidden) == 0 {
		return normal[seed%uint32(len(normal))], nil
	}
	family := normal
	if seed%100 < 10 {
		family = hidden
	}
	return family[(seed/100)%uint32(len(family))], nil
}

func (r *Run) StormEntryKey() string {
	if r.Definition.Admission != "storm" {
		return ""
	}
	return "storm-lane-entry:" + r.idKey()
}

type StormDeathProof struct {
	Index                     byte
	Prerequisites             []uint16
	Hash                      string
	Arrival, PlayerAttributed bool `json:",omitempty"`
}

func (p StormDeathProof) Version() string {
	switch {
	case p.Arrival:
		return "TrustedLocalClientStormLaneArrival/v1"
	case p.PlayerAttributed:
		return "TrustedLocalClientStormLaneAttributedCleanup/v1"
	default:
		return "TrustedLocalClientStormLaneCleanup/v1"
	}
}

func (r *Run) stormProof(index int, body []byte) (*StormDeathProof, error) {
	d := r.Definition.Dungeon
	if r.Definition.Admission != "storm" || d.Difficulty != 4 || d.EntryFatigueCost != 8 || d.RoomFatigueCost != 0 {
		return nil, nil
	}
	room := r.Room
	owner, rows := le.Uint16(body[4:]), int(body[30])
	reason := body[68+14*rows]
	if rule := room.StormArrival; rule != nil && slices.Contains(rule.Targets, byte(index)) &&
		owner == math.MaxUint16 && rows == 0 && reason == 0 {
		if d.ID < 100000139 || d.ID > 100000150 || room.MapID != 100000218 || room.X != d.BossX || room.Y != d.BossY ||
			int(rule.Boss) >= len(room.Monsters) || room.Monsters[rule.Boss].Code != 109010261 ||
			room.Monsters[rule.Boss].Type != 3 || room.Monsters[rule.Boss].Dummy || len(rule.Targets) != 6 || !isHash(rule.Hash) ||
			room.Monsters[index].Type != 0 || room.Monsters[index].Dummy ||
			(room.Monsters[index].Code != 63035 && room.Monsters[index].Code != 63036) {
			return nil, errors.New("Storm Lane arrival scene differs from its frozen source")
		}
		return &StormDeathProof{Index: byte(index), Prerequisites: []uint16{}, Hash: rule.Hash, Arrival: true}, nil
	}
	rule := room.StormCleanup
	if rule == nil || int(rule.Index) != index {
		return nil, nil
	}
	m := room.Monsters[index]
	if !isHash(rule.Hash) || m.Type != 3 || !m.Dummy || len(rule.Prerequisites) != len(room.Monsters)-1 {
		return nil, errors.New("Storm Lane cleanup actor differs from its frozen source")
	}
	ok := owner == math.MaxUint16 && rows == 0 && (reason == 0 || reason == 7)
	if owner == r.Character && rows > 0 && reason == 0 {
		ok = true
		for i := range rows {
			one := body[31+i*14:]
			if le.Uint16(one) != 0x0111 || le.Uint16(one[2:]) != r.Character || le.Uint64(one[4:]) == 0 || le.Uint16(one[12:]) == 0 {
				ok = false
				break
			}
		}
	}
	if !ok {
		return nil, fmt.Errorf("invalid Storm Lane cleanup report")
	}
	var deps []uint16
	for i, n := range rule.Prerequisites {
		expected := i
		if i >= index {
			expected++
		}
		if int(n) != expected {
			return nil, errors.New("Storm Lane cleanup prerequisite order differs from the frozen room")
		}
		deps = append(deps, r.actorBase()+uint16(n))
	}
	if deps == nil {
		deps = []uint16{}
	}
	return &StormDeathProof{Index: byte(index), Prerequisites: deps, Hash: rule.Hash, PlayerAttributed: owner != math.MaxUint16}, nil
}
