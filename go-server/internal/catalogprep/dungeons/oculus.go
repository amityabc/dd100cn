package dungeons

import (
	"encoding/json"
	"fmt"
	"regexp"
	"slices"
	"strings"

	"s9a1/server/internal/catalogprep/commerce"
	"s9a1/server/internal/catalogprep/pvftext"
	"s9a1/server/internal/dungeon"
	"s9a1/server/internal/opening"
	"s9a1/server/internal/savejson"
)

const oculusRulesPath = "etc/semiraid/theoculus/theoculus.etc"

var oculusSources = []struct {
	Mode uint16
	Id   uint32
	Plan string
}{
	{0, 100000007, "normal"}, {2, 400000389, "guide"}, {6, 100002669, "single"},
}

type OculusData struct {
	Modes       map[uint16]OculusMode
	Cards       map[uint32]OculusCards
	Stone       json.RawMessage
	Definitions []Definition
	SourceJSON  json.RawMessage `json:"-"`
}

type OculusMode struct {
	Mode                                                                                               uint16
	Dungeon                                                                                            Definition
	Rules                                                                                              OculusRules
	SharedRules                                                                                        oculusSharedRules
	UsesPublishedLayout                                                                                bool
	EventSpawnMaps                                                                                     []uint32
	DisableDrops, DisableClearReward, DisableExperience, UsesOrdinaryRewards, UsesOrdinaryMonsterDrops bool
	ExperienceMultiplier                                                                               json.Number
	SelectableDifficulties                                                                             []uint16
	StartMaps                                                                                          []dungeon.OculusPosition
	WarpConditions                                                                                     []dungeon.OculusWarp
	ClearMapId                                                                                         uint32
	ClearMapRawValue                                                                                   int
	SpawnPlanKind                                                                                      string
	SpawnPlans                                                                                         []oculusSpawnPlan
	SourceDungeonEntryFatigue                                                                          uint16
	SourceCoinLimit                                                                                    int
	SourcePathgateObjectsRaw                                                                           []int
	SourceHash                                                                                         string
	Awards                                                                                             []dungeon.ExperienceRules
}

type OculusRules struct {
	PlayTimeSeconds                                                                       int
	NeedFatigue                                                                           uint16
	DailyEntryLimit, WeeklyEntryLimit, WeeklyBossRewardLimit                              int
	MinimumMembers                                                                        byte
	RecruitingArea, WaitingArea                                                           dungeon.OculusArea
	ShopNpcId, ShopOpenMonsterId, BossMonsterId, LuiseMonsterId, RoseberyRonMonsterId     uint32
	SoryunMonsterIds                                                                      []uint32
	GrimSeekers, Imposters, Kaiju, MoveObjects, KillCoinLimitBonuses, KillTimeoverBonuses []dungeon.OculusValue
	NayuPassiveObjectId                                                                   uint32
	QuestClearCondition                                                                   int
}

type oculusSharedRules struct {
	ImpostersBuffRaw, AttackBonusDamageRateRaw, ExtraAttackBonusDamageRateRaw []int
	DisconnectWaitTime                                                        int
	DisconnectRetryItemId                                                     uint32
	MovieTime, EnterTime                                                      int
	EffectPassiveObjectId                                                     uint32
	EffectSettingRaw                                                          []int
	RepairMachineIndex                                                        uint32
	WarningPrayerRates                                                        []int
	SourceHash                                                                string
}

type oculusSpawnPlan struct {
	RawColumns []int
	SourceHash string
}

// oculusSourceRecord retains the property order of the C# record hashed into
// the world-drop qualification. Export-only fields do not belong to that hash.
type oculusSourceRecord struct {
	Mode                      uint16
	Dungeon                   Definition
	Rules                     OculusRules
	SharedRules               oculusSharedRules
	UsesPublishedLayout       bool
	EventSpawnMaps            []uint32
	StartMaps                 []dungeon.OculusPosition
	WarpConditions            []dungeon.OculusWarp
	ClearMapId                uint32
	ClearMapRawValue          int
	SpawnPlanKind             string
	SpawnPlans                []oculusSpawnPlan
	SourceDungeonEntryFatigue uint16
	SourceCoinLimit           int
	SourcePathgateObjectsRaw  []int
	SourceHash                string
}

