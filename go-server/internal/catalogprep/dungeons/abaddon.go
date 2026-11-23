package dungeons

import (
	"encoding/json"
	"fmt"
	"regexp"
	"slices"
	"strings"

	"s9a1/server/internal/catalogprep/commerce"
	"s9a1/server/internal/opening"
	"s9a1/server/internal/storage"
)

const (
	abaddonParameters   = "etc/abaddonimprisoned/abaddonimprisonedinfo.etc"
	abaddonRewardSource = "etc/itemdropinfo_clearreward.etc"
)

var abaddonIDs = []uint32{100000157, 100000158, 100000159, 100000160, 100000161}

func BuildAbaddon(a Archive, xp *Experience) (Layouts, error) {
	s, err := newCatalogSource(a, xp, nil, nil)
	if err != nil {
		return Layouts{}, err
	}
	items, err := commerce.NewItems(a)
	if err != nil {
		return Layouts{}, err
	}
	return s.abaddon(items)
}

func (s *catalogSource) abaddon(items *commerce.Items) (Layouts, error) {
	var out Layouts
	world, err := s.worldText(100)
	if err != nil {
		return out, err
	}
	parameter, err := s.a.ReadText(abaddonParameters, budget)
	if err != nil {
		return out, err
	}
	source, err := s.a.ReadText(abaddonRewardSource, budget)
	if err != nil {
		return out, err
	}
	p := &input{}
	if !p.require(slices.Equal(p.numbers(p.field(parameter, "dungeon group", true)),
		[]int64{100000157, 0, 100000158, 0, 100000159, 1, 100000160, 1, 100000161, 1}) &&
		p.scalar(parameter, "revive time", -1) == 3500 && p.scalar(parameter, "limit time max", -1) == 60000, "Abaddon lifecycle parameters changed") {
		return out, p.err
	}
	selectors, err := worldSelectors(world)
	if err != nil {
		return out, err
	}
	templates := map[uint32]*commerce.ItemTemplate{}
	for _, id := range []uint32{10313060, 490014289} {
		t, err := items.Prepared(id)
		if err != nil || t == nil {
			return out, fmt.Errorf("Abaddon template %d: %v", id, err)
		}
		templates[id] = t
	}
	for i, id := range abaddonIDs {
		d, err := s.abaddonDungeon(id, i < 2, selectors, world, parameter, source, templates)
		if err != nil {
			return out, fmt.Errorf("Abaddon dungeon %d: %w", id, err)
		}
		out.Definitions = append(out.Definitions, d)
	}
	return out, nil
}

