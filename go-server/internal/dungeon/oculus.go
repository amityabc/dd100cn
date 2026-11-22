package dungeon

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"slices"
	"strings"

	"s9a1/server/internal/opening"
	"s9a1/server/internal/protocol"
)

type OculusArea struct {
	Town, Area byte
	X, Y       uint16
}

type OculusValue struct {
	Id       uint32
	RawValue int
}

type OculusPosition struct {
	X, Y  byte
	Floor byte
}

type OculusWarp struct{ Source, Destination OculusPosition }

type OculusMode struct {
	Mode                                                uint16
	Dungeon                                             opening.Dungeon
	SourceHash                                          string
	SourceCoinLimit                                     byte
	UsesPublishedLayout                                 bool
	EventSpawnMaps                                      []uint32
	StartMaps                                           []OculusPosition
	WarpConditions                                      []OculusWarp
	ClearMapId                                          uint32
	Awards                                              []ExperienceRules
	DisableDrops, DisableClearReward, DisableExperience bool
	UsesOrdinaryRewards, UsesOrdinaryMonsterDrops       bool
	ExperienceMultiplier                                json.Number
	SelectableDifficulties                              []uint16
	Rules                                               struct {
		PlayTimeSeconds, DailyEntryLimit, WeeklyEntryLimit, WeeklyBossRewardLimit int
		NeedFatigue                                                               uint16
		MinimumMembers                                                            byte
		RecruitingArea, WaitingArea                                               OculusArea
		ShopNpcId, ShopOpenMonsterId, BossMonsterId                               uint32
		LuiseMonsterId, RoseberyRonMonsterId                                      uint32
		SoryunMonsterIds                                                          []uint32
		GrimSeekers, Imposters, Kaiju, MoveObjects                                []OculusValue
		KillCoinLimitBonuses, KillTimeoverBonuses                                 []OculusValue
	}
}

type oculusCell struct {
	Position OculusPosition
	Kind     byte
	Monster  uint32
}

type oculusLayout struct {
	mode      OculusMode
	cells     []oculusCell
	retired   map[oculusCell]string
	admission Definition
	previous  []*Run
}

const oculusLayoutPolicy = "PublishedCnOculusLayout/v2"

func oculusFloor(x, y byte) byte { return x/5 + 2*(y/3) }

func PrepareOculus(mode OculusMode, party string, a protocol.Actor, first uint16, seed uint32) (*Run, uint16, error) {
	if !isHash(mode.SourceHash) || len(party) != 36 || mode.SourceCoinLimit == 0 || mode.Rules.PlayTimeSeconds < 1 || mode.Rules.PlayTimeSeconds > 86400 {
		return nil, 0, errors.New("invalid Oculus resource or party")
	}
	d := Definition{Admission: "oculus", Awards: mode.Awards}
	d.Dungeon.Dungeon = mode.Dungeon
	d.Dungeon.Difficulty = 4
	d.Dungeon.DisableDrops, d.Dungeon.DisableClearReward, d.Dungeon.DisableExperience = mode.DisableDrops, mode.DisableClearReward, mode.DisableExperience
	d.Dungeon.UsesOrdinaryRewards, d.Dungeon.UsesOrdinaryMonsterDrops = mode.UsesOrdinaryRewards, mode.UsesOrdinaryMonsterDrops
	d.Dungeon.ExperienceMultiplier = mode.ExperienceMultiplier
	if !mode.UsesPublishedLayout {
		return nil, 0, errors.New("Oculus mode has no qualified solo layout")
	}
	d, layout, err := materializeOculus(d, mode, party)
	if err != nil {
		return nil, 0, err
	}
	r, next, err := Prepare(d, Entry{Dungeon: d.Dungeon.ID, Difficulty: 4}, a, first, seed)
	if err != nil {
		return nil, 0, err
	}
	layout.admission = d
	r.ID, r.OculusLayout, r.OculusGeneration = party, layout, 1
	return r, next, nil
}