func BuildOculus(a Archive, xp *Experience) (OculusData, error) {
	s, err := newCatalogSource(a, xp, nil, nil)
	if err != nil {
		return OculusData{}, err
	}
	items, err := commerce.NewItems(a)
	if err != nil {
		return OculusData{}, err
	}
	return s.oculus(items)
}

func (s *catalogSource) oculus(items *commerce.Items) (OculusData, error) {
	var out OculusData
	text, err := s.a.ReadText(oculusRulesPath, budget)
	if err != nil {
		return out, err
	}
	p := &input{}
	shared := oculusSharedRules{
		ImpostersBuffRaw: p.oculusValues(text, "imposters buff", 5), AttackBonusDamageRateRaw: p.oculusValues(text, "attack bonus damage rate", 2),
		ExtraAttackBonusDamageRateRaw: p.oculusValues(text, "extra attack bonus damage rate", 2),
		DisconnectWaitTime:            p.positive(p.oculusScalar(text, "disconnect wait time")),
		DisconnectRetryItemId:         p.id(int64(p.oculusScalar(text, "disconnect retry item"))),
		MovieTime:                     p.positive(p.oculusScalar(text, "movie time")), EnterTime: p.positive(p.oculusScalar(text, "enter time")),
		EffectPassiveObjectId: p.id(int64(p.oculusScalar(text, "effect passive object index"))),
		EffectSettingRaw:      p.oculusValues(text, "effect setting", 6), RepairMachineIndex: p.id(int64(p.oculusScalar(text, "repair machine index"))),
		WarningPrayerRates: p.oculusValues(text, "warning prayer rate", 3), SourceHash: hashText(text),
	}
	if p.err != nil {
		return out, p.err
	}
	out.Modes = map[uint16]OculusMode{}
	source := make([]oculusSourceRecord, 0, len(oculusSources))
	for _, row := range oculusSources {
		m, err := s.oculusMode(row.Mode, row.Id, row.Plan, text, shared)
		if err != nil {
			return out, fmt.Errorf("Oculus %s: %w", row.Plan, err)
		}
		out.Modes[m.Mode] = m
		out.Definitions = append(out.Definitions, m.Dungeon)
		source = append(source, oculusSourceRecord{m.Mode, m.Dungeon, m.Rules, m.SharedRules, m.UsesPublishedLayout, m.EventSpawnMaps, m.StartMaps, m.WarpConditions,
			m.ClearMapId, m.ClearMapRawValue, m.SpawnPlanKind, m.SpawnPlans, m.SourceDungeonEntryFatigue, m.SourceCoinLimit, m.SourcePathgateObjectsRaw, m.SourceHash})
	}
	out.SourceJSON, err = savejson.Marshal(source)
	if err != nil {
		return out, err
	}
	out.Cards, err = s.oculusCards(items, out.Definitions)
	if err != nil {
		return out, err
	}
	stone, err := items.Ordinary(10312874)
	if err != nil || stone == nil {
		return out, fmt.Errorf("Oculus Time Guide Stone template: %v", err)
	}
	out.Stone, err = json.Marshal(stone)
	return out, err
}

