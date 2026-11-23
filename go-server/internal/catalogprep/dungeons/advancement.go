package dungeons

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"hash"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"s9a1/server/internal/catalogprep/commerce"
	"s9a1/server/internal/opening"
	"s9a1/server/internal/questwire"
)

type advancementSource struct {
	gate       uint16
	id, target uint32
	stage      byte
	quest      uint16
}

var advanceSources = []advancementSource{
	{7, 7123, 53546, 1, 0}, {17, 5100, 95264, 2, 0}, {186, 100000194, 100000442, 0, 6422}, {185, 100000325, 100001118, 0, 12301},
	{185, 100000163, 100000301, 0, 6226}, {185, 100000164, 100000306, 0, 6227}, {185, 100000165, 100000311, 0, 6228}, {185, 100000175, 100000443, 0, 6413},
	{185, 291100433, 292106851, 0, 12155}, {185, 291100434, 292106862, 0, 12156}, {185, 291100434, 292106875, 0, 12157},
	{185, 100000006, 292106897, 0, 12158}, {185, 100000006, 292106906, 0, 12159}, {185, 291100434, 292106886, 0, 12163},
	{185, 100000006, 292106918, 0, 12164}, {185, 100000006, 292106929, 0, 12165}, {185, 100000156, 292106937, 0, 12166},
}

func BuildAdvancement(a Archive, xp *Experience, quests []questwire.Quest) (Layouts, error) {
	s, err := newCatalogSource(a, xp, quests, nil)
	if err != nil {
		return Layouts{}, err
	}
	return s.advancement()
}

func (s *catalogSource) advancement() (Layouts, error) {
	var out Layouts
	qualified := map[[3]uint32]bool{}
	for _, source := range advanceSources {
		branches, err := s.advanceSource(source, qualified)
		if err != nil {
			return out, fmt.Errorf("advancement dungeon %d: %w", source.id, err)
		}
		out.Definitions = append(out.Definitions, branches...)
	}
	tournament, err := s.tournament()
	if err != nil {
		return out, err
	}
	out.Definitions = append(out.Definitions, tournament...)
	return out, nil
}

func (s *catalogSource) advanceSource(src advancementSource, qualified map[[3]uint32]bool) ([]Layout, error) {
	world, err := s.worldText(int(src.gate))
	if err != nil {
		return nil, err
	}
	text, err := s.dungeonText(src.id)
	if err != nil {
		return nil, err
	}
	selectors, err := advancementSelectors(world, src.id)
	if err != nil {
		return nil, err
	}
	mazeBodies := map[string]bool{}
	connected := map[uint16]bool{}
	out := []Layout{}
	for i, raw := range mazeBlocks(text) {
		body := strings.TrimSpace(raw)
		if mazeBodies[body] {
			continue
		}
		mazeBodies[body] = true
		p := &input{}
		connections := p.questConnections(raw)
		for _, q := range connections {
			p.require(!connected[q], "Advancement quest has conflicting maze sources")
			connected[q] = true
		}
		if p.err != nil {
			return nil, p.err
		}
		var candidates []uint16
		for _, id := range selectors {
			q := s.quests[id]
			wanted := src.quest == id || src.quest == 0 && q.AwakeningAward != nil && q.AwakeningAward.Stage == src.stage
			if wanted && q.Supported && q.Kind == 1 && len(q.Data) > 0 && q.Data[0] == int(src.target) &&
				(len(connections) == 0 || slices.Contains(connections, id)) {
				candidates = append(candidates, id)
			}
		}
		if len(candidates) == 0 {
			continue
		}
		d, err := s.definition(src.id, text, i, layoutOptions{plainMapText: true})
		if err != nil {
			return nil, err
		}
		expected := byte(0)
		if src.stage == 2 {
			expected = 2
		}
		if d.Difficulty != expected || src.stage == 2 && (!d.meta.disableDrops || !d.meta.disableClear || !d.meta.disableExperience) ||
			!slices.ContainsFunc(d.Rooms, func(r opening.Room) bool { return r.MapID == src.target || slices.Contains(r.MapAliases, src.target) }) {
			return nil, fmt.Errorf("advancement maze no longer matches target or reward restrictions")
		}
		d.UsesOrdinaryRewards, d.UsesOrdinaryMonsterDrops = false, false
		d, err = s.awakeningActors(d)
		if err != nil {
			return nil, err
		}
		if src.id == 7123 {
			d, err = s.firstAwakeningTransfer(d)
			if err != nil {
				return nil, err
			}
		}
		for _, id := range candidates {
			key := [3]uint32{uint32(src.gate), src.id, uint32(id)}
			if qualified[key] {
				return nil, fmt.Errorf("advancement quest %d has ambiguous maze connections", id)
			}
			qualified[key] = true
			own := d
			if len(connections) == 0 {
				own.meta.quests = []uint16{id}
			}
			own, err = s.bindCinematics(own)
			if err != nil {
				return nil, err
			}
			l, err := s.layout(src.gate, own, "advancement", []uint16{id}, nil, nil, nil)
			if err != nil {
				return nil, err
			}
			out = append(out, l)
		}
	}
	return out, nil
}

