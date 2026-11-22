// Package dungeon owns the transient state and wire projections of ordinary
// runs. Admission and irreversible gameplay effects commit through storage.
package dungeon

import (
	"encoding/hex"
	"encoding/json"
	"fmt"
	"slices"

	"s9a1/server/internal/opening"
	"s9a1/server/internal/ruby"
	"s9a1/server/internal/storage"
)

type Entrance struct {
	Town, Area, Gate uint16
}

type ResourceIssue struct {
	Dungeon uint32
	Maze    int
	Reason  string
}

type SelectorRule struct {
	Quest      int
	InProgress bool
}

type Qualification struct {
	Initialized bool
	Level       byte
	Active      map[uint16]uint16
	Cleared     map[uint16]byte
}

type Definition struct {
	Gate    uint16
	Dungeon struct {
		opening.Dungeon
		QuestConnections, ChallengeStages []uint16
		SelectableDifficulties            []byte
		DisableExperience, DisableDrops   bool
		DisableClearReward                bool
		UsesOrdinaryRewards               bool
		UsesOrdinaryMonsterDrops          bool
		TowerOfGrief                      bool
		NamedMonsterCodes                 []uint32
		BasisLevel                        byte
		Tournament                        *tournamentDefinition
		ResourceVariant                   uint32
		ExperienceMultiplier              json.Number
		EntryTicketCount                  uint32
		WisdomPasses                      []storage.WisdomPass
		Abaddon                           bool
		AbaddonMechanics                  *abaddonMechanics
		AbaddonMailRewards                []storage.AbaddonMailReward
		Warps                             []struct{ SourceX, SourceY, DestinationX, DestinationY byte }
	}
	EligibleQuests                    []uint16
	Admission                         string
	WisdomReward                      *WisdomReward
	Selector                          []SelectorRule
	RequiredClears                    []uint16
	BlockedQuests                     []uint16
	UnavailableExperienceDifficulties []byte
	Awards                            []ExperienceRules
}

type ExperienceRules struct {
	Difficulty byte
	Monsters   []struct {
		Level, Type   byte
		ByPlayerLevel []uint32
	}
}

type LuckPolicy struct {
	Hash                                                         string
	PointsPerFatigue, PointsPerPlayInterval, PlayIntervalMinutes int64
	Bands                                                        []struct{ Minimum, Maximum, PartsPer10000 int64 }
	RareDeduction                                                []struct {
		MinimumLevel, MaximumLevel int
		PartsPer10000              []int64
	}
	GapPairs []int
}

type Data struct {
	Wisdom            *WisdomCatalog
	BossEquipment     *BossEquipmentCatalog
	Ruby              *ruby.Resources
	Entrances         []Entrance
	Definitions       []Definition
	ResourceIssues    []ResourceIssue
	ExperienceHash    string
	Luck              LuckPolicy
	Drops             DropData
	ClearExperience   ClearExperienceData
	RankExperience    RankExperienceData
	Cards             CardData
	TournamentRewards map[uint32]json.RawMessage
	Oculus            map[uint16]OculusMode
	OculusCards       map[uint32]OculusCardData
	OculusStone       json.RawMessage
	WorldDrops        *WorldDropCatalog
	SpecialRewards    map[uint32]SpecialCardData
}

type ClearExperienceData struct {
	ResourceHash string
	Multiplier   json.Number
}

type RankExperienceData struct {
	ResourceHash string
	Grades       []byte
	Rates        []json.Number
}

func (r RankExperienceData) MarshalJSON() ([]byte, error) {
	// The source uses ImmutableArray<byte>, which serializes as integers.
	grades := make([]uint16, len(r.Grades))
	for i, grade := range r.Grades {
		grades[i] = uint16(grade)
	}
	return json.Marshal(struct {
		ResourceHash string
		Grades       []uint16
		Rates        []json.Number
	}{r.ResourceHash, grades, r.Rates})
}