func (s *catalogSource) oculusMode(mode uint16, id uint32, kind, text string, shared oculusSharedRules) (OculusMode, error) {
	var out OculusMode
	dgn, err := s.dungeonText(id)
	if err != nil {
		return out, err
	}
	p := &input{}
	if !p.require(uint32(p.modeScalar(text, "dungeon index", mode)) == id && len(mazeBlocks(dgn)) == 1, "Oculus base mode dungeon source changed") {
		return out, p.err
	}
	scalar := func(tag string) int { return p.modeScalar(text, tag, mode) }
	mon := func(tag string) uint32 { return p.id(int64(scalar(tag))) }
	values := func(tag string) []dungeon.OculusValue { return p.oculusResources(text, tag, mode) }
	r := OculusRules{
		PlayTimeSeconds: p.positive(scalar("play time")), NeedFatigue: p.u16(scalar("need fatigue")),
		DailyEntryLimit: p.positive(scalar("limit daily enter count")), WeeklyEntryLimit: p.positive(scalar("limit weekly enter count")),
		WeeklyBossRewardLimit: p.positive(scalar("limit weekly boss reward count")), MinimumMembers: p.width(int64(p.positive(scalar("limit minimum member count")))),
		RecruitingArea: p.oculusArea(text, "recruiting area", mode), WaitingArea: p.oculusArea(text, "waiting area", mode),
		ShopNpcId: mon("shop npc index"), ShopOpenMonsterId: mon("shop open monster index"), BossMonsterId: mon("boss"),
		LuiseMonsterId: mon("luise"), RoseberyRonMonsterId: mon("rosebery ron"), GrimSeekers: values("grim seeker"), Imposters: values("imposters"),
		Kaiju: values("kaiju"), MoveObjects: values("move object"), KillCoinLimitBonuses: values("kill coin limit bonus"), KillTimeoverBonuses: values("kill timeover bonus"),
		NayuPassiveObjectId: mon("nayu passive object index"), QuestClearCondition: scalar("quest clear condition"),
	}
	named := []uint32{r.BossMonsterId, r.LuiseMonsterId, r.RoseberyRonMonsterId, r.ShopOpenMonsterId}
	for _, row := range p.modeRows(text, "soryun", mode, 2) {
		id := p.id(int64(row[1]))
		p.require(!slices.Contains(r.SoryunMonsterIds, id), "Oculus soryun repeats monster identity")
		r.SoryunMonsterIds = append(r.SoryunMonsterIds, id)
	}
	named = append(named, r.SoryunMonsterIds...)
	for _, rows := range [][]dungeon.OculusValue{r.GrimSeekers, r.Imposters, r.Kaiju, r.KillCoinLimitBonuses, r.KillTimeoverBonuses} {
		for _, row := range rows {
			named = append(named, row.Id)
		}
	}
	for _, id := range named {
		p.require(s.monsters[int64(id)] != "", "Oculus rule references missing monster")
	}
	for _, row := range r.KillCoinLimitBonuses {
		p.require(row.RawValue >= 1 && row.RawValue <= 255, "Oculus coin bonus exceeds native width")
	}
	if p.err != nil {
		return out, p.err
	}
	d, err := s.definition(id, dgn, 0, layoutOptions{mainline: true})
	if err != nil {
		return out, err
	}
	rawFatigue := p.u16(p.oculusScalar(dgn, "use fatigue only start dungeon"))
	p.require(d.Difficulty == 4 && d.MinimumLevel == 100 && len(d.meta.quests) == 0, "Oculus base mode level or maze qualification unsupported")
	starts := p.oculusPositions(p.oculusValues(dgn, "start map", -1))
	p.oculusMapSpecification(dgn, d)
	warps := p.oculusWarps(dgn)
	clear := p.oculusValues(dgn, "clear map", 2)
	boss := p.oculusValues(dgn, "boss map", 2)
	coins := p.positive(p.oculusScalar(dgn, "coin limit"))
	p.require(coins <= 255, "Oculus SourceCoinLimit exceeds native byte width")
	if p.err != nil {
		return out, p.err
	}
	clearMap := p.id(int64(clear[0]))
	p.require(slices.ContainsFunc(d.Rooms, func(room opening.Room) bool {
		return room.MapID == clearMap && int(room.X) == boss[0] && int(room.Y) == boss[1]
	}),
		"Oculus boss and terminal clear map disagree")
	events := []uint32{}
	for _, room := range d.Rooms {
		mapText, err := s.readMap(room.MapID, false)
		if err != nil {
			return out, err
		}
		if !tagPresent(mapText, "event monster position") {
			continue
		}
		positions := p.oculusValues(mapText, "event monster position", -1)
		p.require(len(positions)%3 == 0 && len(positions) <= 192, "Oculus event position source exceeds native capacity")
		if len(positions) > 0 {
			events = append(events, room.MapID)
		}
	}
	slices.Sort(events)
	if err := oculusReachable(d, startMapKeys(starts), warps); err != nil {
		return out, err
	}
	plans := p.oculusPlans(text, kind)
	pathgate := p.oculusValues(dgn, "pathgate object", -1)
	if p.err != nil {
		return out, p.err
	}
	hash := hashText(fmt.Sprintf("OculusBase/v1\n%d\n%s\n%s", mode, shared.SourceHash, d.ResourceHash))
	d.ResourceHash = hash
	d.UsesOrdinaryRewards, d.UsesOrdinaryMonsterDrops = false, false
	d.EntryFatigueCost, d.RoomFatigueCost = r.NeedFatigue, 0
	d.meta.selectable = []byte{4}
	var samples []opening.Monster
	for _, room := range d.Rooms {
		for _, mon := range room.Monsters {
			if mon.AiScriptIndex == nil && mon.Code != 0 && mon.Level != 0 {
				samples = append(samples, mon)
			}
		}
	}
	count := len(samples)
	for i := 0; i < count; i++ {
		m := samples[i]
		m.Type = 3
		samples = append(samples, m)
	}
	awards := []Award{}
	if !d.meta.disableExperience {
		awards, err = s.xp.awards(ExperienceProfile{Multiplier: d.meta.multiplier}, 4, samples)
		if err != nil {
			return out, err
		}
	}
	out = OculusMode{Mode: mode, Dungeon: d, Rules: r, SharedRules: shared, UsesPublishedLayout: mode == 2 || mode == 6,
		EventSpawnMaps: events, DisableDrops: d.meta.disableDrops, DisableClearReward: d.meta.disableClear, DisableExperience: d.meta.disableExperience,
		UsesOrdinaryRewards: false, UsesOrdinaryMonsterDrops: false, ExperienceMultiplier: d.meta.multiplier, SelectableDifficulties: []uint16{4},
		StartMaps: starts, WarpConditions: warps, ClearMapId: clearMap, ClearMapRawValue: clear[1], SpawnPlanKind: kind, SpawnPlans: plans,
		SourceDungeonEntryFatigue: rawFatigue, SourceCoinLimit: coins, SourcePathgateObjectsRaw: pathgate, SourceHash: hash,
		Awards: []dungeon.ExperienceRules{{Difficulty: 4, Monsters: awards}}}
	return out, nil
}