func advancementSelectors(world string, id uint32) ([]uint16, error) {
	rows, err := worldSelectors(world)
	if err != nil {
		return nil, err
	}
	seen := map[uint16]bool{}
	var out []uint16
	for _, r := range rows {
		if r.Dungeon == id && r.InProgress {
			q := uint16(r.Quest)
			if seen[q] {
				return nil, fmt.Errorf("advancement selector has a duplicate quest connection")
			}
			seen[q] = true
			out = append(out, q)
		}
	}
	slices.Sort(out)
	return out, nil
}

type awakeningActor struct {
	code    uint32
	hostile bool
	typ     byte
}

func (s *catalogSource) awakeningActors(d Definition) (Definition, error) {
	var mapID uint32
	var expected []awakeningActor
	switch d.ID {
	case 7123:
		mapID, expected = 53546, []awakeningActor{{1521, false, 0}, {2005, true, 3}}
	case 100000194:
		mapID, expected = 100000355, []awakeningActor{{25437, true, 0}, {5449, true, 0}, {10456, true, 0}}
	case 100000325:
		mapID, expected = 100001118, []awakeningActor{{2100, false, 0}}
	default:
		return d, nil
	}
	i := slices.IndexFunc(d.Rooms, func(r opening.Room) bool { return r.MapID == mapID })
	if i < 0 {
		return d, fmt.Errorf("awakening dungeon lacks its APC map %d", mapID)
	}
	r := &d.Rooms[i]
	p := &input{}
	text, err := s.readMap(mapID, true)
	if err != nil {
		return d, err
	}
	body := p.field(text, "ai character", true)
	rows := aicRow.FindAllStringSubmatchIndex(body, -1)
	p.completeRows(body, rows, "awakening APC script rows changed")
	if !p.require(len(rows) == len(expected) && d.MinimumLevel != 0 && len(r.Companions) == 0 &&
		len(r.Monsters)+len(expected) <= 255 && !slices.ContainsFunc(r.Monsters, func(m opening.Monster) bool { return m.AiScriptIndex != nil }),
		"awakening APC projection has a mismatched source") {
		return d, p.err
	}
	identity := []string{d.ResourceHash}
	for i, a := range expected {
		row := matchFields(body, rows[i])
		p.require(p.id(p.number(row[1], 64)) == a.code && (row[5] == "monster") == a.hostile && (row[6] == "boss") == (a.typ == 3) &&
			row[7] == "0" && row[8] == "0", "awakening APC script rows changed")
		for _, coord := range row[2:5] {
			p.number(coord, 32)
		}
		slot := byte(i)
		if a.hostile {
			r.Monsters = append(r.Monsters, opening.Monster{Code: a.code, Level: d.MinimumLevel, Type: a.typ, BlocksRoom: true, AiScriptIndex: &slot})
		} else {
			r.Companions = append(r.Companions, opening.Companion{Code: a.code, Level: d.MinimumLevel, Type: 5, ScriptIndex: slot})
		}
		path := s.aics[int64(a.code)]
		if path == "" {
			return d, fmt.Errorf("awakening APC template %d is absent", a.code)
		}
		template, err := s.a.ReadText(path, budget)
		if err != nil {
			return d, err
		}
		p.require(strings.TrimSpace(template) != "", "awakening APC template is empty")
		raw, err := s.a.Read(path, budget)
		if err != nil {
			return d, err
		}
		identity = append(identity, fmt.Sprintf("%d:%s:%X", a.code, path, sha256.Sum256(raw)))
	}
	if r.X == d.Start.X && r.Y == d.Start.Y {
		d.Start = *r
	}
	d.ResourceHash = hashText(strings.Join(identity, "\n"))
	return d, p.err
}

