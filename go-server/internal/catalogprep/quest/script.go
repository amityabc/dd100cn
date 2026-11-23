package quest

import (
	"crypto/sha256"
	"fmt"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"s9a1/server/internal/catalogprep/pvftext"
	"s9a1/server/internal/questwire"
)

type script struct {
	text string
	err  error
}

func (s *script) fail(problem string) {
	if s.err == nil {
		s.err = fmt.Errorf("%s", problem)
	}
}

func (s *script) blocks(tag string) []string { return pvftext.Blocks(s.text, tag) }

func (s *script) section(tag string) string {
	result, err := pvftext.Section(s.text, tag)
	if err != nil {
		s.fail(err.Error())
	}
	return result
}

func tagValue(value string) string {
	return strings.ToLower(strings.Trim(value, " \t\r\n\x60[]"))
}

func (s *script) tag(tag string) string { return tagValue(s.section(tag)) }

func (s *script) numbers(text string) []int {
	result, err := pvftext.Integers(text)
	if err != nil {
		s.fail(err.Error())
	}
	return result
}

func (s *script) data(tag string) []int { return s.numbers(s.section(tag)) }

func (s *script) scalar(tag string, fallback int) int {
	values := s.data(tag)
	switch len(values) {
	case 0:
		return fallback
	case 1:
		return values[0]
	default:
		s.fail("invalid scalar " + tag)
		return fallback
	}
}

func (s *script) closed(tag string) bool {
	pattern := regexp.MustCompile("(?im)^\\[/" + regexp.QuoteMeta(tag) + "\\]\\s*$")
	return len(pattern.FindAllStringIndex(s.text, -1)) == 1
}

func hash(text string) string { return fmt.Sprintf("%X", sha256.Sum256([]byte(text))) }

func anyOf[T comparable](value T, values ...T) bool { return slices.Contains(values, value) }

func distinct[T comparable](values []T) []T {
	seen := make(map[T]bool, len(values))
	out := make([]T, 0, len(values))
	for _, n := range values {
		if !seen[n] {
			out = append(out, n)
			seen[n] = true
		}
	}
	return out
}

func matchedRows(text string, regex *regexp.Regexp) ([][]string, bool) {
	var rows [][]string
	offset := 0
	for _, match := range regex.FindAllStringSubmatchIndex(text, -1) {
		if match[0] != offset {
			return nil, false
		}
		row := make([]string, len(match)/2-1)
		for i := range row {
			a, b := match[i*2+2], match[i*2+3]
			if a >= 0 {
				row[i] = text[a:b]
			}
		}
		rows = append(rows, row)
		offset = match[1]
	}
	return rows, offset == len(text)
}

var (
	jobNames = []string{"swordman", "fighter", "gunner", "mage", "priest", "at gunner", "thief", "at fighter", "at mage", "demonic swordman", "creator mage", "at swordman", "knight", "demonic lancer", "at priest", "gun blader"}
	targetRE = regexp.MustCompile("\\s*\x60\\[([^\\]]+)\\]\x60\\s+(-?\\d+)\\s+(-?\\d+)\\s*")
	stringRE = regexp.MustCompile("\\s*\x60([^\x60]+)\x60\\s*")
)

func (s *script) job(value string) int {
	value = tagValue(value)
	n := slices.Index(jobNames, value)
	if n < 0 && value != "" && value != "all" {
		s.fail("unknown quest job")
	}
	return n
}

func (s *script) id(n int) uint16 {
	if n <= 0 || n >= 30000 {
		s.fail("quest dependency out of range")
		return 0
	}
	return uint16(n)
}

func (s *script) ids(values []int) []uint16 {
	ids := make([]uint16, len(values))
	for i, value := range values {
		ids[i] = s.id(value)
	}
	return ids
}

func (s *script) levels() (byte, byte) {
	values := s.data("level")
	low, high := 1, 255
	if len(values) > 0 {
		low = values[0]
	}
	if len(values) > 1 {
		high = min(high, values[1])
	}
	if len(values) > 2 || low <= 0 || low > high {
		s.fail("invalid quest levels")
		return 1, 255
	}
	return byte(low), byte(high)
}