type CardData struct {
	Hash, GuideHash string
	DifficultyRates []json.Number
	SoloPartyRate   json.Number
	Rank            struct {
		Grades  []int
		Bonuses []json.Number
	}
	Probabilities                []cardProbability
	Equipment                    []dropCandidate
	DropKinds, ItemTypes, Rarity []uint32
	DifficultyBonuses            []json.Number
	Windows                      map[byte]struct{ Down, Up byte }
	MapRates                     map[byte]uint32
	Specific                     *specificCard
	Generation                   *equipmentGeneration
	Paid                         *paidCardData
	Challenge                    []challengeCardData
}

type Catalog struct {
	Wisdom            *WisdomCatalog
	BossEquipment     *BossEquipmentCatalog
	Ruby              *ruby.Resources
	entrances         map[[2]uint16]uint16
	rows              map[uint16]map[uint32][]Definition
	Luck              LuckPolicy
	ExperienceHash    string
	Drops             DropData
	ClearExperience   ClearExperienceData
	RankExperience    RankExperienceData
	Cards             CardData
	TournamentRewards map[uint32]json.RawMessage
	Oculus            map[uint16]OculusMode
	OculusCards       map[uint32]OculusCardData
	OculusStone       json.RawMessage
	WorldDrops        *WorldDropCatalog
	SpecialRewards    map[uint32]SpecialCardData
	ResourceIssues    []ResourceIssue
}

func NewCatalog(data Data) (*Catalog, error) {
	c := &Catalog{entrances: make(map[[2]uint16]uint16), rows: make(map[uint16]map[uint32][]Definition),
		Luck: data.Luck, ExperienceHash: data.ExperienceHash, Drops: data.Drops, Ruby: data.Ruby,
		Wisdom: data.Wisdom, BossEquipment: data.BossEquipment,
		ClearExperience: data.ClearExperience, RankExperience: data.RankExperience, Cards: data.Cards,
		TournamentRewards: data.TournamentRewards,
		Oculus:            data.Oculus,
		OculusCards:       data.OculusCards,
		OculusStone:       data.OculusStone,
		WorldDrops:        data.WorldDrops, SpecialRewards: data.SpecialRewards, ResourceIssues: slices.Clone(data.ResourceIssues)}
	if err := c.Drops.prepare(); err != nil {
		return nil, err
	}
	// Older character catalogs have no dungeon section. They can still boot
	// for their supported flows until the local resource export is refreshed.
	if len(data.Entrances) == 0 && len(data.Definitions) == 0 && data.ExperienceHash == "" {
		return c, nil
	}
	if len(data.Entrances) == 0 || len(data.Definitions) == 0 || !isHash(data.ExperienceHash) ||
		!isHash(data.Luck.Hash) || data.Luck.PointsPerFatigue < 0 {
		return nil, fmt.Errorf("ordinary dungeon catalog is incomplete")
	}
	for _, d := range data.Definitions {
		row := d.Dungeon
		if d.Gate == 0 || row.ID == 0 || !isHash(row.ResourceHash) {
			return nil, fmt.Errorf("invalid dungeon %d maze %d", row.ID, row.MazeIndex)
		}
		if _, _, err := (opening.Definition{Dungeon: row.Dungeon}).Reserve(402); err != nil {
			return nil, fmt.Errorf("dungeon %d maze %d: %w", row.ID, row.MazeIndex, err)
		}
		if c.rows[d.Gate] == nil {
			c.rows[d.Gate] = make(map[uint32][]Definition)
		}
		list := c.rows[d.Gate][row.ID]
		for _, other := range list {
			if other.Dungeon.MazeIndex == row.MazeIndex && other.Admission == d.Admission &&
				(d.Admission != "ruby" && d.Admission != "wisdom" || other.Dungeon.ResourceVariant == row.ResourceVariant) &&
				(d.Admission != "advancement" || slices.Equal(other.EligibleQuests, d.EligibleQuests)) {
				return nil, fmt.Errorf("duplicate dungeon %d gate %d maze %d", row.ID, d.Gate, row.MazeIndex)
			}
		}
		c.rows[d.Gate][row.ID] = append(list, d)
	}
	for _, e := range data.Entrances {
		key := [2]uint16{e.Town, e.Area}
		if c.entrances[key] != 0 || len(c.rows[e.Gate]) == 0 {
			return nil, fmt.Errorf("invalid dungeon entrance at town %d area %d", e.Town, e.Area)
		}
		c.entrances[key] = e.Gate
	}
	return c, nil
}

