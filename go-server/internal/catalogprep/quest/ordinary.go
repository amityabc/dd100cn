package quest

import (
	"fmt"
	"regexp"
	"slices"
	"strconv"

	"s9a1/server/internal/questwire"
)

const (
	meetNPC = iota
	clearMap
	huntEnemy
	huntMonster
	seeking
	seekAndMeet
	questClear
	reachRange
	clearCondition
	unsupported
	cinematic
	question
	communicator
	duelTraining
)

const trainingManual = 10158452

var questKinds = map[string]int{
	"meet npc": meetNPC, "clear map": clearMap, "hunt enemy": huntEnemy, "hunt monster": huntMonster,
	"seeking": seeking, "seek n meet npc": seekAndMeet, "quest clear": questClear, "reach the range": reachRange,
	"condition under clear": clearCondition, "look cinematic": cinematic, "question": question,
	"use item n meet npc": communicator, "custom multi condition": duelTraining,
}

type townMovie = struct {
	Movie      uint32
	Town, Area uint16
}
type destinationRequirement = struct{ Dungeon, MinimumDifficulty int }

func parseOrdinary(id uint16, text, rawHash string) (questwire.Quest, error) {
	s := script{text: text}
	grade, objective := s.tag("grade"), s.tag("type")
	kind, known := questKinds[objective]
	q := questwire.Quest{Hash: rawHash, Kind: kind, Subtype: s.scalar("sub type", -1), Data: s.data("int data")}
	q.Criterion.Id = id
	if !known || kind == duelTraining && !anyOf(id, 2672, 2673, 2674) || len(q.Data) == 0 {
		return q, fmt.Errorf("unsupported quest objective")
	}
	q.FateIntroduction = s.fateIntroduction(id, grade, objective)
	for _, tag := range []string{"clear reward item", "account collision quest", "event quest", "is account quest", "expertjob level"} {
		if s.section(tag) != "" {
			return q, fmt.Errorf("unsupported quest owner %s", tag)
		}
	}
	if len(s.blocks("account quest")) > 0 || s.scalar("event", 0) != 0 && !q.FateIntroduction || s.scalar("creature kind", -1) >= 0 ||
		s.scalar("expert job type", -1) >= 0 || s.scalar("is event", 0) != 0 || s.scalar("is account", 0) != 0 ||
		s.scalar("exposed by npc", 1) == 0 && !legacyUpper(id, rawHash) {
		return q, fmt.Errorf("unsupported event, account, profession, creature or hidden quest")
	}
	daily := grade == "daily mission" && isDaily(id) && kind == seeking && len(s.data("main quest")) == 0 &&
		s.scalar("job change quest", 0) == 0 && len(s.data("casecade clear")) == 0
	if !anyOf(grade, "", "normal", "side", "sub", "epic", "episode", "mission", "achievement", "common unique", "realization", "system") && !daily && !q.FateIntroduction {
		return q, fmt.Errorf("unsupported quest grade")
	}
	c := s.eligibility(id, rawHash, false)
	if c.Grow < -1 || c.Grow > 5 || c.GrowNumber < -1 || c.GrowNumber > 5 {
		s.fail("unsupported quest advancement restriction")
	}
	q.Criterion = c
	q.Required = slices.DeleteFunc(q.Required, func(ids []uint16) bool { return len(ids) == 0 })
	if id == 3162 && kind == reachRange && q.Subtype == 0 && (len(q.Required) != 1 || !slices.Equal(q.Required[0], []uint16{3161})) {
		s.fail("unsupported Minet prerequisite")
	}
	s.objective(&q, grade)
	s.destinations(&q, grade)
	q.AdvancementChain = s.scalar("job change quest", 0)
	if !anyOf(q.AdvancementChain, 0, 2, 3, 4) {
		s.fail("unsupported advancement quest chain")
	}
	rewardType := s.tag("reward type")
	if daily && (q.Parent != 0 || len(q.Children) != 0 || rewardType != "item") {
		s.fail("unsupported daily quest reward owner")
	}
	s.rewards(&q, grade, rewardType)
	q.CascadeClear = distinct(s.ids(s.data("casecade clear")))
	q.SkillEffects = s.skillEffects()
	q.DropRules = s.dropRules(&q, grade)
	q.Operations = &questwire.OperationRules{
		CanAbandon: s.scalar("cant giveup", 0) == 0, Available: true, Grade: grade,
		AcceptItems: s.acceptanceItems(&q), Daily: daily, DuelTraining: q.Kind == duelTraining,
	}
	q.Supported = s.err == nil
	q.AcceptanceSupported = q.Supported
	return q, s.err
}

