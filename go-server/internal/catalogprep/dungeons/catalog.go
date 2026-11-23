package dungeons

import (
	"encoding/json"
	"fmt"
	"slices"

	"s9a1/server/internal/catalogprep/commerce"
	"s9a1/server/internal/catalogprep/towns"
	"s9a1/server/internal/dungeon"
	"s9a1/server/internal/opening"
	"s9a1/server/internal/questwire"
	"s9a1/server/internal/ruby"
)

// Layouts is the admission portion of the original dungeon export.
type Layouts struct {
	Entrances           []dungeon.Entrance
	Definitions         []Layout
	ResourceIssues      []dungeon.ResourceIssue
	Ruby                *RubyRules
	PreparedDefinitions []Definition `json:"-"`
}

func (l *Layouts) BindEntrances(areas []towns.Area) {
	gates := map[uint16]bool{}
	for _, d := range l.Definitions {
		gates[d.Gate] = true
	}
	l.Entrances = []dungeon.Entrance{}
	for _, a := range areas {
		if a.DungeonGate != nil && gates[*a.DungeonGate] {
			l.Entrances = append(l.Entrances, dungeon.Entrance{Town: a.Town, Area: a.Area, Gate: *a.DungeonGate})
		}
	}
}

type Layout struct {
	Gate                              uint16
	Dungeon                           Definition
	Admission                         string
	EligibleQuests, RequiredClears    []uint16
	Selector                          []dungeon.SelectorRule
	BlockedQuests                     []uint16
	Awards                            []dungeon.ExperienceRules
	UnavailableExperienceDifficulties []byte
}

type Definition struct {
	opening.Dungeon
	meta                                          nativeMeta
	UsesOrdinaryRewards, UsesOrdinaryMonsterDrops bool
	ResourceVariant, EntryTicketCount             uint32
	WisdomReward                                  any
	WisdomPasses                                  []any
	ChallengeStages                               []uint16
	Tournament                                    any
	RubyFloor                                     *RubyFloor
	RubyResources                                 *RubyRules
	Abaddon                                       bool
	AbaddonReward, AbaddonMechanics               any
	AbaddonMailRewards                            []any
	Warps                                         []DungeonWarp
}

type RubyFloor struct {
	Floor            byte
	Next, NativeNext uint32
}
type RubyRules struct {
	ruby.Resources
	TreasureIds []uint32
}
type DungeonWarp struct{ SourceX, SourceY, DestinationX, DestinationY byte }

// BuildLayouts preserves the original admission grouping and exposes the
// underlying prepared definitions for resource-scoped drop preparation.
func BuildLayouts(a Archive, xp *Experience, quests []questwire.Quest, issues map[uint16]string, wisdom *WisdomData) (Layouts, error) {
	if xp == nil || wisdom == nil || len(wisdom.Hash) != 64 || len(quests) == 0 {
		return Layouts{}, fmt.Errorf("dungeon layout preparation needs native EXP, Wisdom equipment and quest snapshots")
	}
	s, err := newCatalogSource(a, xp, quests, issues)
	if err != nil {
		return Layouts{}, err
	}
	items, err := commerce.NewItems(a)
	if err != nil {
		return Layouts{}, err
	}
	var out Layouts
	for _, build := range []func() (Layouts, error){s.ordinary, s.advancement, s.farming, s.storm,
		func() (Layouts, error) { return s.wisdomLayouts(wisdom.Hash) },
		func() (Layouts, error) { return s.abaddon(items) }, s.rubyLayouts, s.mainline} {
		part, err := build()
		if err != nil {
			return Layouts{}, err
		}
		out.Definitions = append(out.Definitions, part.Definitions...)
		if part.Ruby != nil {
			out.Ruby = part.Ruby
		}
		if part.ResourceIssues != nil {
			out.ResourceIssues = part.ResourceIssues
		}
	}
	mainline := map[[2]uint32]bool{}
	for _, row := range out.Definitions {
		d := row.Dungeon
		if row.Admission == "mainline" {
			key := [2]uint32{d.ID, uint32(d.MazeIndex)}
			if mainline[key] {
				continue
			}
			mainline[key] = true
		}
		if d.Tournament != nil {
			// The tournament route zeros these when forming the exported
			// admission row; the internal resource record retains its defaults.
			d.EntryFatigueCost, d.RoomFatigueCost = 1, 1
		}
		out.PreparedDefinitions = append(out.PreparedDefinitions, d)
	}
	return out, nil
}