func (s *catalogSource) abaddonDungeon(id uint32, first bool, selectors []selectorRow, world, parameter, source string, templates map[uint32]*commerce.ItemTemplate) (Layout, error) {
	p := &input{}
	matches := 0
	for _, row := range selectors {
		if row.Dungeon == id {
			matches++
			p.require(row.Quest == 6071 && !row.InProgress, "Abaddon selector lost its episode")
		}
	}
	if !p.require(matches == 1, "Abaddon selector is missing or ambiguous") || p.err != nil {
		return Layout{}, p.err
	}
	text, err := s.dungeonText(id)
	if err != nil {
		return Layout{}, err
	}
	d, err := s.definition(id, text, 0, layoutOptions{mainline: true})
	if err != nil {
		return Layout{}, err
	}
	minimum, count := byte(96), 14
	if id == 100000161 {
		count = 9
	}
	if first {
		minimum, count = 95, 11
	}
	board := p.field(text, "dungeon clear result", true)
	boss := d.Rooms[slices.IndexFunc(d.Rooms, func(r opening.Room) bool { return r.X == d.BossX && r.Y == d.BossY })]
	p.require(d.MinimumLevel == minimum && p.scalar(text, "limit party count", -1) == 1 && tagPresent(text, "abaddon for the imprisoned") &&
		d.meta.disableDrops && d.meta.disableClear && tagPresent(text, "disable exp") && p.scalar(board, "reward card", -1) == 0 &&
		p.scalar(board, "result board", -1) == 1 && (first && tagPresent(text, "no fatigue") || !first && p.scalar(text, "use fatigue only first enter", -1) == 10) &&
		len(d.Rooms) == count && len(d.meta.quests) == 0 && slices.ContainsFunc(boss.Monsters, func(m opening.Monster) bool { return m.Type == 3 }),
		"Abaddon prison lacks its complete solo no-drop resource")
	for _, room := range d.Rooms {
		p.require(!slices.ContainsFunc(room.Monsters, func(m opening.Monster) bool { return m.AiScriptIndex != nil }), "Abaddon cannot project APC")
	}
	if id == 100000160 {
		warp := p.field(text, "warp map condition", true)
		p.require(slices.Equal(p.numbers(p.field(warp, "source grid pos", true)), []int64{2, 0}) &&
			slices.Equal(p.numbers(p.field(warp, "dest grid pos", true)), []int64{10, 1}), "Abaddon fourth prison warp changed")
		d.Warps = []DungeonWarp{{2, 0, 10, 1}}
	}
	d.meta.selectable = []byte{0}
	switch id {
	case 100000158, 100000160:
		d.meta.selectable = []byte{0, 1}
	case 100000161:
		d.meta.selectable = []byte{0, 2}
	}
	if p.err != nil {
		return Layout{}, p.err
	}
	d.Abaddon = true
	d.UsesOrdinaryRewards, d.UsesOrdinaryMonsterDrops = false, false
	d.meta.disableExperience = true
	d.EntryFatigueCost, d.RoomFatigueCost = 10, 0
	if first {
		d.EntryFatigueCost = 0
	}
	fixedHash := ""
	if !first {
		table := fmt.Sprintf("abaddon_special_%d", id-100000146)
		fixed, tmpl, err := abaddonReward(table, source, templates)
		if err != nil {
			return Layout{}, err
		}
		fixedHash = tmpl.ResourceHash
		d.AbaddonReward = fixed
	}
	rows, hashes, err := abaddonMail(id, first, parameter, source, templates)
	if err != nil {
		return Layout{}, err
	}
	for _, r := range rows {
		d.AbaddonMailRewards = append(d.AbaddonMailRewards, r)
	}
	d.AbaddonMechanics, err = abaddonMechanics(parameter, text, d)
	if err != nil {
		return Layout{}, err
	}
	d.ResourceHash = hashText("LocalAbaddonAdmission/v1" + world + parameter + d.ResourceHash + source + fixedHash + strings.Join(hashes, "|"))
	if err := reachable(d.Dungeon); err != nil {
		return Layout{}, err
	}
	return s.layout(100, d, "abaddon", nil, []uint16{6071}, nil, nil)
}

type abaddonHP struct {
	Monster      uint32
	Percent      byte
	Milliseconds uint32
}
type abaddonSymbol struct{ X, Y, Bit byte }
type abaddonTiming struct {
	RoomTimes                          map[uint32]uint32
	HpTimes                            []abaddonHP
	KillTimes                          map[uint32]uint32
	Symbols                            []abaddonSymbol
	Maximum, TimeoutGrace, ReturnDelay uint32
}

var abaddonInteraction = regexp.MustCompile(`(?ms)^\[interaction\]\s*([012])\s*(.*?)^\[/interaction\]`)