func (s *script) fateIntroduction(id uint16, grade, objective string) bool {
	if !anyOf(id, 8884, 8885) {
		return false
	}
	npc := 400001285
	var predecessors []int
	if id == 8885 {
		npc, predecessors = 400001287, []int{8884}
	}
	return s.scalar("event", 0) == 1 && s.scalar("show quest on event", 0) == 4171 && grade == "special" &&
		objective == "meet npc" && s.scalar("npc index", 0) == 400001285 && s.scalar("complete npc index", 0) == npc &&
		slices.Equal(s.data("int data"), []int{npc}) && slices.Equal(s.data("level"), []int{100, 10000}) &&
		s.scalar("synchronization account quest", 0) == 1 && slices.Equal(s.data("pre required quest"), predecessors) &&
		s.tag("reward type") == "item" && slices.Equal(s.data("reward int data"), []int{0, 100})
}

func isDaily(id uint16) bool { return anyOf(id, 2683, 2901, 2902, 4099, 4100, 8714, 10019) }

var questionGuide = regexp.MustCompile("^22\\s+3\\s+331\\s+-1\\s+\x60\\[bottom\\]\x60$")
var communicatorGuide = regexp.MustCompile("^76\\s+0\\s+100000198\\s+-1\\s+\x60\\[bottom\\]\x60$")

func (s *script) question(id uint16, grade string, subtype int, data []int) byte {
	expected := 0
	switch id {
	case 3868:
		expected = 3
	case 3869:
		expected = 2
	}
	blocks := s.blocks("question info")
	if expected == 0 || grade != "epic" || subtype != -1 || !slices.Equal(data, []int{331}) ||
		s.scalar("npc index", -1) != 331 || s.scalar("complete npc index", -1) != -1 ||
		!questionGuide.MatchString(s.section("go guide")) || len(blocks) != 1 || !s.closed("question info") {
		s.fail("unsupported quest question")
		return 0
	}
	rows, ok := matchedRows(blocks[0], stringRE)
	if !ok || len(rows) != expected+2 {
		s.fail("invalid quest question options")
	}
	return byte(expected)
}

var townGuide = regexp.MustCompile("^(\\d+)\\s+(\\d+)\\s+\\d+\\s+\\d+\\s+\x60\\[bottom\\]\x60$")
var npcGuide = regexp.MustCompile("^(\\d+)\\s+(\\d+)\\s+[1-9]\\d*\\s+-1\\s+\x60\\[bottom\\]\x60$")