func (d Definition) MarshalJSON() ([]byte, error) {
	type storyExp struct{ BonusPercents []uint32 }
	var story *storyExp
	if len(d.meta.story) > 0 {
		story = &storyExp{d.meta.story}
	}
	var stage *uint16
	if d.ChallengeStage != 0 {
		stage = &d.ChallengeStage
	}
	selectable := make([]uint16, len(d.meta.selectable))
	for i, v := range d.meta.selectable {
		selectable[i] = uint16(v)
	}
	return json.Marshal(struct {
		Id                                                                             uint32
		MinimumLevel, MazeIndex, BossX, BossY                                          byte
		Start                                                                          nativeRoom
		StoryExperience                                                                *storyExp `json:",omitempty"`
		NamedMonsterCodes                                                              []uint32  `json:",omitempty"`
		BasisLevel                                                                     byte      `json:",omitempty"`
		Rooms                                                                          []nativeRoom
		ExperienceMultiplier                                                           json.Number
		ResourceHash                                                                   string
		QuestConnections                                                               []uint16
		Difficulty                                                                     byte
		DisableDrops, DisableClearReward, DisableExperience, HellDungeon, TowerOfGrief bool
		ResourceVariant, EntryTicketCount                                              uint32
		WisdomReward                                                                   any
		WisdomPasses                                                                   []any
		UsesOrdinaryRewards, UsesOrdinaryMonsterDrops, AllowsOrdinaryMonsterDrops      bool
		EntryFatigueCost, RoomFatigueCost                                              uint16
		SelectableDifficulties                                                         []uint16
		DifficultyDropItems                                                            [][]uint32
		ChallengeStages                                                                []uint16
		ChallengeStage                                                                 *uint16 `json:",omitempty"`
		Tournament                                                                     any
		RubyFloor                                                                      *RubyFloor
		RubyResources                                                                  *RubyRules
		Abaddon                                                                        bool
		AbaddonReward, AbaddonMechanics                                                any
		AbaddonMailRewards                                                             []any
		Warps                                                                          []DungeonWarp
	}{
		Id: d.ID, MinimumLevel: d.MinimumLevel, MazeIndex: d.MazeIndex, BossX: d.BossX, BossY: d.BossY, Start: wireRoom(d.Start),
		StoryExperience: story, NamedMonsterCodes: d.meta.named, BasisLevel: d.meta.basis, Rooms: wireRooms(d.Rooms),
		ExperienceMultiplier: d.meta.multiplier, ResourceHash: d.ResourceHash, QuestConnections: list(d.meta.quests), Difficulty: d.Difficulty, DisableDrops: d.meta.disableDrops,
		DisableClearReward: d.meta.disableClear, DisableExperience: d.meta.disableExperience, HellDungeon: d.meta.hell, TowerOfGrief: d.meta.tower,
		ResourceVariant: d.ResourceVariant, EntryTicketCount: d.EntryTicketCount, WisdomReward: d.WisdomReward, WisdomPasses: list(d.WisdomPasses),
		UsesOrdinaryRewards: d.UsesOrdinaryRewards, UsesOrdinaryMonsterDrops: d.UsesOrdinaryMonsterDrops,
		AllowsOrdinaryMonsterDrops: !d.meta.disableDrops && d.UsesOrdinaryMonsterDrops && (d.UsesOrdinaryRewards || d.Difficulty != 0),
		EntryFatigueCost:           d.EntryFatigueCost, RoomFatigueCost: d.RoomFatigueCost, SelectableDifficulties: selectable,
		DifficultyDropItems: list(d.meta.difficultyItems), ChallengeStages: list(d.ChallengeStages), ChallengeStage: stage,
		Tournament: d.Tournament, RubyFloor: d.RubyFloor, RubyResources: d.RubyResources,
		Abaddon: d.Abaddon, AbaddonReward: d.AbaddonReward, AbaddonMechanics: d.AbaddonMechanics, AbaddonMailRewards: list(d.AbaddonMailRewards), Warps: list(d.Warps),
	})
}

func (s *mapSource) definition(id uint32, text string, maze int, options layoutOptions) (Definition, error) {
	d, meta, err := s.parse(id, text, maze, options)
	return Definition{Dungeon: d, meta: meta, UsesOrdinaryRewards: true, UsesOrdinaryMonsterDrops: true}, err
}

type catalogSource struct {
	*mapSource
	xp           *Experience
	quests       map[uint16]questwire.Quest
	questIssues  map[uint16]string
	worlds       map[int64]string
	worldTexts   map[int]string
	dungeonTexts map[uint32]string
	movies       map[int64]string
	cinematics   map[cinematicKey][]opening.CinematicDestroy
}