func abaddonMechanics(parameter, text string, d Definition) (abaddonTiming, error) {
	p := &input{}
	read := func(tag string) []int64 { return p.numbers(p.field(parameter, tag, true)) }
	limits, kills, hps := read("limit time"), read("monster kill time"), read("monster hp percent time")
	ids := make([]int64, len(abaddonIDs))
	for i, id := range abaddonIDs {
		ids[i] = int64(id)
	}
	r := abaddonTiming{RoomTimes: map[uint32]uint32{}, HpTimes: []abaddonHP{}, KillTimes: map[uint32]uint32{}, Symbols: []abaddonSymbol{},
		Maximum: 60000, TimeoutGrace: 7000, ReturnDelay: 3000}
	if !p.require(len(limits)%2 == 0 && len(kills)%3 == 0 && len(hps)%4 == 0 &&
		p.scalar(parameter, "server timeout check delay", -1) == 7000 && p.scalar(parameter, "return to village time on fail", -1) == 3 &&
		slices.Equal(read("account best time dungeon"), ids), "Abaddon timing resource is incomplete") {
		return r, p.err
	}
	for i := 0; i < len(limits); i += 2 {
		id, ms := p.id(limits[i]), limits[i+1]
		p.require(ms >= 1 && ms <= 60000, "Abaddon room limit exceeds native range")
		if slices.ContainsFunc(d.Rooms, func(q opening.Room) bool { return q.MapID == id }) {
			p.require(r.RoomTimes[id] == 0, "Abaddon room time duplicated")
			r.RoomTimes[id] = uint32(ms)
		}
	}
	for i := 0; i < len(kills); i += 3 {
		if kills[i] != int64(d.ID) {
			continue
		}
		id, ms := p.id(kills[i+1]), kills[i+2]
		p.require(r.KillTimes[id] == 0 && ms >= 1 && ms <= 60000, "Abaddon monster timer invalid or duplicated")
		r.KillTimes[id] = uint32(ms)
	}
	for i := 0; i < len(hps); i += 4 {
		if hps[i] != int64(d.ID) {
			continue
		}
		id, percent, ms := p.id(hps[i+1]), hps[i+2], hps[i+3]
		p.require(percent >= 1 && percent <= 100 && ms >= 1 && ms <= 60000 &&
			!slices.ContainsFunc(r.HpTimes, func(q abaddonHP) bool { return q.Monster == id }), "invalid Abaddon HP timer")
		r.HpTimes = append(r.HpTimes, abaddonHP{id, byte(percent), uint32(ms)})
	}
	if !tagPresent(text, "dungeon interaction system") {
		return r, p.err
	}
	interaction := p.field(text, "dungeon interaction system", true)
	rows := abaddonInteraction.FindAllStringSubmatch(interaction, -1)
	if !p.require(len(rows) == 3, "Abaddon must retain all three native interactions") {
		return r, p.err
	}
	icons := p.field(text, "maze minimap ani icon", true)
	seen := map[int]bool{}
	for _, row := range rows {
		i := int(p.number(row[1], 32))
		name := []string{"blue", "yellow", "green"}[i]
		body := row[2]
		p.require(!seen[i] && normalizeScript(p.field(body, "success condition", true)) == "`dungeon symbol` `"+name+" condition` 1", "Abaddon symbol interaction changed")
		seen[i] = true
		stats := [][]string{{"cast speed", "move speed"}, {"physical attack", "magical attack"}, {"physical defense", "magical defense"}}[i]
		for _, stat := range stats {
			appendage := p.field(body, "add appendage", true)
			p.require(appendageBonus(appendage, stat, 20), "Abaddon 20-percent appendage changed")
		}
		iconPattern := regexp.MustCompile("\\d+\\s+(\\d+)\\s+(\\d+)\\s+`[^`]*\\/" + strings.ToUpper(name) + "_named\\.ani`\\s+1")
		which := iconPattern.FindAllStringSubmatch(icons, -1)
		if !p.require(len(which) == 1, "Abaddon interaction lacks color-matched icon") {
			return r, p.err
		}
		x, y := p.width(p.number(which[0][1], 64)), p.width(p.number(which[0][2], 64))
		p.require(slices.ContainsFunc(d.Rooms, func(q opening.Room) bool { return q.X == x && q.Y == y }), "Abaddon symbol room outside frozen maze")
		r.Symbols = append(r.Symbols, abaddonSymbol{x, y, 1 << i})
	}
	complete := p.field(p.field(interaction, "complete interactions", true), "add appendage", true)
	for _, stat := range []string{"physical attack", "magical attack", "physical defense", "magical defense", "cast speed", "move speed"} {
		p.require(appendageBonus(complete, stat, 30), "Abaddon 30-percent appendage changed")
	}
	return r, p.err
}

