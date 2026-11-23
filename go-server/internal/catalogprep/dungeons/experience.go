// Package dungeons prepares dungeon resource data from the original PVF.
package dungeons

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"math"
	"math/big"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"s9a1/server/internal/catalogprep/pvftext"
	"s9a1/server/internal/dungeon"
	"s9a1/server/internal/opening"
)

const budget = 1 << 20

type Archive interface {
	Read(string, int) ([]byte, error)
	ReadText(string, int) (string, error)
	ReadDungeonMapText(string, int) (string, error)
	ReadIndex(string, ...int) (map[int64]string, error)
	ResolveString(int32) (string, error)
}

type Experience struct {
	Hash            string
	Clear           dungeon.ClearExperienceData
	Rank            dungeon.RankExperienceData
	levels          map[uint32]uint32
	monsterRate     *big.Rat
	difficultyRates []*big.Rat
}

var tableHeader = regexp.MustCompile(`(?m)^\s*#PVF_File\s*$`)

func BuildExperience(a Archive) (*Experience, error) {
	text, err := a.ReadText("monster/monsterexp.tbl", budget)
	if err != nil {
		return nil, err
	}
	parameters, err := a.ReadText("etc/(r)serverparameter.etc", budget)
	if err != nil {
		return nil, err
	}
	rankText, err := a.ReadText("etc/ranksysteminfo.etc", budget)
	if err != nil {
		return nil, err
	}
	fields := strings.Fields(tableHeader.ReplaceAllString(text, ""))
	if len(fields) < 2 || len(fields)%2 != 0 {
		return nil, fmt.Errorf("incomplete PVF monster experience table")
	}
	e := &Experience{levels: make(map[uint32]uint32, len(fields)/2)}
	for i := 0; i < len(fields); i += 2 {
		level, le := strconv.ParseUint(fields[i], 10, 16)
		amount, ae := strconv.ParseUint(fields[i+1], 10, 32)
		if le != nil || ae != nil || level == 0 || amount == 0 || e.levels[uint32(level)] != 0 {
			return nil, fmt.Errorf("invalid or repeated PVF monster experience level %q", fields[i])
		}
		e.levels[uint32(level)] = uint32(amount)
	}
	_, clear, err := decimalRates(parameters, "clear exp bonusrate", 1, 1)
	if err != nil {
		return nil, err
	}
	rates, _, err := decimalRates(parameters, "monster exp bonusrate", 1, 1)
	if err != nil {
		return nil, err
	}
	e.monsterRate = rates[0]
	e.difficultyRates, _, err = decimalRates(parameters, "dungeon difficulty exp bonusrate", 1, 16)
	if err != nil {
		return nil, err
	}
	_, rankRates, err := decimalRates(parameters, "clear rank exp bonusrate", 5, 5)
	if err != nil {
		return nil, err
	}
	grades, err := pvftext.Numbers(rankText, "rank grade")
	if err != nil || len(grades) != 8 {
		return nil, fmt.Errorf("invalid PVF rank grades")
	}
	e.Hash = hashText("LocalDungeonExperience/PvfNamed3/v3\n" + text + "\n" + parameters)
	e.Clear = dungeon.ClearExperienceData{ResourceHash: hashText(parameters), Multiplier: clear[0]}
	e.Rank = dungeon.RankExperienceData{ResourceHash: hashText("ClientDelegatedRank/PvfTopFive/v1\n" + parameters + "\n" + rankText), Rates: rankRates}
	for i, value := range grades {
		if value < 0 || value > 100 || i > 0 && value >= grades[i-1] {
			return nil, fmt.Errorf("invalid PVF rank grade %d", value)
		}
		e.Rank.Grades = append(e.Rank.Grades, byte(value))
	}
	return e, nil
}

func hashText(text string) string { return fmt.Sprintf("%X", sha256.Sum256([]byte(text))) }

var decimalPattern = regexp.MustCompile(`^\d+(?:\.\d*)?$`)

func decimal(value string) (*big.Rat, bool) {
	if !decimalPattern.MatchString(value) {
		return nil, false
	}
	return new(big.Rat).SetString(value)
}

func decimalRates(text, tag string, lo, hi int) ([]*big.Rat, []json.Number, error) {
	body, err := pvftext.Section(text, tag)
	fields := strings.Fields(body)
	if err != nil || len(fields) < lo || len(fields) > hi {
		return nil, nil, fmt.Errorf("invalid PVF %s field", tag)
	}
	nums := make([]json.Number, len(fields))
	rates := make([]*big.Rat, len(fields))
	for i, field := range fields {
		rate, ok := decimal(field)
		if !ok || rate.Cmp(big.NewRat(1000, 1)) > 0 {
			return nil, nil, fmt.Errorf("invalid PVF %s value %q", tag, field)
		}
		rates[i], nums[i] = rate, json.Number(field)
	}
	return rates, nums, nil
}