func newCatalogSource(a Archive, xp *Experience, quests []questwire.Quest, issues map[uint16]string) (*catalogSource, error) {
	s, err := newMapSource(a)
	if err != nil {
		return nil, err
	}
	out := &catalogSource{mapSource: s, xp: xp, quests: map[uint16]questwire.Quest{}, questIssues: issues,
		worldTexts: map[int]string{}, dungeonTexts: map[uint32]string{}, cinematics: map[cinematicKey][]opening.CinematicDestroy{}}
	for _, q := range quests {
		out.quests[q.Id] = q
	}
	s.aics, err = a.ReadIndex("aicharacter", budget)
	if err != nil {
		return nil, err
	}
	out.worlds, err = a.ReadIndex("worldmap", budget)
	if err != nil {
		return nil, err
	}
	out.movies, err = a.ReadIndex("cinematic", budget)
	if err != nil {
		return nil, err
	}
	return out, nil
}

func (s *catalogSource) dungeonText(id uint32) (string, error) {
	if raw, ok := s.dungeonTexts[id]; ok {
		return raw, nil
	}
	path := s.dungeons[int64(id)]
	if path == "" {
		return "", fmt.Errorf("Mainline dungeon index entry is missing")
	}
	raw, err := s.a.ReadText(path, budget)
	if err == nil {
		s.dungeonTexts[id] = raw
	}
	return raw, err
}

func (s *catalogSource) worldText(id int) (string, error) {
	if raw, ok := s.worldTexts[id]; ok {
		return raw, nil
	}
	path := s.worlds[int64(id)]
	if path == "" {
		return "", fmt.Errorf("worldmap %d has no original resource", id)
	}
	raw, err := s.a.ReadText(path, budget)
	if err == nil {
		s.worldTexts[id] = raw
	}
	return raw, err
}

func (s *catalogSource) layout(gate uint16, d Definition, admission string, eligible, cleared []uint16, selector []dungeon.SelectorRule, blocked []uint16) (Layout, error) {
	if eligible == nil {
		eligible = []uint16{}
		for _, id := range d.meta.quests {
			if slices.Contains(s.quests[id].DestinationDungeons, int(d.ID)) {
				eligible = append(eligible, id)
			}
		}
	}
	l := Layout{Gate: gate, Dungeon: d, Admission: admission, EligibleQuests: eligible, RequiredClears: cleared, Selector: selector, BlockedQuests: blocked,
		Awards: []dungeon.ExperienceRules{}, UnavailableExperienceDifficulties: []byte{}}
	var samples []opening.Monster
	for _, r := range append([]opening.Room{d.Start}, d.Rooms...) {
		add := func(room opening.Room) {
			for _, m := range room.Monsters {
				if m.Code != 0 && m.Level != 0 && m.AiScriptIndex == nil {
					samples = append(samples, m)
				}
			}
		}
		add(r)
		if r.CinematicLayer != nil {
			add(*r.CinematicLayer)
		}
	}
	diffs := []byte{d.Difficulty}
	if admission == "mainline" && len(d.meta.selectable) > 0 {
		diffs = slices.Clone(d.meta.selectable)
	} else if len(d.meta.quests) == 0 {
		diffs = append(diffs, d.meta.selectable...)
	}
	slices.Sort(diffs)
	for _, difficulty := range slices.Compact(diffs) {
		profile := ExperienceProfile{Multiplier: d.meta.multiplier, Ordinary: d.UsesOrdinaryRewards, StoryPercents: d.meta.story, Named: d.meta.named}
		if admission != "mainline" && difficulty != d.Difficulty && len(d.meta.quests) == 0 {
			profile.StoryPercents = nil
		}
		awards := []Award{}
		if !d.meta.disableExperience {
			if profile.Ordinary && len(profile.StoryPercents) > 0 && int(difficulty) >= len(profile.StoryPercents) {
				l.UnavailableExperienceDifficulties = append(l.UnavailableExperienceDifficulties, difficulty)
				continue
			}
			var err error
			awards, err = s.xp.awards(profile, difficulty, samples)
			if err != nil {
				return Layout{}, fmt.Errorf("dungeon %d maze %d difficulty %d: %w", d.ID, d.MazeIndex, difficulty, err)
			}
		}
		l.Awards = append(l.Awards, dungeon.ExperienceRules{Difficulty: difficulty, Monsters: awards})
	}
	return l, nil
}
