package quest

import (
	"fmt"
	"math"
	"regexp"
	"strconv"
	"strings"
)

type experienceTable struct {
	base       []int
	difficulty map[string]int64
	penalties  [2][2][]int
	basic      int
}

var experienceRow = regexp.MustCompile("\\s*\x60([^\x60]+)\x60\\s+(-?\\d+)\\s*")
var ignoreQuestLevel = regexp.MustCompile("(?im)^\\s*\\[ignore quest level 4 exp\\]")

func parseExperienceTable(text string) (experienceTable, error) {
	s := script{text: text}
	result := experienceTable{difficulty: make(map[string]int64)}
	rates, complete := matchedRows(s.section("difficulty"), experienceRow)
	if !complete || len(rates) == 0 {
		s.fail("incomplete quest experience difficulty rates")
	}
	for _, row := range rates {
		rate, err := strconv.ParseInt(row[1], 10, 64)
		key := strings.ToLower(row[0])
		_, duplicate := result.difficulty[key]
		if err != nil || rate < -1000 || duplicate {
			s.fail("invalid quest experience rate")
		}
		result.difficulty[key] = rate
	}
	args := s.data("exp reward table")
	if len(args) == 0 || len(args)%2 != 0 {
		s.fail("incomplete quest experience table")
	}
	for i, value := range args {
		if i%2 == 0 {
			if value < 0 {
				s.fail("negative quest experience base")
			}
			result.base = append(result.base, value)
		} else if value != -1 {
			s.fail("invalid quest experience level separator")
		}
	}
	for kind, prefix := range []string{"", "epic "} {
		for i, name := range []string{"green", "grey"} {
			row := s.data(prefix + name + " level penalty")
			if len(row) != 3 || row[0] > row[1] || row[2] < 0 {
				s.fail("invalid quest experience level penalty")
			}
			result.penalties[kind][i] = row
		}
	}
	result.basic = s.scalar("basic level penalty", 100)
	if result.basic < 0 {
		return result, fmt.Errorf("negative quest experience penalty")
	}
	return result, s.err
}

func (e experienceTable) rewards(qText string, fate bool) ([]*uint32, error) {
	s := script{text: qText}
	out := make([]*uint32, 100)
	if fate {
		for i := range out {
			value := uint32(0)
			out[i] = &value
		}
		return out, nil
	}
	questLevel, rates := 1, 0
	if values := s.data("level"); len(values) != 0 {
		questLevel = values[0]
	}
	if s.tag("grade") == "epic" {
		rates = 1
	}
	difficulty := s.tag("difficulty")
	bonus, known := e.difficulty[difficulty]
	ignoreLevel := ignoreQuestLevel.MatchString(qText)
	noExp := strings.Contains(strings.ToLower(s.section("attribute")), "not give exp quest")
	if s.err != nil {
		return nil, s.err
	}
	for i := range out {
		player, level := i+1, questLevel
		penalty := e.basic
		if ignoreLevel {
			level, penalty = player, 100
		} else {
			difference := player - level
			for _, row := range e.penalties[rates] {
				if difference >= row[0] && difference <= row[1] {
					penalty = row[2]
					break
				}
			}
		}
		if noExp {
			value := uint32(0)
			out[i] = &value
			continue
		}
		if !known || level < 1 || level >= len(e.base) || bonus > math.MaxInt64-1000 {
			continue
		}
		base, rate := int64(e.base[level]), 1000+bonus
		if rate > 0 && base > math.MaxInt64/rate {
			return nil, fmt.Errorf("quest experience rate exceeds integer range")
		}
		value := base * rate / 1000
		if penalty > 0 && value > math.MaxInt64/int64(penalty) {
			return nil, fmt.Errorf("quest experience penalty exceeds integer range")
		}
		value = value * int64(penalty) / 100
		if value < 0 || value > math.MaxUint32 {
			return nil, fmt.Errorf("quest experience award exceeds uint32")
		}
		row := uint32(value)
		out[i] = &row
	}
	return out, nil
}
