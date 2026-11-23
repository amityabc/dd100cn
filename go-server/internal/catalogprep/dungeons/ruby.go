package dungeons

import (
	"fmt"
	"regexp"
	"slices"

	"s9a1/server/internal/catalogprep/pvftext"
	"s9a1/server/internal/opening"
	"s9a1/server/internal/ruby"
)

const rubyParametersPath = "etc/curseofbloodyfruit/curseofbloodyfruitparameter.etc"

var rubyBossPlaceholder = regexp.MustCompile(`(?ms)^\[boss map\]\s*4\s+4\s*\[/boss map\]`)

func BuildRuby(a Archive, xp *Experience) (Layouts, error) {
	s, err := newCatalogSource(a, xp, nil, nil)
	if err != nil {
		return Layouts{}, err
	}
	return s.rubyLayouts()
}

func (s *catalogSource) rubyLayouts() (Layouts, error) {
	world, err := s.worldText(180)
	if err != nil {
		return Layouts{}, err
	}
	raw, err := s.a.ReadText(rubyParametersPath, budget)
	if err != nil {
		return Layouts{}, err
	}
	rules, err := rubyRules(raw)
	if err != nil {
		return Layouts{}, err
	}
	rows := Layouts{Definitions: make([]Layout, 0, 611), Ruby: rules}
	for key := byte(1); key <= 60; key++ {
		placements, err := s.rubyFloor(key, world, raw, rules)
		if err != nil {
			return Layouts{}, fmt.Errorf("Ruby floor %d: %w", key, err)
		}
		rows.Definitions = append(rows.Definitions, placements...)
	}
	for _, id := range rules.HiddenIds {
		text, err := s.dungeonText(id)
		if err != nil {
			return Layouts{}, err
		}
		maze := -1
		for i, b := range mazeBlocks(text) {
			if !tagPresent(b, "quest connection") {
				maze = i
				break
			}
		}
		d, err := s.definition(id, text, maze, layoutOptions{mainline: true})
		if err != nil {
			return Layouts{}, err
		}
		p := &input{}
		count := 4
		if id == 100000650 {
			count = 7
		}
		boss := false
		for _, r := range d.Rooms {
			if r.X == d.BossX && r.Y == d.BossY {
				boss = slices.ContainsFunc(r.Monsters, func(m opening.Monster) bool { return m.Type == 3 })
			}
			p.require(!slices.ContainsFunc(r.Monsters, func(m opening.Monster) bool { return m.AiScriptIndex != nil }), "Ruby hidden layout has an unexpected APC")
		}
		if !p.require(d.Difficulty == 2 && d.EntryFatigueCost == 3 && d.RoomFatigueCost == 0 && p.scalar(text, "character coin limit", -1) == 0 &&
			tagPresent(text, "curse of bloodyfruit dungeon") && len(d.Rooms) == count && boss, "Ruby hidden solo route is incomplete") || p.err != nil {
			return Layouts{}, p.err
		}
		d.RubyFloor, d.RubyResources, d.UsesOrdinaryRewards = &RubyFloor{}, rules, false
		d.meta.selectable = []byte{2}
		d.ResourceHash = hashText(rules.Hash + d.ResourceHash + text)
		if err := reachable(d.Dungeon); err != nil {
			return Layouts{}, err
		}
		l, err := s.layout(180, d, "ruby", nil, nil, nil, nil)
		if err != nil {
			return Layouts{}, err
		}
		rows.Definitions = append(rows.Definitions, l)
	}
	return rows, nil
}

