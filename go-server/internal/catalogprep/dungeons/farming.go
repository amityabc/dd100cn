package dungeons

import (
	"fmt"
	"regexp"
	"slices"
	"strings"

	"s9a1/server/internal/catalogprep/pvftext"
)

var ordinaryFarming = []uint32{100000002, 100000176, 100000177, 100000178, 100000179, 100000180, 100000181}
var challengeFarming = []uint32{100000214, 100000212, 100000215, 100000199, 100000209, 100000210, 100000211}

func BuildFarming(a Archive, xp *Experience) (Layouts, error) {
	s, err := newCatalogSource(a, xp, nil, nil)
	if err != nil {
		return Layouts{}, err
	}
	return s.farming()
}

var quoted = regexp.MustCompile("`([^`]*)`")

func (s *catalogSource) farming() (Layouts, error) {
	var out Layouts
	guide, err := s.a.ReadText("etc/guidesystem/100lvfarmingguideregulartab.etc", budget)
	if err != nil {
		return out, err
	}
	guideRows := pvftext.Blocks(guide, "dungeon info")
	ids := append(slices.Clone(ordinaryFarming), challengeFarming...)
	for _, id := range ids {
		rows, err := s.farmingDungeon(id, guide, guideRows)
		if err != nil {
			return out, fmt.Errorf("farming dungeon %d: %w", id, err)
		}
		out.Definitions = append(out.Definitions, rows...)
	}
	return out, nil
}

func (s *catalogSource) farmingDungeon(id uint32, guide string, guideRows []string) ([]Layout, error) {
	gate := 184
	if id == 100000002 || id == 100000214 {
		gate = 183
	}
	world, err := s.worldText(gate)
	if err != nil {
		return nil, err
	}
	selector, err := worldSelectors(world)
	if err != nil {
		return nil, err
	}
	clear := uint16(0)
	for _, row := range selector {
		if row.Dungeon != id {
			continue
		}
		if clear != 0 || row.InProgress || row.Quest <= 0 {
			return nil, fmt.Errorf("farming selector does not have one completed quest")
		}
		clear = uint16(row.Quest)
	}
	if clear == 0 {
		return nil, fmt.Errorf("farming selector lacks completed quest")
	}
	challenge := slices.Contains(challengeFarming, id)
	guideQuest := clear
	guideID := id
	if id >= 100000178 && id <= 100000181 {
		guideQuest = 12185
	}
	if id == 100000180 || id == 100000181 {
		guideID = 100000179
	}
	p := &input{}
	if challenge {
		main, _, _ := strings.Cut(world, "[extra map]")
		locks := p.numbers(p.field(main, "lock dungeon", false))
		guideQuest = 0
		p.require(len(locks)%2 == 0, "Farming worldmap locks require dungeon/quest pairs")
		for i := 0; i+1 < len(locks); i += 2 {
			if locks[i] == int64(id) {
				p.require(guideQuest == 0 && locks[i+1] > 0 && locks[i+1] < 30000, "ambiguous farming challenge lock quest")
				guideQuest = uint16(locks[i+1])
			}
		}
	}
	dgn, err := s.dungeonText(id)
	if err != nil {
		return nil, err
	}
	options := layoutOptions{mainline: true}
	base, err := s.definition(id, dgn, 0, options)
	if err != nil {
		return nil, err
	}
	enabled := p.numbers(p.field(dgn, "difficulty level", true))
	if !p.require(len(enabled) == 5 && !slices.ContainsFunc(enabled, func(x int64) bool { return x != 0 && x != 1 }), "invalid farming difficulty flags") {
		return nil, p.err
	}
	var difficulties []byte
	var stages []uint16
	if challenge {
		flex := pvftext.Blocks(dgn, "flexible difficulty table")
		if !p.require(tagPresent(dgn, "challenge dungeon") && p.scalar(dgn, "select dungeon normal disable", 0) == 1 &&
			tagPresent(dgn, "enter limit anti evil") && slices.Equal(p.numbers(p.field(dgn, "limit party count", true)), []int64{1}) &&
			base.MinimumLevel == 100 && len(flex) == 1 && p.scalar(flex[0], "difficulty", -1) == 4,
			"invalid native farming challenge qualification") {
			return nil, p.err
		}
		tables := quoted.FindAllStringSubmatch(p.field(flex[0], "monsterapc diff table list", true), -1)
		if !p.require(len(tables) == 5, "farming challenge requires five native difficulty table slots") {
			return nil, p.err
		}
		for i, flag := range enabled {
			if flag == 1 {
				p.require(strings.TrimSpace(tables[i][1]) != "", "enabled challenge stage lacks source monster table")
				stages = append(stages, uint16(i+1))
			}
		}
		p.require(len(stages) > 0, "farming challenge has no enabled stage")
		difficulties = []byte{4}
	} else {
		for _, row := range guideRows {
			if p.scalar(row, "dungeon index", -1) != int64(guideID) {
				continue
			}
			difficulty := p.scalar(row, "difficulty", -1)
			if !p.require(difficulty >= 0 && difficulty <= 4 && enabled[difficulty] == 1 && !slices.Contains(difficulties, byte(difficulty)) &&
				p.scalar(row, "necessary questindex", -1) == int64(guideQuest) && p.scalar(row, "fatigue", -1) == int64(base.EntryFatigueCost),
				"farming guide difficulty, prerequisite or fatigue disagrees with PVF") {
				return nil, p.err
			}
			difficulties = append(difficulties, byte(difficulty))
		}
		first := byte(1)
		if id == 100000002 {
			first = 0
		} else if id >= 100000178 && id <= 100000181 {
			first = 2
		}
		p.require(slices.Contains(difficulties, first), "farming ordinary difficulty is absent from guide")
	}
	slices.Sort(difficulties)
	mazes := mazeBlocks(dgn)
	p.require(len(mazes) > 0 && len(mazes) <= 16, "farming maze count exceeds source bounds")
	if p.err != nil {
		return nil, p.err
	}
	policies := map[uint32]string{100000002: "GhentFarming/v1", 100000177: "MemoryLandFarming/v1", 100000176: "MineOfAbyssFarming/v1", 100000178: "CryptOfPainFarming/v1"}
	policy := policies[id]
	if policy == "" {
		policy = "DarkTempleFarming/v1"
	}
	if challenge {
		policy = "FarmingChallenge/v1"
	}
	cleared := []uint16{clear}
	if guideQuest != 0 && guideQuest != clear {
		cleared = append(cleared, guideQuest)
	}
	out := make([]Layout, 0, len(mazes))
	for i := range mazes {
		d := base
		if i != 0 {
			d, err = s.definition(id, dgn, i, options)
			if err != nil {
				return nil, err
			}
		}
		if len(d.meta.quests) != 0 || d.Difficulty != 0 || d.RoomFatigueCost != 0 {
			return nil, fmt.Errorf("farming maze is not a native ordinary entry-only layout")
		}
		if err := reachable(d.Dungeon); err != nil {
			return nil, err
		}
		d.meta.selectable, d.ChallengeStages = difficulties, stages
		if challenge {
			d.Difficulty = 4
		}
		d.ResourceHash = hashText(policy + "\n" + world + "\n" + guide + "\n" + d.ResourceHash)
		layout, err := s.layout(uint16(gate), d, "farming", nil, cleared, nil, nil)
		if err != nil {
			return nil, err
		}
		out = append(out, layout)
	}
	return out, nil
}