func appendageBonus(text, stat string, bonus int) bool {
	return regexp.MustCompile(fmt.Sprintf(`(?m)^\[%s\]\s*\[%%\]\s*%d(?:\s|$)`, regexp.QuoteMeta(stat), bonus)).MatchString(text)
}

var (
	abaddonMailRow   = regexp.MustCompile("(\\d+)\\s+(\\d+)\\s+`([^`]+)`\\s+`([^`]+)`\\s+`([^`]+)`\\s*")
	abaddonRewardRow = regexp.MustCompile("^\\s*`card 1`\\s+(\\d+)\\s+10000\\s+(\\d+)\\s*$")
)

func abaddonReward(table, source string, templates map[uint32]*commerce.ItemTemplate) (storage.AbaddonFixedReward, *commerce.ItemTemplate, error) {
	exp := regexp.MustCompile("(?ms)^\\[table\\]\\s*`" + regexp.QuoteMeta(table) + "`\\s+(.*?)^\\[/table\\]")
	blocks := exp.FindAllStringSubmatch(source, -1)
	if len(blocks) != 1 {
		return storage.AbaddonFixedReward{}, nil, fmt.Errorf("Abaddon fixed table %s absent or ambiguous", table)
	}
	r := abaddonRewardRow.FindStringSubmatch(blocks[0][1])
	if r == nil {
		return storage.AbaddonFixedReward{}, nil, fmt.Errorf("Abaddon %s is not an unconditional fixed reward", table)
	}
	id, count := uint32(uintToken(r[1], 32)), uint32(uintToken(r[2], 32))
	t := templates[id]
	if t == nil || t.Kind != 2 || count == 0 || count > t.StackLimit {
		return storage.AbaddonFixedReward{}, nil, fmt.Errorf("Abaddon fixed %s attachment outside its native stack core", table)
	}
	data, err := json.Marshal(t)
	return storage.AbaddonFixedReward{SourceHash: hashText(source), Template: data, Count: count}, t, err
}

func abaddonMail(id uint32, first bool, parameter, source string, templates map[uint32]*commerce.ItemTemplate) ([]storage.AbaddonMailReward, []string, error) {
	p := &input{}
	body := p.field(parameter, "reward", true)
	prison := regexp.MustCompile(fmt.Sprintf("(?ms)^\\[dungeon\\]\\s*%d\\s+(.*?)^\\[/dungeon\\]", id)).FindAllStringSubmatch(body, -1)
	if !p.require(len(prison) == 1, "Abaddon mail resource missing or duplicated") {
		return nil, nil, p.err
	}
	text := prison[0][1]
	rows := abaddonMailRow.FindAllStringSubmatchIndex(text, -1)
	p.completeRows(text, rows, "Abaddon prison mail rows are incomplete")
	if !p.require(len(rows) > 0, "Abaddon prison mail is empty") || p.err != nil {
		return nil, nil, p.err
	}
	var outputs []storage.AbaddonMailReward
	var hashes []string
	seen := map[byte]bool{}
	for _, pos := range rows {
		r := matchFields(text, pos)
		from, to := p.number(r[1], 32), p.number(r[2], 32)
		if !p.require(to >= from && to <= 10 && (first && from >= 1 || !first && from == 0 && to == 0), "invalid Abaddon mail floor range") {
			return nil, nil, p.err
		}
		table := r[3]
		if table == "abaddon_equip_4" || table == "abaddon_equip_6" {
			continue
		}
		reward, t, err := abaddonReward(table, source, templates)
		if err != nil {
			return nil, nil, err
		}
		p.require(from == 0 && t.TemplateId == 10313060 || from > 0 && t.TemplateId == 490014289, "Abaddon mail item outside its source identity")
		for floor := byte(from); floor <= byte(to); floor++ {
			p.require(!seen[floor], "Abaddon mail floors overlap")
			seen[floor] = true
			outputs = append(outputs, storage.AbaddonMailReward{Floor: floor, Table: table, Sender: r[4], Title: r[5], Reward: reward})
			hashes = append(hashes, t.ResourceHash)
		}
	}
	return outputs, hashes, p.err
}