func isHash(value string) bool {
	if len(value) != 64 {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil
}

func (c *Catalog) Gate(town, area uint16) uint16 {
	return c.entrances[[2]uint16{town, area}]
}

func (c *Catalog) QuestCandidates(gate uint16, dungeon uint32) []uint16 {
	var result []uint16
	for _, row := range c.rows[gate][dungeon] {
		result = append(result, row.EligibleQuests...)
		result = append(result, row.BlockedQuests...)
	}
	slices.Sort(result)
	return slices.Compact(result)
}

func (c *Catalog) IsChallenge(gate uint16, dungeon uint32) bool {
	return slices.ContainsFunc(c.rows[gate][dungeon], func(d Definition) bool {
		return d.Admission == "farming" && len(d.Dungeon.ChallengeStages) > 0
	})
}

func (c *Catalog) Available(gate uint16, level byte) []uint32 {
	return c.AvailableFor(gate, Qualification{Level: level})
}

func (c *Catalog) AvailableFor(gate uint16, q Qualification) []uint32 {
	var ids []uint32
	for id, rows := range c.rows[gate] {
		for _, row := range rows {
			ordinary := row.Admission == "" || row.Admission == "gran" || row.Admission == "training"
			if (!ordinary || len(row.Dungeon.QuestConnections) == 0) && row.allowed(q) {
				ids = append(ids, id)
				break
			}
		}
	}
	slices.Sort(ids)
	return ids
}

func (c *Catalog) MinimumLevel(gate uint16, id uint32, q Qualification) byte {
	q.Level = 255
	var minLevel byte
	for _, row := range c.rows[gate][id] {
		if row.allowed(q) && (minLevel == 0 || row.Dungeon.MinimumLevel < minLevel) {
			minLevel = row.Dungeon.MinimumLevel
		}
	}
	return minLevel
}

func (d Definition) allowed(q Qualification) bool {
	if q.Level < d.Dungeon.MinimumLevel {
		return false
	}
	if d.Admission != "" && d.Admission != "gran" && d.Admission != "training" && !q.Initialized {
		return false
	}
	for _, id := range d.RequiredClears {
		if q.Cleared[id] == 0 {
			return false
		}
	}
	if len(d.Selector) > 0 && !slices.ContainsFunc(d.Selector, func(rule SelectorRule) bool {
		if rule.Quest == -1 {
			return true
		}
		if rule.InProgress {
			_, active := q.Active[uint16(rule.Quest)]
			return active
		}
		return q.Cleared[uint16(rule.Quest)] != 0
	}) {
		return false
	}
	if len(d.Dungeon.QuestConnections) == 0 {
		return true
	}
	return slices.ContainsFunc(d.EligibleQuests, func(id uint16) bool { _, active := q.Active[id]; return active })
}

func (c *Catalog) Choose(gate uint16, q Qualification, req Entry, eligible []uint16, seed uint32) (Definition, bool) {
	if req.QuestHint > 65535 && req.QuestHint != ^uint32(0) {
		return Definition{}, false
	}
	var rows []Definition
	for _, row := range c.rows[gate][req.Dungeon] {
		if row.allowed(q) {
			rows = append(rows, row)
		}
	}
	if len(rows) == 0 {
		return Definition{}, false
	}
	if rows[0].Admission == "wisdom" {
		if req.Difficulty > 1 || req.ChallengeStage != 0 {
			return Definition{}, false
		}
		reward, err := c.Wisdom.Plan(seed, c.BossEquipment)
		if err != nil {
			return Definition{}, false
		}
		boss := uint32(109010632)
		if reward.Pool == 3 {
			boss = 109010633
		}
		var variants []Definition
		for _, row := range rows {
			for _, room := range row.Dungeon.Rooms {
				if slices.ContainsFunc(room.Monsters, func(m opening.Monster) bool { return m.HellPhase == 3 && m.Code == boss }) {
					variants = append(variants, row)
					break
				}
			}
		}
		if len(variants) == 0 {
			return Definition{}, false
		}
		d := variants[seed%uint32(len(variants))]
		d.Dungeon.Difficulty, d.WisdomReward = req.Difficulty, reward
		return d, true
	}
	if rows[0].Admission == "farming" {
		d := rows[int(seed%uint32(len(rows)))]
		if !slices.Contains(d.Dungeon.SelectableDifficulties, req.Difficulty) ||
			(len(d.Dungeon.ChallengeStages) == 0 && req.ChallengeStage != 0) ||
			(len(d.Dungeon.ChallengeStages) != 0 && !slices.Contains(d.Dungeon.ChallengeStages, req.ChallengeStage)) {
			return Definition{}, false
		}
		d.Dungeon.Difficulty, d.Dungeon.ChallengeStage = req.Difficulty, req.ChallengeStage
		return d, true
	}
	if rows[0].Admission == "abaddon" {
		d := rows[0]
		if req.ChallengeStage != 0 || !slices.Contains(d.Dungeon.SelectableDifficulties, req.Difficulty) {
			return Definition{}, false
		}
		d.Dungeon.Difficulty = req.Difficulty
		return d, true
	}
	if rows[0].Admission == "ruby" {
		if req.ChallengeStage != 0 || req.Difficulty != 2 {
			return Definition{}, false
		}
		return rows[seed%uint32(len(rows))], true
	}
	if req.ChallengeStage != 0 {
		return Definition{}, false
	}
	hint := uint16(req.QuestHint)
	if req.QuestHint == ^uint32(0) || !slices.Contains(eligible, hint) {
		hint = 0
	}
	candidates := eligible
	if hint != 0 {
		candidates = append([]uint16{hint}, eligible...)
	}
	for _, id := range candidates {
		for _, d := range rows {
			if slices.Contains(d.BlockedQuests, id) && (hint == 0 || hint == id || q.Active[id] == hint) {
				return Definition{}, false
			}
		}
		if d, ok := questMaze(rows, id); ok {
			if hint != 0 && hint != id && (d.Admission == "advancement" || d.Admission == "sky" || d.Admission == "mainline" && q.Active[id] != hint) {
				continue
			}
			return d.selectDifficulty(q.Level, hint, req.Difficulty)
		}
	}
	for _, row := range rows {
		if len(row.Dungeon.QuestConnections) == 0 {
			return row.selectDifficulty(q.Level, 0, req.Difficulty)
		}
	}
	return Definition{}, false
}

func questMaze(rows []Definition, id uint16) (Definition, bool) {
	for _, row := range rows {
		if slices.Contains(row.EligibleQuests, id) {
			return row, true
		}
	}
	return Definition{}, false
}

func (d Definition) selectDifficulty(level byte, hint uint16, difficulty byte) (Definition, bool) {
	q := d.Dungeon
	if slices.Contains(d.UnavailableExperienceDifficulties, difficulty) ||
		len(q.SelectableDifficulties) == 0 && slices.Contains(d.UnavailableExperienceDifficulties, q.Difficulty) {
		return Definition{}, false
	}
	if d.Admission == "mainline" && len(q.SelectableDifficulties) > 0 {
		if level < q.MinimumLevel || !slices.Contains(q.SelectableDifficulties, difficulty) {
			return Definition{}, false
		}
		d.Dungeon.Difficulty = difficulty
		return d, true
	}
	if d.Admission == "mainline" && !(q.ID == 233 && hint == 3778 && difficulty == 0) && difficulty != q.Difficulty {
		return Definition{}, false
	}
	switch {
	case level < q.MinimumLevel:
		return Definition{}, false
	case difficulty == q.Difficulty:
		return d, true
	case len(q.QuestConnections) == 0 && slices.Contains(q.SelectableDifficulties, difficulty):
		d.Dungeon.Difficulty = difficulty
		return d, true
	case difficulty == 0 && len(q.SelectableDifficulties) == 0 && hint != 0 && slices.Contains(q.QuestConnections, hint):
		return d, true
	default:
		return Definition{}, false
	}
}