func (s *catalogSource) rubyFloor(floor byte, world, parameters string, rules *RubyRules) ([]Layout, error) {
	id := ruby.FloorID(floor)
	text, err := s.dungeonText(id)
	if err != nil {
		return nil, err
	}
	selector, err := worldSelectors(world)
	if err != nil {
		return nil, err
	}
	p := &input{}
	matching := 0
	for _, r := range selector {
		if r.Dungeon == id && r.Quest == -1 && !r.InProgress {
			matching++
		}
	}
	mazes := mazeBlocks(text)
	board := p.field(text, "dungeon clear result", true)
	if !p.require(matching == 1 && len(mazes) == 2 && p.scalar(text, "minimum required level", 0) == 100 &&
		p.scalar(text, "basis level", 0) == 135 && p.scalar(text, "designate dungeon difficulty", 0) == 2 && p.scalar(text, "difficulty", 0) == 2 &&
		p.scalar(text, "minimum enter fatigue", 0) == 1 && p.scalar(text, "character coin limit", -1) == 0 &&
		tagPresent(text, "curse of bloodyfruit dungeon") && p.scalar(board, "result board", -1) == 0 && p.scalar(board, "reward card", -1) == 0,
		"Ruby floor lacks its native solo fixed-difficulty no-board entrance") || p.err != nil {
		return nil, p.err
	}
	next, rawNext := ruby.FloorID(floor+1), p.numbers(p.field(text, "next", false))
	if floor == 60 {
		next = 0
	}
	nativeNext := next
	if floor == 23 {
		nativeNext = 9707
	}
	if !p.require(floor == 60 && len(rawNext) == 0 || floor != 60 && slices.Equal(rawNext, []int64{int64(nativeNext), 0, 2}),
		"Ruby linked-floor source differs from its ordered native table") {
		return nil, p.err
	}
	out := make([]Layout, 0, 10)
	for i, maze := range mazes {
		random := p.field(maze, "random boss map", true)
		tag, count := "boss index", 1
		if floor == 58 {
			tag, count = "multiple boss index", 3
		}
		bosses := p.numbers(p.field(random, tag, true))
		pos := p.numbers(p.field(random, "appear map", true))
		if !p.require(len(bosses) == count && len(pos) == 10, "Ruby random boss source is incomplete") {
			return nil, p.err
		}
		for _, code := range bosses {
			p.require(code > 0 && s.monsters[code] != "", "Ruby random boss source is incomplete")
		}
		positions := map[[2]byte]bool{}
		for v := 0; v < 5; v++ {
			x, y := p.width(pos[v*2]), p.width(pos[v*2+1])
			at := [2]byte{x, y}
			p.require(!positions[at], "Ruby random boss source repeats its coordinates")
			positions[at] = true
			prepared := rubyBossPlaceholder.ReplaceAllString(text, fmt.Sprintf("[boss map]\n%d %d\n[/boss map]", x, y))
			d, err := s.definition(id, prepared, i, layoutOptions{mainline: true})
			if err != nil {
				return nil, err
			}
			j := slices.IndexFunc(d.Rooms, func(r opening.Room) bool { return r.X == x && r.Y == y })
			if !p.require(j >= 0 && len(d.Rooms) == 10 && d.EntryFatigueCost == 2 && d.RoomFatigueCost == 0 && len(d.meta.quests) == 0,
				"Ruby floor lacks its event boss room or entry-only fatigue") {
				return nil, p.err
			}
			r := &d.Rooms[j]
			mapText, err := s.readMap(r.MapID, false)
			if err != nil {
				return nil, err
			}
			events := p.numbers(p.field(mapText, "event monster position", true))
			p.require(len(events) >= 3*len(bosses) && len(events)%3 == 0 && len(events) <= 192, "Ruby floor lacks its event boss positions")
			for _, q := range d.Rooms {
				p.require(!slices.ContainsFunc(q.Monsters, func(m opening.Monster) bool { return m.Type == 3 || m.AiScriptIndex != nil }),
					"Ruby floor has a static boss or APC")
			}
			if p.err != nil {
				return nil, p.err
			}
			for i, code := range bosses {
				spawn := uint32(10000 + i)
				r.Monsters = append(r.Monsters, opening.Monster{Code: uint32(code), Level: 135, Type: 3, BlocksRoom: true, NativeSpawnIndex: &spawn})
			}
			if r.X == d.Start.X && r.Y == d.Start.Y {
				d.Start = *r
			}
			d.ResourceHash = hashText(ruby.Policy + "\n" + world + "\n" + parameters + "\n" + text + "\n" + d.ResourceHash)
			d.RubyFloor, d.RubyResources = &RubyFloor{floor, next, nativeNext}, rules
			d.ResourceVariant, d.UsesOrdinaryRewards = uint32(i*5+v+1), false
			d.meta.selectable = []byte{2}
			if err := reachable(d.Dungeon); err != nil {
				return nil, err
			}
			layout, err := s.layout(180, d, "ruby", nil, nil, nil, nil)
			if err != nil {
				return nil, err
			}
			out = append(out, layout)
		}
	}
	return out, nil
}

