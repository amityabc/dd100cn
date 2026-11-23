package dungeons

import (
	"crypto/sha256"
	"fmt"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"s9a1/server/internal/catalogprep/pvftext"
)

type LuckBand struct{ Minimum, Maximum, PartsPer10000 int }
type LuckDeduction struct {
	MinimumLevel, MaximumLevel int
	PartsPer10000              []int
}
type Luck struct {
	Hash                                                         string
	PointsPerFatigue, PointsPerPlayInterval, PlayIntervalMinutes int
	Bands                                                        []LuckBand
	RareDeduction, UniqueDeduction                               []LuckDeduction
	GapPairs                                                     []int
}

const luckPolicy = "PVFLuckParameters/v1;native-upper-bound-rows;three-gap-lower-bounds;no-level-extrapolation;event-points-excluded"

var luckTag = regexp.MustCompile(`(?m)^\[([^\r\n\[\]]+)\][ \t]*\r?$`)

func BuildLuck(a Archive) (Luck, error) {
	const path = "etc/(r)serverparameter.etc"
	text, err := a.ReadText(path, budget)
	if err != nil {
		return Luck{}, err
	}
	raw, err := a.Read(path, budget)
	if err != nil {
		return Luck{}, err
	}
	tags := []string{"point per fatigue", "point per play time", "luck point", "luck point deduction rare", "luck point deduction unique", "luck point deduction gap"}
	rows := luckTag.FindAllStringSubmatchIndex(text, -1)
	values := map[string][]int{}
	for i, row := range rows {
		tag := text[row[2]:row[3]]
		if !slices.Contains(tags, tag) {
			continue
		}
		end := len(text)
		if i+1 < len(rows) {
			end = rows[i+1][0]
		}
		ns, err := pvftext.Integers(text[row[1]:end])
		if err != nil || len(ns) == 0 || len(ns) > 4096 || values[tag] != nil {
			return Luck{}, fmt.Errorf("invalid PVF luck section %s", tag)
		}
		values[tag] = ns
	}
	fatigue, time, bands, gap := values[tags[0]], values[tags[1]], values[tags[2]], values[tags[5]]
	if len(values) != len(tags) || len(fatigue) != 1 || fatigue[0] < 0 || len(time) != 2 || slices.Min(time) < 0 || len(bands)%3 != 0 || len(gap) != 8 {
		return Luck{}, fmt.Errorf("invalid PVF luck accrual or bounds")
	}
	result := Luck{PointsPerFatigue: fatigue[0], PointsPerPlayInterval: time[0], PlayIntervalMinutes: time[1], GapPairs: gap}
	prior := -1
	for i := 0; i < len(bands); i += 3 {
		if bands[i] < 0 || bands[i+1] < bands[i] || bands[i+1] <= prior || bands[i+2] < 0 {
			return Luck{}, fmt.Errorf("invalid PVF luck row interval")
		}
		result.Bands = append(result.Bands, LuckBand{bands[i], bands[i+1], bands[i+2]})
		prior = bands[i+1]
	}
	result.RareDeduction, err = deduction(values[tags[3]])
	if err != nil {
		return Luck{}, err
	}
	result.UniqueDeduction, err = deduction(values[tags[4]])
	if err != nil {
		return Luck{}, err
	}
	for i := 0; i < len(gap); i += 2 {
		if gap[i] < gap[i+1] || i > 0 && gap[i] >= gap[i-2] {
			return Luck{}, fmt.Errorf("invalid PVF luck gap order")
		}
	}
	identity := []string{luckPolicy, fmt.Sprintf("%X", sha256.Sum256(raw))}
	for _, tag := range tags {
		tokens := make([]string, len(values[tag]))
		for i, n := range values[tag] {
			tokens[i] = strconv.Itoa(n)
		}
		identity = append(identity, tag+":"+strings.Join(tokens, " "))
	}
	result.Hash = hashText(strings.Join(identity, "\n"))
	return result, nil
}

func deduction(rows []int) ([]LuckDeduction, error) {
	if len(rows)%6 != 0 {
		return nil, fmt.Errorf("invalid PVF luck deduction row width")
	}
	var result []LuckDeduction
	prior := 0
	for i := 0; i < len(rows); i += 6 {
		if rows[i] < 0 || rows[i+1] < rows[i] || rows[i+1] > 255 || rows[i+1] <= prior || slices.Min(rows[i+2:i+6]) < 0 {
			return nil, fmt.Errorf("invalid PVF luck deduction interval")
		}
		result = append(result, LuckDeduction{rows[i], rows[i+1], rows[i+2 : i+6]})
		prior = rows[i+1]
	}
	return result, nil
}
