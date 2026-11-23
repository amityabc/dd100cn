package quest

import (
	"math"
	"regexp"
	"slices"
	"strconv"

	"s9a1/server/internal/questwire"
)

type skillEffect = struct {
	Job                       byte
	Id                        uint16
	SpDiscount, MaximumRefund bool
}

var effectRE = regexp.MustCompile("\\s*(\\d+)\\s+(\\d+)\\s+\x60(sp discount|maximum refund)\x60\\s*")

func (s *script) skillEffects() []skillEffect {
	var result []skillEffect
	for _, block := range s.blocks("check skill on clear") {
		rows, complete := matchedRows(block, effectRE)
		if !complete || len(rows) == 0 {
			s.fail("invalid quest skill effects")
		}
		for _, row := range rows {
			job, jobErr := strconv.ParseUint(row[0], 10, 8)
			id, idErr := strconv.ParseUint(row[1], 10, 16)
			if jobErr != nil || idErr != nil || job > 15 || id == 0 || slices.ContainsFunc(result, func(value skillEffect) bool {
				return value.Job == byte(job) && value.Id == uint16(id)
			}) {
				s.fail("invalid quest skill effect target")
			}
			discount := row[2] == "sp discount"
			result = append(result, skillEffect{byte(job), uint16(id), discount, !discount})
		}
	}
	return result
}

var rewardRE = regexp.MustCompile("\\s*(-?\\d+|\x60\\[job\\]\x60)\\s*")

func (s *script) items(text string, selection, ticket bool) ([]questwire.Material, []questwire.RewardRow, uint32) {
	tokens, ok := matchedRows(text, rewardRE)
	if !ok {
		s.fail("unsupported quest item reward syntax")
	}
	var plain []questwire.Material
	var rows []questwire.RewardRow
	var gold int64
	at := 0
	number := func() int64 {
		if at == len(tokens) {
			s.fail("incomplete quest item reward")
			return 0
		}
		v, err := strconv.ParseInt(tokens[at][0], 10, 64)
		at++
		if err != nil {
			s.fail("invalid quest reward number")
		}
		return v
	}
	for at < len(tokens) {
		template := number()
		job, grow := int64(-1), int64(-1)
		if at < len(tokens) && tokens[at][0] == "\x60[job]\x60" {
			at++
			job, grow = number(), number()
			if job < 0 || job > 15 || grow < -1 || grow > 5 {
				s.fail("invalid quest reward job restriction")
			}
		}
		count := number()
		if template == 0 && job == -1 && !selection && (ticket && count >= 0 && count <= math.MaxUint32 || count == 0) {
			gold += count
			if gold > math.MaxUint32 {
				s.fail("quest gold reward exceeds uint32")
			}
			continue
		}
		if template <= 0 || template > math.MaxUint32 || count <= 0 || count > math.MaxUint32 {
			s.fail("invalid quest item reward")
			continue
		}
		item := questwire.Material{Template: uint32(template), Count: uint32(count)}
		if job == -1 {
			plain = append(plain, item)
		}
		rows = append(rows, questwire.RewardRow{Item: item, Job: int(job), Grow: int(grow)})
	}
	return plain, rows, uint32(gold)
}

func (s *script) awakening(c questwire.Criterion, grade string, kind int, ticket bool) *questwire.Award {
	args := s.data("reward int data")
	chain := c.AdvancementChain
	if len(args) != 1 || args[0] < 1 || args[0] > 3 || chain != 0 && chain != args[0]+1 ||
		!ticket && (!anyOf(grade, "epic", "achievement", "realization") || !anyOf(kind, clearMap, seeking, meetNPC) || len(c.NonTargets) != 0) {
		s.fail("unsupported awakening reward")
		return nil
	}
	stage := byte(args[0])
	if chain == 0 {
		if stage != 1 || c.Job != -1 || c.Grow != -1 || len(c.Targets) != 1 ||
			!anyOf(c.Targets[0].Job, 9, 10) || c.Targets[0].Grow != 0 || c.Targets[0].Awakening != -1 ||
			!ticket && (grade != "epic" || kind != clearMap) {
			s.fail("unsupported unchained awakening reward")
			return nil
		}
		return &questwire.Award{Job: c.Targets[0].Job, Grow: 0, Stage: stage}
	}
	if len(c.Targets) == 0 && (c.Job < 0 || stage < 3 && c.Grow < 0 && !anyOf(c.Job, 9, 10)) {
		s.fail("missing awakening qualification")
	}
	return &questwire.Award{Job: c.Job, Grow: c.Grow, Stage: stage}
}

var dropQuests = []uint16{3229, 3233, 3284, 3291, 3293, 3297, 3453, 3459, 3481, 3489, 3524, 3545, 3594, 3597, 3632, 3635, 3661, 3668, 3669, 3710, 3735, 3747, 6925, 12184, 12191, 12192, 12193, 12194, 12195, 13607, 13611, 13612, 5651, 5652, 5653, 5654, 6053, 6056, 6059, 7342, 6394, 6405, 7921, 7989, 7993, 2683, 2901, 2902, 4099, 4100, 8714, 10019}

func (s *script) dropRules(q *questwire.Quest, grade string) []questwire.QuestDropRule {
	var drops []questwire.QuestDropRule
	for _, tag := range []string{"monster reward item", "enemy reward item"} {
		blocks := s.blocks(tag)
		if len(blocks) == 0 {
			continue
		}
		if !slices.Contains(dropQuests, q.Id) && grade != "achievement" || !anyOf(grade, "epic", "side", "mission", "achievement", "daily mission") ||
			q.Kind != seeking || len(blocks) != 1 || !s.closed(tag) {
			s.fail("unsupported quest collection drop owner")
			continue
		}
		rows := s.numbers(blocks[0])
		width := 7
		if tag == "enemy reward item" {
			width = 8
		}
		if len(rows) == 0 || len(rows)%width != 0 || len(rows)/width > 64 {
			s.fail("incomplete quest collection drop rows")
			continue
		}
		for row := range slices.Chunk(rows, width) {
			enemy := 1
			if width == 8 {
				enemy = row[1]
			}
			args := row[width-6:]
			dungeon, difficulty, template, count, rate, held := args[0], args[1], args[2], args[3], args[4], args[5]
			if row[0] <= 0 || enemy < 1 || enemy > 3 || dungeon < -1 || dungeon == 0 ||
				dungeon == -1 && len(q.DestinationDungeons) == 0 ||
				dungeon > 0 && len(q.DestinationDungeons) > 0 && !slices.Contains(q.DestinationDungeons, dungeon) ||
				difficulty < -1 || difficulty > 4 || template <= 0 || count < 1 || count > 100 || rate < 0 || rate > 100 || held < -1 ||
				!slices.ContainsFunc(q.Materials, func(m questwire.Material) bool { return int(m.Template) == template }) {
				s.fail("invalid quest collection drop rule")
				continue
			}
			drops = append(drops, questwire.QuestDropRule{Dungeon: dungeon, Difficulty: difficulty, Template: uint32(template), Count: uint32(count), Code: uint32(row[0]), EnemyKind: byte(enemy), Rate: byte(rate), MaximumHeld: held})
		}
	}
	if len(drops) > 64 {
		s.fail("quest collection drops exceed native limit")
	}
	return drops
}