func rubyRules(text string) (*RubyRules, error) {
	p := &input{}
	read := func(tag string) []int64 { return p.numbers(p.field(text, tag, true)) }
	floors := read("floor dungeon index")
	r := &RubyRules{Resources: ruby.Resources{Hash: hashText(text)}}
	if !p.require(len(floors) == 120 && p.scalar(text, "worldmap index", 0) == 180 && p.scalar(text, "enterable level", 0) == 100 &&
		p.scalar(text, "max floor count", 0) == 60, "Ruby ordered floor parameters changed") {
		return nil, p.err
	}
	for i := 0; i < 60; i++ {
		p.require(floors[2*i] == int64(i+1) && floors[2*i+1] == int64(ruby.FloorID(byte(i+1))), "Ruby ordered floor parameters changed")
	}
	for _, id := range read("boss reward item") {
		r.BoxIds = append(r.BoxIds, p.id(id))
	}
	if !p.require(slices.Equal(r.BoxIds, []uint32{10312271, 10312272, 10312273, 10312274, 10319996}), "Ruby reward item source changed") {
		return nil, p.err
	}
	weights := func(values []int64, ids []uint32) ([]ruby.Weight, int64) {
		out := make([]ruby.Weight, len(values))
		limit := int64(0)
		for i, w := range values {
			p.require(w >= 0 && w <= 100, "invalid Ruby reward weight")
			out[i] = ruby.Weight{Item: ids[i], Weight: uint64(w)}
			limit += w
		}
		return out, limit
	}
	prizes, rates, hidden := read("boss reward"), read("hidden dungeon rate"), read("hidden dungeon info")
	if !p.require(len(prizes) == 72 && len(rates) == 180 && len(hidden) == 12 && hidden[11] == 0, "Ruby checkpoint or hidden route source changed") {
		return nil, p.err
	}
	for i := 0; i < 12; i++ {
		row := prizes[i*6 : i*6+6]
		odds, total := weights(row[1:], r.BoxIds)
		p.require(row[0] == int64(5*(i+1)) && total == 100, "invalid Ruby checkpoint reward")
		r.Prizes = append(r.Prizes, struct {
			Floor   int
			Weights []ruby.Weight
		}{int(row[0]), odds})
	}
	for i := 0; i < 60; i++ {
		row := rates[i*3 : i*3+3]
		p.require(row[0] == int64(i+1) && row[1] >= 0 && row[1] <= 100000 && row[2] >= 0, "invalid Ruby hidden floor chance")
		r.HiddenRates = append(r.HiddenRates, struct{ Floor, Rate, Reserved int }{int(row[0]), int(row[1]), int(row[2])})
	}
	for _, id := range hidden[:11] {
		p.require(ruby.Hidden(uint32(id)) && !slices.Contains(r.HiddenIds, uint32(id)), "invalid or duplicate Ruby hidden map")
		r.HiddenIds = append(r.HiddenIds, uint32(id))
	}
	jumps := read("jumping rate")
	jumpRate := map[int64]int64{}
	for at := 0; at+1 < len(jumps); at += 2 {
		floor, rate := jumps[at], jumps[at+1]
		_, exists := jumpRate[floor]
		p.require(!exists && rate >= 0 && rate <= 100, "invalid Ruby jump rate")
		jumpRate[floor] = rate
	}
	details := pvftext.Blocks(p.field(text, "jumping floor rate", true), "detail")
	if !p.require(len(jumps)%2 == 0 && len(details) == 3, "Ruby jump parameters changed") {
		return nil, p.err
	}
	for i, body := range details {
		row := p.numbers(body)
		floor := int64(20 * (i + 1))
		rate, exists := jumpRate[floor]
		if !p.require(len(row) > 0 && row[0] == floor && exists, "Ruby jump details changed") {
			return nil, p.err
		}
		var distance []ruby.Weight
		for at := 1; at < len(row); {
			n := int(row[at])
			at++
			if !p.require(n >= 2 && n <= 4 && len(row)-at >= n-1, "Ruby jump weights are incomplete") {
				return nil, p.err
			}
			var total int64
			distance, total = weights(row[at:at+n-1], []uint32{2, 3, 4})
			p.require(total == 100, "Ruby jump odds are incomplete")
			at += n - 1
		}
		r.Jumps = append(r.Jumps, struct {
			Floor, Rate int
			Distances   []ruby.Weight
		}{int(floor), int(rate), distance})
	}
	for kind, group := range pvftext.Blocks(p.field(text, "hidden reward", true), "reward group") {
		for _, info := range pvftext.Blocks(group, "reward info") {
			bounds := p.numbers(p.field(info, "floor bound", true))
			table := p.numbers(p.field(info, "reward table", true))
			if !p.require(len(bounds) == 2 && (len(table) == 5 || len(table) == 10), "Ruby hidden reward source is incomplete") {
				return nil, p.err
			}
			rolls := [][]ruby.Weight{}
			for i := 0; i < len(table); i += 5 {
				odds, total := weights(table[i:i+5], r.BoxIds)
				p.require(total <= 100, "invalid Ruby hidden reward odds")
				rolls = append(rolls, odds)
			}
			r.HiddenPrizes = append(r.HiddenPrizes, struct {
				Kind, Minimum, Maximum int
				Rolls                  [][]ruby.Weight
			}{kind, int(bounds[0]), int(bounds[1]), rolls})
		}
	}
	r.UpgradedRate = int(p.scalar(text, "hidden upgraded named rate", -1))
	r.BossRate = int(p.scalar(text, "hidden boss rate", -1))
	r.NamedMinimum = int(p.scalar(text, "hidden minimum floor named", -1))
	r.BossMinimum = int(p.scalar(text, "hidden minimum floor boss", -1))
	r.Cooldown = int(p.scalar(text, "hidden prevent continuously appear", -1))
	p.require(r.UpgradedRate >= 0 && r.UpgradedRate <= 10000 && r.BossRate >= 0 && r.BossRate <= 10000 && r.Cooldown >= 0 && r.Cooldown <= 60 &&
		r.NamedMinimum >= 1 && r.NamedMinimum <= 60 && r.BossMinimum >= r.NamedMinimum && r.BossMinimum <= 60 &&
		p.scalar(text, "hidden minimum floor upgraded named", -1) == int64(r.NamedMinimum), "Ruby hidden qualification parameters are unsupported")
	r.Compound = p.rubyCompound(read("reward compound rate"), read("compound add info"), read("compound probability info"))
	r.TreasureIds = slices.Clone(r.BoxIds)
	for _, tier := range r.Compound.Tiers {
		if !slices.Contains(r.BoxIds, tier.Item) {
			r.TreasureIds = append(r.TreasureIds, tier.Item)
		}
	}
	return r, p.err
}