func materializeOculus(d Definition, mode OculusMode, party string) (Definition, *oculusLayout, error) {
	rules := mode.Rules
	if (mode.Mode != 2 && mode.Mode != 6) || len(d.Dungeon.Rooms) != 47 || len(mode.StartMaps) != 4 ||
		len(rules.GrimSeekers) != 2 || len(rules.Imposters) != 2 || len(rules.Kaiju) != 2 || len(rules.SoryunMonsterIds) != 3 {
		return Definition{}, nil, errors.New("incomplete Oculus layout source")
	}
	normalized := strings.ReplaceAll(strings.ToLower(party), "-", "")
	hash := sha256.Sum256([]byte(oculusLayoutPolicy + normalized + mode.SourceHash))
	state := le.Uint32(hash[:])
	if state == 0 {
		state = 0x9E3779B9
	}
	next := func(bound int) int {
		limit := uint32(bound)
		threshold := -limit % limit
		for {
			state ^= state << 13
			state ^= state >> 17
			state ^= state << 5
			if state >= threshold {
				return int(state % limit)
			}
		}
	}
	rooms := slices.Clone(d.Dungeon.Rooms)
	slices.SortFunc(rooms, func(a, b opening.Room) int { return int(a.Y)*10 + int(a.X) - int(b.Y)*10 - int(b.X) })
	used := map[OculusPosition]bool{}
	for _, p := range mode.StartMaps {
		used[p] = true
	}
	layout := &oculusLayout{mode: mode}
	place := func(floor, kind byte, mon uint32) error {
		var indexes []int
		for i, r := range rooms {
			p := OculusPosition{r.X, r.Y, oculusFloor(r.X, r.Y)}
			if p.Floor != floor || used[p] {
				continue
			}
			if kind != 5 && (!slices.Contains(mode.EventSpawnMaps, r.MapID) || len(r.Monsters) == 0) {
				continue
			}
			if kind != 5 {
				m := r.Monsters[0]
				if m.AiScriptIndex != nil || m.NativeSpawnIndex != nil || m.Level == 0 || !m.BlocksRoom || m.Dummy {
					continue
				}
			}
			indexes = append(indexes, i)
		}
		if len(indexes) == 0 {
			return errors.New("Oculus floor lacks a prepared encounter room")
		}
		i := indexes[next(len(indexes))]
		r := &rooms[i]
		position := OculusPosition{r.X, r.Y, floor}
		used[position] = true
		layout.cells = append(layout.cells, oculusCell{position, kind, mon})
		if kind != 5 {
			r.Monsters = []opening.Monster{{Code: mon, Level: r.Monsters[0].Level, Type: 3, BlocksRoom: true}}
		}
		return nil
	}
	pair := func(family []OculusValue, kind byte, floors []byte) error {
		for i := len(floors) - 1; i > 0; i-- {
			j := next(i + 1)
			floors[i], floors[j] = floors[j], floors[i]
		}
		first := 1 - next(2)
		for i, source := range []OculusValue{family[first], family[1-first]} {
			if err := place(floors[i], kind, source.Id); err != nil {
				return err
			}
		}
		return nil
	}
	if err := pair(rules.GrimSeekers, 1, []byte{0, 1}); err != nil {
		return Definition{}, nil, err
	}
	if err := pair(rules.Imposters, 2, []byte{0, 1, 2}); err != nil {
		return Definition{}, nil, err
	}
	if err := pair(rules.Kaiju, 3, []byte{0, 1, 2}); err != nil {
		return Definition{}, nil, err
	}
	if err := place(2, 4, rules.SoryunMonsterIds[0]); err != nil {
		return Definition{}, nil, err
	}
	for i := byte(0); i < 3; i++ {
		if err := place(i, 5, 0); err != nil {
			return Definition{}, nil, err
		}
	}
	slices.SortFunc(layout.cells, func(a, b oculusCell) int {
		return int(a.Position.Y)*10 + int(a.Position.X) - int(b.Position.Y)*10 - int(b.Position.X)
	})
	raw, err := json.Marshal(layout.cells)
	if err != nil {
		return Definition{}, nil, err
	}
	sum := sha256.Sum256([]byte(oculusLayoutPolicy + "\n" + mode.SourceHash + "\n" + normalized + "\n" + string(raw)))
	d.Dungeon.ResourceHash = strings.ToUpper(hex.EncodeToString(sum[:]))
	d.Dungeon.Rooms = rooms
	for _, r := range rooms {
		if r.X == d.Dungeon.Start.X && r.Y == d.Dungeon.Start.Y {
			d.Dungeon.Start = r
			return d, layout, nil
		}
	}
	return Definition{}, nil, errors.New("Oculus start room is missing")
}