func (p *input) positive(value int) int {
	p.require(value > 0, "Oculus rule must be positive")
	return value
}

func (p *input) u16(x int) uint16 {
	p.require(x >= 0 && x <= 65535, "Oculus value exceeds U16")
	return uint16(x)
}

func (p *input) oculusSection(text, tag string) string {
	fields := pvftext.Blocks(text, tag)
	if !p.require(len(fields) == 1, "Oculus declaration absent or repeated: "+tag) {
		return ""
	}
	return fields[0]
}

func (p *input) oculusValues(text, tag string, width int) []int {
	v, err := pvftext.Integers(p.oculusSection(text, tag))
	p.require(err == nil && len(v) <= 4096 && (width < 0 || len(v) == width), "Oculus declaration width invalid: "+tag)
	return v
}

func (p *input) oculusScalar(text, tag string) int {
	x := p.oculusValues(text, tag, 1)
	if len(x) != 1 {
		return 0
	}
	return x[0]
}

func (p *input) modeRows(text, tag string, mode uint16, width int) [][]int {
	v := p.oculusValues(text, tag, -1)
	if !p.require(len(v) > 0 && len(v)%width == 0, "Oculus mode row has invalid width: "+tag) {
		return nil
	}
	var out [][]int
	for i := 0; i < len(v); i += width {
		p.require(v[i] >= 0 && v[i] <= 65535, "Oculus mode key exceeds native width")
		if v[i] == int(mode) {
			out = append(out, v[i:i+width])
		}
	}
	p.require(len(out) > 0, "Oculus mode rule is absent: "+tag)
	return out
}

func (p *input) modeScalar(text, tag string, mode uint16) int {
	v := p.modeRows(text, tag, mode, 2)
	if !p.require(len(v) == 1, "Oculus mode scalar absent or repeated: "+tag) {
		return 0
	}
	return v[0][1]
}