func (s *script) eligibility(id uint16, rawHash string, strictParent bool) questwire.Criterion {
	q := questwire.Criterion{Id: id, Job: -1, Grow: s.scalar("grow type", -1), GrowNumber: s.scalar("grow number", -1)}
	q.MinimumLevel, q.MaximumLevel = s.levels()
	for i, block := range s.blocks("job") {
		job := s.job(block)
		if i != 0 && q.Job != job {
			s.fail("conflicting quest jobs")
		}
		q.Job = job
	}
	for i, block := range s.blocks("main quest") {
		parent := s.numbers(block)
		if len(parent) != 1 {
			s.fail("malformed quest parent")
			continue
		}
		if strictParent || parent[0] != 0 {
			value := s.id(parent[0])
			if i != 0 && q.Parent != value {
				s.fail("conflicting quest parents")
			}
			q.Parent = value
		} else if i != 0 && q.Parent != 0 {
			s.fail("conflicting quest parents")
		}
	}
	for _, block := range s.blocks("pre required quest") {
		q.Required = append(q.Required, s.ids(s.numbers(block)))
	}
	for _, block := range s.blocks("pre required quest answer") {
		row := s.numbers(block)
		if len(row) == 0 || len(row)%2 != 0 {
			s.fail("invalid quest answer prerequisite")
			continue
		}
		for part := range slices.Chunk(row, 2) {
			if part[1] < 0 || part[1] >= 255 {
				s.fail("invalid quest answer")
			}
			q.Answers = append(q.Answers, struct {
				Quest  uint16
				Answer byte
			}{s.id(part[0]), byte(part[1])})
		}
	}
	for _, block := range s.blocks("collision quest") {
		q.Collisions = append(q.Collisions, s.ids(s.numbers(block))...)
	}
	q.Targets = s.targets("target character")
	if continuationHashes[id] == rawHash {
		var targets []questwire.Target
		for _, t := range q.Targets {
			targets = append(targets, t)
			if t.Awakening == 1 {
				t.Awakening = 2
				targets = append(targets, t)
				t.Awakening = 3
				targets = append(targets, t)
			}
		}
		q.Targets = targets
	}
	q.NonTargets = s.targets("non target character")
	return q
}

func (s *script) targets(section string) []questwire.Target {
	var result []questwire.Target
	for _, block := range s.blocks(section) {
		rows, complete := matchedRows(block, targetRE)
		if !complete || len(rows) == 0 {
			s.fail("invalid target character tuple")
		}
		for _, row := range rows {
			grow, err1 := strconv.Atoi(row[1])
			awakening, err2 := strconv.Atoi(row[2])
			if err1 != nil || err2 != nil || grow < -1 || grow > 5 || awakening < -1 || awakening > 3 {
				s.fail("invalid target character qualification")
			}
			result = append(result, questwire.Target{Job: s.job(row[0]), Grow: grow, Awakening: awakening})
		}
	}
	return result
}

func (s *script) children(subtype int, data []int) []uint16 {
	children := s.ids(data)
	if subtype != 2 || len(data) == 0 || len(data) > 511 || len(distinct(children)) != len(children) {
		s.fail("invalid linked quest children")
	}
	return children
}

func legacyUpper(id uint16, rawHash string) bool {
	return id == 6413 && rawHash == "021CA57975AC6EF09EC801B313E1C453E95A3B045130A3263E1342267610AF98"
}

var continuationHashes = map[uint16]string{
	3357: "247CCA0569692275E973998BE5B015359673F8D42837394D20A3B8DDD8FF9C3B",
	3362: "624986153409ADC6055CB264D98459B7658933D7EF119202973F5D1C49978AA9",
	3367: "A623ED2A7F8F87A57E69B4602B3801F34F6815197F1333753B3FFE25796BED42",
	3372: "B1BB3AC204F536E151E10FC30EE0173498841F4601780C428EFB4C12231A4014",
	3377: "C42ED53CFD781B2A5BA049EFBC7EF884BDFE6B8208CF4FCA4F13A3458D07FAB8",
	3382: "158C35FF8F68F8703460A14CD42E31A71C90070649A2E24E9656BA2D7F45BB2B",
	3387: "3E4A57EFD509FEB76694A55E71C8A758896CB443FAC60F1E1581A6AEC1198053",
	3392: "660DD67024ECE54C099180CBC6C3A641D663B21DE9E5F83C9B8CA0999C47339C",
	3398: "EAE22500D202BB589EE93CC25A4B0A9E3782C4328D3DF4BF469C94E18221115D",
	3403: "0EF6B0FEF1C92B6663D0B51FAF6805C816FEA1D7651BF08252D5722D80BC17D3",
	3408: "0D8D984ABF097A7AE42114928A4696733C4593C36D778918A9F53156DE5D38EE",
	3413: "19C741CA90CE48DD517DF4DBDD1AB68323B27999953BC9ED0241C0C807B49F3F",
	3418: "17D18904DFF0138251583DCEFC75CFFBD74E192F424E0561161AF07B76E8A106",
}