func (s *script) objective(q *questwire.Quest, grade string) {
	id, kind, sub, data := q.Id, q.Kind, q.Subtype, q.Data
	switch kind {
	case duelTraining:
		expected := []int{1, 1, 1, 1}
		if id == 2672 {
			expected[3] = 0
		} else if id == 2673 {
			expected = []int{1, 0, 0, 2}
		}
		if grade != "side" || sub != -1 || !slices.Equal(data, expected) || s.scalar("npc index", 0) != 323 ||
			s.scalar("complete npc index", 0) != -1 || !slices.Equal(s.data("pre required quest"), []int{int(id) - 1}) ||
			!slices.Equal(s.data("level"), []int{50, 10000}) {
			s.fail("unsupported training quest")
		}
	case communicator:
		if id != 12122 || grade != "epic" || sub != 1 || !slices.Equal(data, []int{1, 10312154, 1, 100000198}) ||
			s.scalar("npc index", -1) != 100000301 || s.scalar("complete npc index", -1) != 100000198 ||
			!communicatorGuide.MatchString(s.section("go guide")) {
			s.fail("unsupported communicator quest")
		}
	case question:
		q.QuestionChoices = s.question(id, grade, sub, data)
	case meetNPC:
		scroll := id == 12313 && sub == 1 && grade == "achievement" && s.scalar("job change quest", 0) == 4 &&
			slices.Equal(data, []int{100000263}) && s.scalar("npc index", 0) == 100000263 &&
			slices.Equal(s.data("pre required quest"), []int{12301}) && slices.Equal(s.data("casecade clear"), []int{6413}) &&
			slices.Equal(s.data("level"), []int{100, 10000}) && s.tag("reward type") == "item" &&
			slices.Equal(s.data("reward int data"), []int{10313702, 1})
		oculus := id == 6225 && sub == 1 && grade == "episode" && slices.Equal(data, []int{100000320}) && s.scalar("npc index", 0) == 100000327 &&
			s.scalar("complete npc index", 0) == 100000320 && slices.Equal(s.data("pre required quest"), []int{12167}) &&
			slices.Equal(s.data("level"), []int{100, 10000}) && s.tag("reward type") == "item" &&
			slices.Equal(s.data("reward int data"), []int{0, 0})
		if sub != -1 && !scroll && !oculus || len(data) != 1 || data[0] <= 0 {
			s.fail("invalid meet-NPC objective")
		}
	case clearMap:
		if sub != -1 || data[0] <= 0 || !(len(data) == 1 || len(data) == 2 && data[1] == -1) {
			s.fail("invalid clear-map objective")
		}
	case huntEnemy, huntMonster:
		width := 4
		if kind == huntEnemy {
			width = 5
		}
		if sub != -1 || len(data)%width != 0 || len(data)/width < 1 || len(data)/width > 3 {
			s.fail("invalid hunt objective shape")
			return
		}
		for row := range slices.Chunk(data, width) {
			if row[0] != -1 && row[0] <= 0 || row[1] < -1 || row[1] > 4 ||
				row[2] <= 0 && !(kind == huntMonster && row[2] == -5) || row[width-1] < 1 || row[width-1] > 511 ||
				width == 5 && !anyOf(row[3], 1, 2, 3) {
				s.fail("invalid hunt objective")
			}
		}
	case seeking, seekAndMeet:
		length := len(data)
		if kind == seekAndMeet {
			length--
		}
		if kind == seeking && !anyOf(sub, -1, 0) || kind == seekAndMeet && sub != -1 ||
			length < 2 || length%2 != 0 || length/2 > 32 {
			s.fail("invalid collection objective shape")
			return
		}
		for row := range slices.Chunk(data[:length], 2) {
			template, count := row[0], row[1]
			switch {
			case template < 0 || count <= 0 || template == 0 && q.GoldRequirement != 0:
				s.fail("invalid collection requirement")
			case template == 0:
				q.GoldRequirement = uint32(count)
			default:
				q.Materials = append(q.Materials, questwire.Material{Template: uint32(template), Count: uint32(count)})
			}
		}
	case questClear:
		q.Children = s.children(sub, data)
	case reachRange:
		if sub == 0 {
			child := grade == "sub" &&
				(id == 3759 && slices.Equal(data, []int{78, 50, 50}) && s.scalar("main quest", 0) == 3698 ||
					id == 3832 && slices.Equal(data, []int{344, 200, 200}) && s.scalar("main quest", 0) == 3830)
			if !(grade == "epic" && slices.Equal(data, npcRangeObjectives[id]) || child) {
				s.fail("unconfirmed NPC range objective")
			}
			return
		}
		if sub != 1 || len(data) != 6 || data[0] <= 0 || data[1] < 0 || data[2] < 0 || data[3] < 0 || data[4] <= 0 || data[5] <= 0 {
			s.fail("invalid town rectangle objective")
		}
	case clearCondition:
		if !(sub == 6 && len(data) == 2 && data[0] > 0 && data[1] == -1 ||
			sub == 11 && len(data) == 3 && data[0] > 0 && data[1] == -1 && (data[2] == -1 || data[2] > 0 && data[2] <= 511)) {
			s.fail("unsupported dungeon clear condition")
		}
	case cinematic:
		mainline := id == 3817 && grade == "sub" && s.scalar("main quest", 0) == 3494 && slices.Equal(data, []int{2123})
		if sub != -1 || grade != "epic" && !mainline || len(data) != 1 || data[0] <= 0 {
			s.fail("invalid cinematic objective")
			return
		}
		area, npc := s.section("go area guide"), s.section("go guide")
		if area == "" && npc == "" {
			return
		}
		var guide []string
		if area != "" && npc == "" {
			guide = townGuide.FindStringSubmatch(area)
		} else if area == "" {
			guide = npcGuide.FindStringSubmatch(npc)
		}
		if len(guide) != 3 {
			s.fail("unsupported cinematic guide")
			return
		}
		town, townErr := strconv.ParseUint(guide[1], 10, 16)
		areaID, areaErr := strconv.ParseUint(guide[2], 10, 16)
		if townErr != nil || areaErr != nil || town == 0 {
			s.fail("invalid cinematic town and area")
		}
		autoAccept := s.section("auto accept")
		if autoAccept != "" && !regexp.MustCompile("^\x60enter town\x60\\s+"+strconv.FormatUint(town, 10)+"$").MatchString(autoAccept) {
			s.fail("cinematic and auto-accept destinations conflict")
		}
		q.TownCinematicTarget = &townMovie{uint32(data[0]), uint16(town), uint16(areaID)}
	}
}

var npcRangeObjectives = map[uint16][]int{
	3162: {13, 350, 250}, 3224: {15, 100, 100}, 3672: {15, 50, 50}, 3281: {39, 250, 250},
	6301: {100000091, 100, 100}, 12302: {100000374, 2000, 2000}, 12312: {100000374, 2000, 2000}, 12314: {100000374, 2000, 2000},
}