func (s *catalogSource) firstAwakeningTransfer(d Definition) (Definition, error) {
	i := slices.IndexFunc(d.Rooms, func(r opening.Room) bool { return r.MapID == 57792 })
	if i < 0 {
		return d, nil
	}
	sources := []struct{ path, hash string }{
		{s.maps[57792], "423AB5449456B5EDDFCB3BD74C18C193A5E0CCFEE4BD9435CF8BEBEAD41B072D"},
		{s.maps[53663], "958BA2BC2DF0CF80146B9987079056D0349D173EF244919781867A1D26F8736A"},
		{s.maps[53545], "AC6899AB8B350B4F11492F260E5947A6EE01DF683714CE148BD30B5FEB1C2A5F"},
		{"Map/Cataclysm/FirstAwakening/Swordman_M/Action/blind4.act", "B4CF2EEB498D64FFE1E6D784CEAA17F627D01C192AD335E9A2733120F3573387"},
		{"Map/Cataclysm/ShelterOfNorth/01_ShelterOfNorth/Action/53663.act", "A76562391F8B43C6C1EABCB26B230999D96FDDA6A3C89DBF8516E8DB747721F7"},
		{"Cinematic/Dungeon/Cataclysm/FirstAwakening/Swordman_M/5109.cmt", "0D7F92D9A95B8ADFEA065E4AE23FBC5F29368045E1AA6DFD651CB76E6E2A541D"},
		{"Cinematic/Dungeon/Cataclysm/FirstAwakening/Swordman_M/5110.cmt", "DE8078407F6C87E5737937CF175A23596D1D10713D85BCD1493806B50B186829"},
		{"Cinematic/Dungeon/Cataclysm/FirstAwakening/Swordman_M/5111.cmt", "C72812B1D1ABD2E65CB0D4AAED0B234C863966DB4435D4C936B9F8ACA567A616"},
		{"Cinematic/Dungeon/Cataclysm/FirstAwakening/Swordman_M/5112.cmt", "64248A8D151084FDA5AADB11A39C745B717C916597E5BED09F2D34A840A2D578"},
		{"Cinematic/Dungeon/Cataclysm/FirstAwakening/Swordman_M/5113.cmt", "80AEBFB347D6CF145055799302D698C38E4979767C454D1A887BB0065C97FEE9"},
		{"Cinematic/Dungeon/Cataclysm/FirstAwakening/Swordman_M/5114.cmt", "C9ECFC208F7053BE41A0CFAF55F33D2D5CC8206B98DC34DEA6B5F189F43AE416"},
		{"Cinematic/Dungeon/Cataclysm/FirstAwakening/Swordman_M/5115.cmt", "CD6DE5286E1E05A852478BEC40F7FB9014BF1BBD227BAF2B9052932ABEB36C6D"},
	}
	var identity []string
	for _, source := range sources {
		raw, err := s.a.Read(source.path, budget)
		if err != nil {
			return d, err
		}
		if fmt.Sprintf("%X", sha256.Sum256(raw)) != source.hash {
			return d, nil
		}
		identity = append(identity, source.path+":"+source.hash)
	}
	r := &d.Rooms[i]
	codes := []uint32{}
	for _, m := range r.Monsters {
		codes = append(codes, m.Code)
	}
	if !slices.Equal(r.MapAliases, []uint32{53663}) || r.CinematicLayer != nil || !slices.Equal(codes, []uint32{65455, 65456, 63821, 65455}) ||
		!slices.ContainsFunc(d.Rooms, func(r opening.Room) bool { return r.X == 3 && r.Y == 1 && r.MapID == 53545 }) {
		return d, fmt.Errorf("First-awakening transit graph differs from its pinned scripts")
	}
	text, err := s.readMap(53663, true)
	if err != nil {
		return d, err
	}
	p := &input{}
	p.require(strings.TrimSpace(p.field(text, "monster", false)) == "" && strings.TrimSpace(p.field(text, "ai character", false)) == "",
		"First-awakening transit layer must contain no combat actors")
	hash := hashText(strings.Join(identity, "\n"))
	r.CinematicLayer = &opening.Room{X: r.X, Y: r.Y, MapID: 53663, PassiveObjectCodes: p.passives(text, true), NpcTemplates: p.npcs(text)}
	r.MapAliases = nil
	r.ScriptTransfer = &opening.ScriptTransfer{X: 3, Y: 1, Map: 53545, Hash: hash, RequiredDeaths: []byte{0, 1, 3}}
	d.ResourceHash = hashText(d.ResourceHash + "\n" + hash)
	return d, p.err
}

