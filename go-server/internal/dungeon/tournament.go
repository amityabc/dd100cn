package dungeon

import (
	"encoding/json"
	"errors"
	"math"
	"strings"

	"s9a1/server/internal/opening"
	"s9a1/server/internal/storage"
)

type tournamentEnemy struct {
	Code              uint32
	ResourceParameter int
	Name              string
}

type tournamentDefinition struct {
	ArenaMapID, RewardItem uint32
	BasisLevel             byte
	Enemies                []tournamentEnemy
	Bracket                []tournamentEnemy
}

const TournamentPolicy = "local100-tournament-four-rounds-basis-level-fixed-material-v1"

func freezeTournament(d Definition, seed uint32) (Definition, error) {
	r := d.Dungeon.Tournament
	if r == nil {
		return d, nil
	}
	def := d.Dungeon
	id := def.ID
	if d.Gate != 16 || id != 123 && id != 124 || def.MinimumLevel != 75 || def.Difficulty != 2 || def.BossX != 0 ||
		def.BossY != 0 || r.ArenaMapID != 95265+id-123 || id == 123 && r.RewardItem != 3329 || id == 124 && r.RewardItem != 3323 ||
		r.BasisLevel != 75 || !def.DisableDrops || !def.DisableExperience || def.UsesOrdinaryRewards || !isHash(def.ResourceHash) ||
		len(r.Enemies) != 15 {
		return Definition{}, errors.New("incomplete tournament resource")
	}
	frozen := *r
	frozen.Bracket = append([]tournamentEnemy(nil), r.Enemies...)
	seen := map[uint32]bool{}
	for _, e := range r.Enemies {
		if seen[e.Code] || e.Code == 0 || strings.TrimSpace(e.Name) == "" ||
			e.ResourceParameter != 300 && e.ResourceParameter != 600 && e.ResourceParameter != 900 {
			return Definition{}, errors.New("invalid tournament opponent source")
		}
		seen[e.Code] = true
	}
	random := newTournamentRandom(int32(seed))
	for i := len(frozen.Bracket) - 1; i > 0; i-- {
		j := random.next(i + 1)
		frozen.Bracket[i], frozen.Bracket[j] = frozen.Bracket[j], frozen.Bracket[i]
	}
	room := opening.Room{MapID: r.ArenaMapID}
	for _, i := range [...]int{0, 2, 6, 14} {
		room.Monsters = append(room.Monsters, opening.Monster{Code: frozen.Bracket[i].Code, Level: r.BasisLevel, BlocksRoom: true})
	}
	d.Dungeon.Start, d.Dungeon.Rooms = room, []opening.Room{room}
	d.Dungeon.Tournament = &frozen
	return d, nil
}

// Explicitly seeded System.Random uses the legacy subtractive generator.
type tournamentRandom struct {
	value        [56]int32
	nextA, nextB int
}

func newTournamentRandom(seed int32) tournamentRandom {
	r := tournamentRandom{nextB: 21}
	subtract := seed
	if seed == math.MinInt32 {
		subtract = math.MaxInt32
	} else if seed < 0 {
		subtract = -seed
	}
	a, b := int32(161803398)-subtract, int32(1)
	r.value[55] = a
	for i := 1; i < 55; i++ {
		k := 21 * i % 55
		r.value[k] = b
		b = a - b
		if b < 0 {
			b += math.MaxInt32
		}
		a = r.value[k]
	}
	for range 4 {
		for i := 1; i < len(r.value); i++ {
			r.value[i] -= r.value[1+(i+30)%55]
			if r.value[i] < 0 {
				r.value[i] += math.MaxInt32
			}
		}
	}
	return r
}

func (r *tournamentRandom) next(bound int) int {
	r.nextA, r.nextB = r.nextA%55+1, r.nextB%55+1
	n := r.value[r.nextA] - r.value[r.nextB]
	if n == math.MaxInt32 {
		n--
	}
	if n < 0 {
		n += math.MaxInt32
	}
	r.value[r.nextA] = n
	return int(float64(n) * (1.0 / math.MaxInt32) * float64(bound))
}

func (r *Run) TournamentReward() uint32 {
	if r == nil || r.Definition.Dungeon.Tournament == nil {
		return 0
	}
	return r.Definition.Dungeon.Tournament.RewardItem
}

func (r *Run) tournamentFrames() []opening.Packet {
	t := r.Definition.Dungeon.Tournament
	w := append(le.AppendUint32(nil, r.Definition.Dungeon.ID), 2, 1)
	for round := range 4 {
		count, step := 16>>round, 1<<round
		w = append(w, 0, byte(count))
		w = append(w, 0, 0, 0, 0, 0, 0, 0)
		for i := 1; i < count; i++ {
			w = le.AppendUint32(append(w, 0), t.Bracket[(i+1)*step-2].Code)
			w = append(w, 0, 0)
		}
	}
	base := r.actorBase()
	for i, m := range r.Room.Monsters {
		w = le.AppendUint16(append(w, 0), base+uint16(i))
		w = append(le.AppendUint32(w, m.Code), m.Level, m.Type)
	}
	room := append(le.AppendUint32([]byte{0, 0}, r.Seed), 0, 1)
	room = append(le.AppendUint32(room, t.ArenaMapID), 0, 0, 0)
	return []opening.Packet{notice(372, w), notice(373, room)}
}

func TournamentClearNotice(item uint32) []byte {
	b := []byte{0, 0, 0, 0, 0, 0, 2}
	for range 2 {
		b = append(le.AppendUint32(le.AppendUint32(b, item), 1), 0, 0)
	}
	return append(b, 0)
}

func (r *Run) TournamentDeathKeys() ([]string, bool) {
	if r.TournamentReward() == 0 || !r.Ready || !r.Cleared || r.PlayerDeath != nil || len(r.Dead) != 4 {
		return nil, false
	}
	keys := make([]string, 0, 4)
	for i := range 4 {
		d, ok := r.Dead[r.actorBase()+uint16(i)]
		if !ok || d.Result.RoomCleared != (i == 3) || d.Result.DungeonCleared != (i == 3) || d.Result.ExperienceAward != 0 ||
			d.Result.Totals != ([4]uint32{}) || len(d.Result.Drops) != 0 || d.Result.DropRuleHash != "" || d.Removal {
			return nil, false
		}
		keys = append(keys, d.Key)
	}
	return keys, true
}

func (c *Catalog) TournamentGrant(item uint32, in storage.Inventory) ([]storage.QuestGrant, error) {
	raw := c.TournamentRewards[item]
	var t dropTemplate
	if len(raw) == 0 || json.Unmarshal(raw, &t) != nil || t.TemplateID != item || t.Kind != 2 || t.StackLimit == 0 {
		return nil, storage.ErrInvalid
	}
	drop := GroundDrop{Reward: GroundReward{Template: raw, StackCount: 1}}
	return drop.Grant(in)
}