func (s *script) destinations(q *questwire.Quest, grade string) {
	allowDifficulty := anyOf(q.Kind, seeking, huntEnemy, huntMonster) && anyOf(grade, "epic", "side", "mission", "achievement", "daily mission")
	var first []int
	for i, block := range s.blocks("dungeon info") {
		values := s.numbers(block)
		if i != 0 && !slices.Equal(first, values) {
			s.fail("conflicting quest dungeon destinations")
		}
		first = values
	}
	if len(first)%2 != 0 {
		s.fail("incomplete quest dungeon destination")
		return
	}
	for row := range slices.Chunk(first, 2) {
		if row[0] <= 0 || row[1] < -1 || row[1] > 4 || !allowDifficulty && row[1] != -1 {
			s.fail("unsupported quest dungeon or difficulty")
		}
		q.DestinationDungeons = append(q.DestinationDungeons, row[0])
		q.DestinationRequirements = append(q.DestinationRequirements, destinationRequirement{row[0], row[1]})
	}
	q.DestinationDungeons = distinct(q.DestinationDungeons)
	q.DestinationRequirements = distinct(q.DestinationRequirements)
	if q.Kind == cinematic && (grade == "sub" && (!slices.Equal(q.DestinationDungeons, []int{163}) || q.TownCinematicTarget != nil) ||
		(q.TownCinematicTarget != nil) == (len(q.DestinationDungeons) != 0)) {
		s.fail("invalid cinematic scene binding")
	}
}

func (s *script) rewards(q *questwire.Quest, grade, reward string) {
	text := s.section("reward int data")
	switch reward {
	case "awakening type":
		q.AwakeningAward = s.awakening(q.Criterion, grade, q.Kind, false)
	case "item":
		if q.FateIntroduction && slices.Equal(s.numbers(text), []int{0, 100}) {
			q.GoldAward = 100
		} else {
			q.Rewards, q.RewardRows, _ = s.items(text, false, false)
		}
	case "tp":
		if q.Id != 2674 || q.Kind != duelTraining || text != "" || !slices.Equal(s.data("sub reward"), []int{trainingManual}) {
			s.fail("unsupported quest TP reward")
		}
		q.TpAward = 2
		q.Materials = []questwire.Material{{Template: trainingManual, Count: 1}}
	case "slot expansion":
		args := s.numbers(text)
		if q.Kind != seeking || q.Subtype != -1 || grade != "side" || len(args) != 1 || args[0] < 0 || args[0] > 2 ||
			len(q.Materials) == 0 || q.GoldRequirement == 0 || !anyOf(s.tag("attribute"), "slot expansion", "magic stone") {
			s.fail("unsupported slot expansion reward")
		}
	default:
		empty := reward == "" && grade == "achievement" && q.Kind == seeking && q.AdvancementChain == 2 && slices.Equal(s.numbers(text), []int{0, 0})
		if !empty && (reward != "" || text != "") {
			s.fail("unsupported quest reward type")
		}
	}
	blocks := s.blocks("reward selection int data")
	if len(blocks) > 1 {
		s.fail("duplicate selectable quest rewards")
	}
	if len(blocks) == 1 && blocks[0] != "" {
		if !s.closed("reward selection int data") || q.Id != 6405 || grade != "side" || q.Kind != seeking || reward != "item" ||
			q.Parent != 0 || len(q.Children) != 0 || q.AdvancementChain != 0 || q.AwakeningAward != nil || len(s.data("casecade clear")) != 0 {
			s.fail("unsupported selectable rewards")
		}
		_, q.SelectionRewardRows, _ = s.items(blocks[0], true, false)
		if len(q.SelectionRewardRows) == 0 || len(q.SelectionRewardRows) > 65535 {
			s.fail("invalid selectable reward count")
		}
	}
}

func (s *script) acceptanceItems(q *questwire.Quest) []questwire.Material {
	items := make([]questwire.Material, 0)
	blocks := s.blocks("depend give item")
	if len(blocks) == 0 {
		return items
	}
	if len(blocks) != 1 || !s.closed("depend give item") {
		s.fail("invalid acceptance item declaration")
		return items
	}
	data := s.numbers(blocks[0])
	if len(data) == 0 {
		return items
	}
	if q.Kind != seeking || len(data)%2 != 0 || len(data) > 62 {
		s.fail("invalid acceptance item data")
		return items
	}
	for row := range slices.Chunk(data, 2) {
		id, count := uint32(row[0]), uint32(row[1])
		if row[0] <= 0 || row[1] <= 0 ||
			!slices.ContainsFunc(q.Materials, func(m questwire.Material) bool { return m.Template == id && m.Count >= count }) ||
			slices.ContainsFunc(items, func(m questwire.Material) bool { return m.Template == id }) {
			s.fail("invalid acceptance item requirement")
			continue
		}
		items = append(items, questwire.Material{Template: id, Count: count})
	}
	return items
}
