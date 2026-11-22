// Package duellobby projects the two original duel lobbies. The lobby
// location belongs to the connection and stays separate from the saved town.
package duellobby

import (
	"encoding/json"
	"errors"

	"s9a1/server/internal/opening"
	"s9a1/server/internal/protocol"
	"s9a1/server/internal/storage"
)

const MinimumLevel byte = 50

func IsChannel(ch uint16) bool { return ch == 251 || ch == 252 }

func Spawn() storage.Position {
	return storage.Position{Town: 10, Area: 0, X: 474, Y: 234, Direction: 5}
}

func ChannelInfo(ready bool) []byte {
	if ready {
		return []byte{1, 0, 0, 0, 0, 0}
	}
	return []byte{0, 21, 0}
}

type skillBuild struct {
	Skills       json.RawMessage
	Sp, Tp       uint16
	CommandLocks json.RawMessage `json:",omitempty"`
}

func (b skillBuild) validate() error {
	var skills []protocol.ActorSkill
	if err := json.Unmarshal(b.Skills, &skills); err != nil || skills == nil || len(skills) > 255 {
		return errors.New("invalid learned skill count")
	}
	var slots [255]bool
	ids := make(map[uint16]bool, len(skills))
	for _, s := range skills {
		if s.ID == 0 || s.Level == 0 || s.Slot > 254 || slots[s.Slot] || ids[s.ID] {
			return errors.New("invalid or duplicate learned skill in duel page")
		}
		slots[s.Slot], ids[s.ID] = true, true
	}
	if len(b.CommandLocks) != 0 && string(b.CommandLocks) != "null" {
		var locks []byte
		if err := json.Unmarshal(b.CommandLocks, &locks); err != nil || len(locks) != 512 {
			return errors.New("invalid duel command lock configuration")
		}
	}
	return nil
}

// InitialSkills is the LocalSkillBuilds.Capture used on a character's first
// duel synchronization. The store calls it with the locked current character.
func InitialSkills(c storage.Character) (json.RawMessage, error) {
	var p struct {
		Initialized          bool
		Skills, CommandLocks json.RawMessage
		Progression          *struct{ Sp, Tp uint16 }
	}
	if err := json.Unmarshal(c.Gameplay, &p); err != nil {
		return nil, err
	}
	if !p.Initialized || p.Progression == nil {
		return nil, errors.New("duel lobby needs initialized gameplay")
	}
	b := skillBuild{Skills: p.Skills, Sp: p.Progression.Sp, Tp: p.Progression.Tp, CommandLocks: p.CommandLocks}
	if string(b.CommandLocks) == "null" {
		b.CommandLocks = nil
	}
	if err := b.validate(); err != nil {
		return nil, err
	}
	return json.Marshal(b)
}

// Display selects the saved duel skill page (or the current skills before
// the first duel customization), preserving the ordinary persisted character.
func Display(c storage.Character) (storage.Character, error) {
	var profile map[string]json.RawMessage
	if err := json.Unmarshal(c.Gameplay, &profile); err != nil {
		return c, err
	}
	if profile == nil {
		return c, errors.New("duel lobby is missing the gameplay profile")
	}
	data := profile["DuelSkills"]
	if len(data) == 0 || string(data) == "null" {
		var err error
		data, err = InitialSkills(c)
		if err != nil {
			return c, err
		}
	}
	var b skillBuild
	var progress map[string]json.RawMessage
	if err := json.Unmarshal(data, &b); err != nil {
		return c, err
	}
	if err := b.validate(); err != nil {
		return c, err
	}
	if err := json.Unmarshal(profile["Progression"], &progress); err != nil {
		return c, err
	}
	if progress == nil {
		return c, errors.New("duel lobby is missing progression")
	}
	profile["Skills"] = b.Skills
	if len(b.CommandLocks) == 0 || string(b.CommandLocks) == "null" {
		delete(profile, "CommandLocks")
	} else {
		profile["CommandLocks"] = b.CommandLocks
	}
	progress["Sp"], _ = json.Marshal(b.Sp)
	progress["Tp"], _ = json.Marshal(b.Tp)
	profile["Progression"], _ = json.Marshal(progress)
	delete(profile, "DuelSkills")
	delete(profile, "SkillStyles")
	delete(profile, "SkillCommands")
	var err error
	c.Gameplay, err = json.Marshal(profile)
	return c, err
}

// Sync builds the core LocalEntryModule projection. The caller appends the
// original quest/skills modules only on their matching loading/ready events.
func Sync(ready bool, op uint16, a protocol.Actor, cera int64, native140, nativeParty bool) ([]opening.Packet, error) {
	if a.Channel != 0 && !IsChannel(uint16(a.Channel)) || a.Level < MinimumLevel {
		return nil, errors.New("invalid duel lobby actor")
	}
	var ack []byte
	switch op {
	case 120:
		ack = []byte{0, 0}
	case 143:
		ack = []byte{1, 0}
	default:
		return nil, errors.New("unsupported duel synchronization request")
	}
	m1, err := protocol.Mode1(a, ready)
	if err != nil {
		return nil, err
	}
	packets := []opening.Packet{{Class: 1, Opcode: op, Body: ack}}
	if ready {
		return append(packets, opening.Packet{Opcode: 2, Body: m1}), nil
	}
	m0, err := protocol.Mode0(a, native140)
	if err != nil {
		return nil, err
	}
	return append(packets, []opening.Packet{
		{Opcode: 376, Body: []byte{0, 0, 0}}, {Opcode: 2, Body: m0}, {Opcode: 2, Body: m1}, {Opcode: 124, Body: make([]byte, 4)},
		{Opcode: 9, Body: protocol.PartyRemove(a.ID, a.Group, a.Channel, nativeParty)},
		{Opcode: 22, Body: protocol.Position(a)}, {Opcode: 23, Body: protocol.Area(a)}, {Opcode: 53, Body: protocol.AccountWallet(cera)},
		{Opcode: 24, Body: protocol.Town(a)}, {Opcode: 41, Body: []byte{0, 0}}}...), nil
}