func (p *input) oculusArea(text, tag string, mode uint16) dungeon.OculusArea {
	v := p.modeRows(text, tag, mode, 5)
	if !p.require(len(v) == 1, "Oculus mode area is absent or repeated") {
		return dungeon.OculusArea{}
	}
	row := v[0]
	return dungeon.OculusArea{Town: p.width(int64(row[1])), Area: p.width(int64(row[2])), X: p.u16(row[3]), Y: p.u16(row[4])}
}

func (p *input) oculusResources(text, tag string, mode uint16) []dungeon.OculusValue {
	v := p.modeRows(text, tag, mode, 3)
	out := make([]dungeon.OculusValue, 0, len(v))
	seen := map[uint32]bool{}
	for _, row := range v {
		id := p.id(int64(row[1]))
		p.require(!seen[id], "Oculus mode resource identity repeated")
		seen[id] = true
		out = append(out, dungeon.OculusValue{Id: id, RawValue: row[2]})
	}
	return out
}

func (p *input) position(x, y int) dungeon.OculusPosition {
	p.require(x >= 0 && x < 10 && y >= 0 && y < 6, "Oculus position leaves native floor grid")
	return dungeon.OculusPosition{X: byte(x), Y: byte(y), Floor: byte(x/5 + 2*(y/3))}
}

func (p *input) oculusPositions(values []int) []dungeon.OculusPosition {
	if !p.require(len(values) > 0 && len(values)%2 == 0 && len(values) <= 512, "Oculus start candidates empty or malformed") {
		return nil
	}
	seen := map[dungeon.OculusPosition]bool{}
	out := []dungeon.OculusPosition{}
	for i := 0; i < len(values); i += 2 {
		pos := p.position(values[i], values[i+1])
		p.require(!seen[pos], "Oculus start candidates repeated")
		seen[pos] = true
		out = append(out, pos)
	}
	return out
}

var oculusMapSpec = regexp.MustCompile("`map`\\s+(\\d+)\\s+(\\d+)\\s+(\\d+)\\s*")

func (p *input) oculusMapSpecification(text string, d Definition) {
	body := p.oculusSection(text, "map specification")
	maps := oculusMapSpec.FindAllStringSubmatchIndex(body, -1)
	p.completeRows(body, maps, "Oculus map specification contains unsupported alternatives")
	p.require(len(maps) > 0 && len(maps) == len(d.Rooms), "Oculus map specification incomplete")
	positions := map[dungeon.OculusPosition]bool{}
	ids := map[uint32]bool{}
	for _, match := range maps {
		row := matchFields(body, match)
		pos := p.position(int(p.number(row[1], 32)), int(p.number(row[2], 32)))
		id := p.id(p.number(row[3], 32))
		p.require(!positions[pos] && !ids[id], "Oculus map identity or coordinates repeated")
		positions[pos], ids[id] = true, true
	}
}

var oculusWarp = regexp.MustCompile(`(?i)\[source grid pos\]\s+(\d+)\s+(\d+)\s+\[dest grid pos\]\s+(\d+)\s+(\d+)\s*`)

func (p *input) oculusWarps(text string) []dungeon.OculusWarp {
	body := p.oculusSection(text, "warp map condition")
	rows := oculusWarp.FindAllStringSubmatchIndex(body, -1)
	p.completeRows(body, rows, "Oculus warp declaration incomplete")
	p.require(len(rows) > 0 && len(rows) <= 256, "Oculus warp declaration exceeds capacity")
	seen := map[dungeon.OculusWarp]bool{}
	out := []dungeon.OculusWarp{}
	for _, row := range rows {
		f := matchFields(body, row)
		pos := func(i int) dungeon.OculusPosition {
			return p.position(int(p.number(f[i], 32)), int(p.number(f[i+1], 32)))
		}
		edge := dungeon.OculusWarp{Source: pos(1), Destination: pos(3)}
		p.require(!seen[edge], "Oculus warp edge repeated")
		seen[edge] = true
		out = append(out, edge)
	}
	return out
}

func startMapKeys(positions []dungeon.OculusPosition) [][2]byte {
	keys := make([][2]byte, len(positions))
	for i, p := range positions {
		keys[i] = [2]byte{p.X, p.Y}
	}
	return keys
}