func (p *input) rubyCompound(tiers, limits, display []int64) *ruby.CompoundRules {
	o := &ruby.CompoundRules{}
	if !p.require(len(tiers) == 12 && len(limits) == 9 && len(display) == 10 && display[0] == 0 && display[9] == 100000,
		"Ruby compound tiers or limits changed") {
		return o
	}
	ids := []uint32{10312271, 10312272, 10312273, 10319994}
	for i := 0; i < 4; i++ {
		t := tiers[i*3 : i*3+3]
		item, points, threshold := p.id(t[0]), int(t[1]), int(t[2])
		p.require(item == ids[i] && threshold > 0 && 1500%threshold == 0 && (i != 0 || threshold == 1) &&
			(i == 0 || threshold >= o.Tiers[i-1].Threshold) && (i < 3 && points > 0 && points <= 1500 || i == 3 && points == -1),
			"Ruby compound tier is invalid")
		o.Tiers = append(o.Tiers, ruby.Tier{Item: item, Points: points, Threshold: threshold})
	}
	for i := 0; i < 3; i++ {
		l := limits[i*3 : i*3+3]
		p.require(l[0] >= 2 && l[1] <= 5 && l[0] <= l[1] && l[2] >= 1 && l[2] <= 2, "Ruby compound limit is invalid")
		o.Limits = append(o.Limits, ruby.Limit{Minimum: int(l[0]), Maximum: int(l[1]), MaximumInputTier: int(l[2])})
	}
	for i := 0; i < 10; i += 2 {
		p.require(display[i] <= display[i+1] && (i == 0 || display[i] == display[i-1]+1), "Ruby compound display ranges are invalid")
		o.DisplayRanges = append(o.DisplayRanges, int(display[i]), int(display[i+1]))
	}
	return o
}