type ExperienceProfile struct {
	Multiplier    json.Number
	Ordinary      bool
	StoryPercents []uint32
	Named         []uint32
}

func (e *Experience) multiplier(profile ExperienceProfile, difficulty byte) (*big.Rat, error) {
	value, ok := decimal(string(profile.Multiplier))
	if !ok {
		return nil, fmt.Errorf("invalid native dungeon experience multiplier")
	}
	if !profile.Ordinary {
		return value, nil
	}
	if int(difficulty) >= len(e.difficultyRates) || len(profile.StoryPercents) != 0 && int(difficulty) >= len(profile.StoryPercents) {
		return nil, fmt.Errorf("dungeon experience difficulty %d has no source rate", difficulty)
	}
	value.Mul(value, e.monsterRate)
	value.Mul(value, e.difficultyRates[difficulty])
	if len(profile.StoryPercents) != 0 {
		value.Mul(value, big.NewRat(100+int64(profile.StoryPercents[difficulty]), 100))
	}
	return value, nil
}

type Award = struct {
	Level, Type   byte
	ByPlayerLevel []uint32
}

func (e *Experience) reward(level, player, rank byte, scale *big.Rat) (uint32, error) {
	if scale.Sign() == 0 {
		return 0, nil
	}
	basis := e.levels[uint32(level)]
	if basis == 0 || player == 0 || rank > 3 {
		return 0, fmt.Errorf("invalid monster/player experience inputs %d/%d type %d", level, player, rank)
	}
	penalty := int64(5)
	switch diff := int(level) - int(player); {
	case diff == -6:
		penalty = 20
	case diff == -5:
		penalty = 50
	case diff == -4:
		penalty = 75
	case diff >= -3 && diff <= 0:
		penalty = 100
	case diff >= 1 && diff <= 3:
		penalty = 112
	case diff == 4 || diff == 5:
		penalty = 100
	case diff == 6:
		penalty = 75
	case diff == 7:
		penalty = 70
	case diff == 8:
		penalty = 60
	case diff == 9:
		penalty = 50
	}
	raw := new(big.Rat).Mul(scale, big.NewRat(int64(basis)*penalty, 100))
	amount := new(big.Int).Quo(raw.Num(), raw.Denom())
	if !amount.IsUint64() {
		return math.MaxUint32, nil
	}
	normal := max(1, min(math.MaxUint32, amount.Uint64()))
	return uint32(min(math.MaxUint32, normal*uint64(rank+1))), nil
}

func (e *Experience) OpeningAwards(row Opening) ([]Award, error) {
	rooms := append([]opening.Room{row.Dungeon.Start}, row.Dungeon.Rooms...)
	samples := make([]opening.Monster, 0)
	for _, r := range rooms {
		samples = append(samples, r.Monsters...)
	}
	slices.SortStableFunc(samples, func(a, b opening.Monster) int {
		if a.Level == b.Level {
			return int(a.Type) - int(b.Type)
		}
		return int(a.Level) - int(b.Level)
	})
	return e.awards(ExperienceProfile{Multiplier: row.ExperienceMultiplier}, row.Dungeon.Difficulty, samples)
}

func (e *Experience) awards(p ExperienceProfile, difficulty byte, samples []opening.Monster) ([]Award, error) {
	scale, err := e.multiplier(p, difficulty)
	if err != nil {
		return nil, err
	}
	seen := map[[2]byte]bool{}
	out := []Award{}
	for _, m := range samples {
		typ := m.Type
		if p.Ordinary && typ < 3 && !m.Dummy && m.AiScriptIndex == nil &&
			(m.Code >= 50000 && m.Code <= 51000 || m.Code >= 56000 && m.Code <= 60000 || slices.Contains(p.Named, m.Code)) {
			typ = 2
		}
		key := [2]byte{m.Level, typ}
		if seen[key] {
			continue
		}
		seen[key] = true
		row := Award{Level: m.Level, Type: typ, ByPlayerLevel: make([]uint32, 100)}
		for player := byte(1); player <= 100; player++ {
			row.ByPlayerLevel[player-1], err = e.reward(m.Level, player, typ, scale)
			if err != nil {
				return nil, err
			}
		}
		out = append(out, row)
	}
	return out, nil
}