func oculusReachable(d Definition, starts [][2]byte, warps []dungeon.OculusWarp) error {
	m := map[[2]byte]opening.Room{}
	for _, r := range d.Rooms {
		m[[2]byte{r.X, r.Y}] = r
	}
	for _, p := range starts {
		if m[p].MapID == 0 {
			return fmt.Errorf("Oculus start leaves declared maze")
		}
	}
	edges := map[[2]byte][][2]byte{}
	for _, w := range warps {
		src, dst := [2]byte{w.Source.X, w.Source.Y}, [2]byte{w.Destination.X, w.Destination.Y}
		if m[src].MapID == 0 || m[dst].MapID == 0 {
			return fmt.Errorf("Oculus warp endpoint leaves declared maze")
		}
		edges[src] = append(edges[src], dst)
	}
	for p, r := range m {
		for _, move := range []struct {
			x, y        int
			exit, entry byte
		}{{1, 0, 1, 4}, {0, -1, 2, 8}, {-1, 0, 4, 1}, {0, 1, 8, 2}} {
			k := [2]byte{byte(int(r.X) + move.x), byte(int(r.Y) + move.y)}
			if r.Gates&move.exit != 0 && m[k].Gates&move.entry != 0 {
				edges[p] = append(edges[p], k)
			}
		}
	}
	queue := [][2]byte{starts[0]}
	visited := map[[2]byte]bool{starts[0]: true}
	for i := 0; i < len(queue); i++ {
		for _, t := range edges[queue[i]] {
			if !visited[t] {
				visited[t] = true
				queue = append(queue, t)
			}
		}
	}
	if len(visited) != len(m) {
		return fmt.Errorf("Oculus maze has unreachable rooms through doors and directed warps")
	}
	return nil
}

var (
	oculusPlanHeader = regexp.MustCompile(`(?im)^\[(normal|hard|guide|extend|single)\][ \t]*\r?$`)
	oculusPlanEnd    = regexp.MustCompile(`(?im)^\[/extend\][ \t]*\r?$`)
	oculusPlan       = regexp.MustCompile(`(?is)\[plan\]\s*(.*?)\[/plan\]\s*`)
)

func (p *input) oculusPlans(text, kind string) []oculusSpawnPlan {
	body := p.oculusSection(text, "spawn plan")
	headers := oculusPlanHeader.FindAllStringSubmatchIndex(body, -1)
	p.require(len(headers) <= 16, "Oculus spawn plan mode exceeds capacity")
	seen := map[string]bool{}
	var selected string
	for i, header := range headers {
		tag := strings.ToLower(body[header[2]:header[3]])
		p.require(!seen[tag], "Oculus spawn plan mode repeated")
		seen[tag] = true
		if tag != kind {
			continue
		}
		end := len(body)
		if i+1 < len(headers) {
			end = headers[i+1][0]
		}
		selected = oculusPlanEnd.ReplaceAllString(body[header[1]:end], "")
	}
	rows := oculusPlan.FindAllStringSubmatchIndex(selected, -1)
	p.completeRows(selected, rows, "Oculus spawn plan list incomplete")
	p.require(len(rows) > 0 && len(rows) <= 64, "Oculus spawn plan list missing or exceeds capacity")
	var out []oculusSpawnPlan
	for _, pos := range rows {
		raw := strings.TrimSpace(selected[pos[2]:pos[3]])
		columns, err := pvftext.Integers(raw)
		if !p.require(err == nil && len(columns) == 18, "Oculus spawn plan requires eighteen columns") {
			return nil
		}
		floors := map[int]bool{}
		for i := 0; i < 18; i += 6 {
			f := columns[i]
			p.require(f >= 1 && f <= 3 && !floors[f] && slices.Min(columns[i+1:i+6]) >= 0 && slices.Max(columns[i+1:i+6]) <= 255,
				"Oculus spawn plan requires distinct floors and five native bytes")
			floors[f] = true
		}
		out = append(out, oculusSpawnPlan{columns, hashText(raw)})
	}
	return out
}
