package catalogprep

import (
	"fmt"
	"math"
	"regexp"
	"strconv"
	"strings"

	"s9a1/server/internal/catalogprep/pvftext"
)

const scriptLimit = 1024 * 1024

type Source interface {
	Read(path string, maximum int) ([]byte, error)
	ReadText(path string, maximum int) (string, error)
	ReadIndex(category string, maximum ...int) (map[int64]string, error)
	ExperienceThresholds(maximum int) ([]int64, error)
}

type Level struct {
	Threshold int64
	Sp, Tp    uint16
}
type ClassReplacement struct {
	MinimumLevel, MaximumLevel byte
	Gold                       []uint32
}

func Progression(src Source) ([]Level, ClassReplacement, error) {
	thresholds, err := src.ExperienceThresholds(scriptLimit)
	if err != nil {
		return nil, ClassReplacement{}, err
	}
	if len(thresholds) < 100 {
		return nil, ClassReplacement{}, fmt.Errorf("experience table has %d levels", len(thresholds))
	}
	points, err := src.ReadText("etc/sptable.etc", scriptLimit)
	if err != nil {
		return nil, ClassReplacement{}, err
	}
	readPoints := func(tag string) (map[int]uint16, error) {
		fields, err := pvftext.Numbers(points, tag)
		if err != nil {
			return nil, err
		}
		if len(fields) == 0 || len(fields)%2 != 0 {
			return nil, fmt.Errorf("invalid %s pairs", tag)
		}
		out := map[int]uint16{}
		for i := 0; i < len(fields); i += 2 {
			level, award := fields[i], fields[i+1]
			if _, used := out[level]; used || level < 1 || level > 255 || award < 0 || award > 65535 {
				return nil, fmt.Errorf("invalid %s at level %d", tag, level)
			}
			out[level] = uint16(award)
		}
		return out, nil
	}
	sp, err := readPoints("sp table")
	if err != nil {
		return nil, ClassReplacement{}, err
	}
	tp, err := readPoints("tp table")
	if err != nil {
		return nil, ClassReplacement{}, err
	}
	levels := make([]Level, 100)
	for i := range levels {
		spAward, spOK := sp[i+1]
		tpAward, tpOK := tp[i+1]
		if !spOK || i+1 >= 50 && !tpOK || thresholds[i] < 0 || i > 0 && thresholds[i] < thresholds[i-1] {
			return nil, ClassReplacement{}, fmt.Errorf("invalid progression at level %d", i+1)
		}
		if i+1 < 50 {
			tpAward = 0
		}
		levels[i] = Level{thresholds[i], spAward, tpAward}
	}
	text, err := src.ReadText("character/growup.etc", scriptLimit)
	if err != nil {
		return nil, ClassReplacement{}, err
	}
	lv, err := pvftext.Numbers(text, "grow up change lv")
	if err != nil {
		return nil, ClassReplacement{}, err
	}
	gold, err := pvftext.Numbers(text, "grow up change gold")
	if err != nil {
		return nil, ClassReplacement{}, err
	}
	if len(lv) != 2 || lv[0] < 1 || lv[1] < lv[0] || lv[1] > 100 || len(gold) == 0 {
		return nil, ClassReplacement{}, fmt.Errorf("invalid class replacement table")
	}
	replacement := ClassReplacement{byte(lv[0]), byte(lv[1]), make([]uint32, len(gold))}
	for i, n := range gold {
		if n < 0 {
			return nil, ClassReplacement{}, fmt.Errorf("negative class replacement cost")
		}
		replacement.Gold[i] = uint32(n)
	}
	return levels, replacement, nil
}

var goldTable = regexp.MustCompile(`(?s)^\s*\[gold limit from level\]\s*(.*?)\[/gold limit from level\]\s*$`)

func GoldCarryLimits(src Source) (map[byte]int64, error) {
	text, err := src.ReadText("etc/goldlimitbylevel.etc", scriptLimit)
	if err != nil {
		return nil, err
	}
	m := goldTable.FindStringSubmatch(text)
	if m == nil {
		return nil, fmt.Errorf("missing complete gold limit table")
	}
	fields := strings.Fields(m[1])
	if len(fields) != 202 {
		return nil, fmt.Errorf("gold limits require 101 level/value pairs")
	}
	limits := make(map[byte]int64, 101)
	for i := 0; i <= 100; i++ {
		level, e := strconv.ParseInt(fields[2*i], 10, 32)
		n, e2 := strconv.ParseUint(fields[2*i+1], 10, 32)
		if e != nil || e2 != nil || level != int64(i) || n > math.MaxInt32 || i > 0 && n == 0 {
			return nil, fmt.Errorf("invalid gold limit row %d", i)
		}
		limits[byte(i)] = int64(n)
	}
	return limits, nil
}

var decimalInteger = regexp.MustCompile(`-?\d+`)

func DailyLimits(src Source) (map[uint32]byte, error) {
	text, err := src.ReadText("etc/dungeonspecialreward.etc", scriptLimit)
	if err != nil {
		return nil, err
	}
	limits := map[uint32]byte{}
	for _, block := range pvftext.Blocks(text, "dungeon") {
		parts := decimalInteger.FindAllString(block, -1)
		if len(parts) < 4 {
			continue
		}
		values := make([]int, len(parts))
		for i, s := range parts {
			n, e := strconv.ParseInt(s, 10, 32)
			if e != nil {
				return nil, e
			}
			values[i] = int(n)
		}
		id := values[0]
		aliases := 0
		switch id {
		case 100000002, 100000176, 100000177, 100000178:
		case 100000179:
			aliases = 2
		default:
			continue
		}
		if _, seen := limits[uint32(id)]; seen || values[1] != aliases || len(values) < 4+aliases || values[2+aliases] < 1 || values[2+aliases] > 32 || values[3+aliases] != 0 {
			return nil, fmt.Errorf("invalid daily reward counter %d", id)
		}
		limits[uint32(id)] = byte(values[2+aliases])
	}
	return limits, nil
}