type tournamentEnemy struct {
	Code              uint32
	ResourceParameter int
	Name              string
}
type tournamentData struct {
	ArenaMapId uint32
	BasisLevel byte
	Enemies    []tournamentEnemy
	RewardItem uint32
	Bracket    []tournamentEnemy
}

var (
	tournamentHeader = regexp.MustCompile("^\\s*1\\s+`\\[monster\\]`\\s*")
	tournamentRow    = regexp.MustCompile("(\\d+)\\s+(\\d+)\\s+`([^`\\r\\n]+)`\\s*")
)

func addLengthText(h hash.Hash, text string) {
	h.Write(binary.LittleEndian.AppendUint32(nil, uint32(len(text))))
	h.Write([]byte(text))
}

func (s *catalogSource) tournament() ([]Layout, error) {
	world, err := s.worldText(16)
	if err != nil {
		return nil, err
	}
	var out []Layout
	for _, row := range []struct{ id, arena, reward uint32 }{{123, 95265, 3329}, {124, 95266, 3323}} {
		text, err := s.dungeonText(row.id)
		if err != nil {
			return nil, err
		}
		arena, err := s.readMap(row.arena, true)
		if err != nil {
			return nil, err
		}
		p := &input{}
		for tag, value := range map[string]int64{"tournament dungeon": 1, "minimum required level": 75, "basis level": 75,
			"designate dungeon difficulty": 2, "enter without fatigue": 1, "tournament round fatigue": 0, "limit party count": 1} {
			p.require(p.scalar(text, tag, -1) == value, "tournament source qualification changed "+tag)
		}
		for _, tag := range []string{"disable drop item", "disable exp", "no fatigue"} {
			p.require(tagPresent(text, tag) && p.field(text, tag, true) == "", "tournament reward restriction changed "+tag)
		}
		p.require(!tagPresent(text, "disable clear reward") && p.scalar(arena, "dungeon", 0) == int64(row.id) &&
			len(mazeBlocks(text)) == 1 && !tagPresent(text, "map specification") && p.field(text, "size", true) == "1 1" &&
			p.field(text, "start map", true) == "0 0" && p.field(text, "boss map", true) == "0 0", "tournament arena connection changed")
		p.require(slices.Equal(p.numbers(p.field(text, "reward item rate", true)), []int64{int64(row.reward), 100, 1}) &&
			slices.Equal(p.numbers(p.field(text, "tournament clear reward exp", true)), []int64{0, 0, 1, 0, 2, 0, 3, 0, 4, 0}),
			"tournament fixed reward changed")
		body := p.field(arena, "tournament enemies", true)
		header := tournamentHeader.FindStringIndex(body)
		if !p.require(header != nil, "tournament enemy category changed") {
			return nil, p.err
		}
		body = body[header[1]:]
		positions := tournamentRow.FindAllStringSubmatchIndex(body, -1)
		p.completeRows(body, positions, "tournament opponent rows incomplete")
		p.require(len(positions) == 15, "tournament requires fifteen opponents")
		if p.err != nil {
			return nil, p.err
		}
		h := sha256.New()
		for _, source := range []string{strconv.FormatUint(uint64(row.id), 10), strconv.FormatUint(uint64(row.arena), 10), text, arena} {
			addLengthText(h, source)
		}
		data := tournamentData{ArenaMapId: row.arena, BasisLevel: 75, RewardItem: row.reward, Bracket: []tournamentEnemy{}}
		seen := map[uint32]bool{}
		for _, pos := range positions {
			m := matchFields(body, pos)
			id, parameter := p.id(p.number(m[1], 64)), p.number(m[2], 32)
			path := s.monsters[int64(id)]
			p.require(!seen[id] && path != "" && (parameter == 300 || parameter == 600 || parameter == 900), "invalid tournament opponent")
			seen[id] = true
			template, err := s.a.ReadText(path, budget)
			if err != nil {
				return nil, err
			}
			levels := p.numbers(p.field(template, "level", true))
			p.require(len(levels) == 2 && levels[0] >= 1 && levels[1] >= levels[0] && levels[1] <= 255, "invalid tournament opponent level range")
			addLengthText(h, m[1])
			addLengthText(h, template)
			data.Enemies = append(data.Enemies, tournamentEnemy{Code: id, ResourceParameter: int(parameter), Name: m[3]})
		}
		room := opening.Room{MapID: row.arena, Gates: 1, NpcTemplates: p.npcs(arena), PassiveObjectCodes: p.passives(arena, false)}
		if p.err != nil {
			return nil, p.err
		}
		d := Definition{Dungeon: opening.Dungeon{ID: row.id, MinimumLevel: 75, Start: room, Rooms: []opening.Room{room}, Difficulty: 2,
			ResourceHash: hashText(fmt.Sprintf("%X\n%s", h.Sum(nil), world))},
			meta: nativeMeta{disableDrops: true, disableExperience: true, multiplier: "1"}, UsesOrdinaryMonsterDrops: true, Tournament: data}
		selectors, err := advancementSelectors(world, row.id)
		if err != nil {
			return nil, err
		}
		for _, id := range selectors {
			q := s.quests[id]
			if !q.Supported || q.Kind != 1 || q.AdvancementChain != 3 || !slices.Equal(q.Data, []int{int(row.arena)}) {
				continue
			}
			d.meta.quests = []uint16{id}
			l, err := s.layout(16, d, "advancement", []uint16{id}, nil, nil, nil)
			if err != nil {
				return nil, err
			}
			out = append(out, l)
		}
	}
	return out, nil
}

func BuildTournamentRewards(a Archive) (map[uint32]json.RawMessage, error) {
	items, err := commerce.NewItems(a)
	if err != nil {
		return nil, err
	}
	out := map[uint32]json.RawMessage{}
	for _, id := range []uint32{3329, 3323} {
		t, err := items.Ordinary(id)
		if err != nil || t == nil || t.Kind != 2 {
			return nil, fmt.Errorf("tournament fixed material %d is unavailable: %v", id, err)
		}
		raw, err := json.Marshal(t)
		if err != nil {
			return nil, err
		}
		out[id] = raw
	}
	return out, nil
}