func (r *Run) OculusRooms() []byte {
	if r.OculusLayout == nil {
		return nil
	}
	b := make([]byte, 360)
	for _, item := range r.OculusLayout.cells {
		p := item.Position
		i := (int(p.Y)*10 + int(p.X)) * 6
		kind := item.Kind
		if _, ok := r.oculusDead(item); ok {
			b[i+5] = 1
			if item.Monster != r.OculusLayout.mode.Rules.ShopOpenMonsterId {
				kind = 0
			}
		}
		b[i] = kind
		le.PutUint32(b[i+1:], item.Monster)
	}
	return b
}

func (r *Run) oculusDead(c oculusCell) (string, bool) {
	if key := r.OculusLayout.retired[c]; key != "" {
		return key, true
	}
	k := cell{c.Position.X, c.Position.Y}
	room := r.rooms[k]
	if c.Monster == 0 || room == nil {
		return "", false
	}
	for i, m := range room.Monsters {
		if m.Code == c.Monster {
			if death, ok := r.Dead[r.bases[k]+uint16(i)]; ok {
				return death.Key, true
			}
		}
	}
	return "", false
}

func (r *Run) OculusNamedDeath(d Death) bool {
	if r.OculusLayout == nil {
		return false
	}
	for _, cell := range r.OculusLayout.cells {
		if cell.Position.X == r.Room.X && cell.Position.Y == r.Room.Y && cell.Monster == d.Monster.Code && cell.Monster != 0 {
			return true
		}
	}
	return false
}

func (r *Run) ResumeOculus(first uint16, a protocol.Actor, history OculusSceneHistory) (*Run, uint16, error) {
	if r.OculusLayout == nil || r.OculusEntry == nil || !r.Ready || r.PlayerDeath != nil || r.Cleared || r.OculusGeneration >= 129 {
		return nil, 0, errors.New("Oculus continuation has no parked scene")
	}
	if history.Generation != r.OculusGeneration || history.DefinitionHash != r.Definition.Dungeon.ResourceHash ||
		history.AdmissionHash != r.OculusEntry.DungeonHash || history.retired == nil {
		return nil, 0, errors.New("Oculus continuation has no validated scene history")
	}
	return r.resumeOculus(first, a, history)
}

func (r *Run) oculusStairs(src, dst cell) ([]string, bool) {
	mode := r.OculusLayout.mode
	source, target := OculusPosition{src.x, src.y, oculusFloor(src.x, src.y)}, OculusPosition{dst.x, dst.y, oculusFloor(dst.x, dst.y)}
	if source.Floor >= 3 || target.Floor != source.Floor+1 || len(mode.Rules.MoveObjects) <= int(source.Floor) || mode.Rules.MoveObjects[source.Floor].Id == 0 ||
		!slices.Contains(mode.StartMaps, target) || !slices.Contains(mode.WarpConditions, OculusWarp{source, target}) ||
		!slices.Contains(r.OculusLayout.cells, oculusCell{Position: source, Kind: 5}) {
		return nil, false
	}
	if source.Floor == 2 {
		for _, c := range r.OculusLayout.cells {
			if c.Kind == 4 {
				if key, ok := r.oculusDead(c); ok {
					return []string{key}, true
				}
			}
		}
		return nil, false
	}
	return nil, true
}
