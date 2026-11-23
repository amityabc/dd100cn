package catalogprep

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
)

type GrowthCurve struct {
	Branch, Stage int
	Values        map[string]json.Number
}
type CharacterGrowth struct {
	SourceHash string
	Stats      map[string]int64
	Initial    map[string]json.Number
	Growth     []GrowthCurve
}

var statKeys = map[string]string{
	"hp max": "hp_max", "mp max": "mp_max", "physical attack": "strength", "physical defense": "vitality",
	"magical attack": "intelligence", "magical defense": "spirit", "fire resistance": "fire_resistance",
	"water resistance": "water_resistance", "dark resistance": "dark_resistance", "light resistance": "light_resistance",
	"inventory limit": "inventory_limit", "hp regen speed": "hp_regen_speed", "mp regen speed": "mp_regen_speed",
	"move speed": "move_speed", "attack speed": "attack_speed", "cast speed": "cast_speed", "hit recovery": "hit_recovery",
	"jump power": "jump_power", "weight": "weight",
}

var growthHeader = regexp.MustCompile(`(?i)\[(initial value|growtype (\d+)|awakening ([1-3]))\]`)
var statValue = regexp.MustCompile(`\[([^]\r\n]+)\]\s*(-?\d+(?:\.\d+)?)`)

func CharacterGrowthTables(src Source) (map[byte]CharacterGrowth, error) {
	index, err := src.ReadIndex("character", scriptLimit)
	if err != nil {
		return nil, err
	}
	jobs := map[byte]CharacterGrowth{}
	for job := byte(0); job < 16; job++ {
		path, ok := index[int64(job)]
		if !ok {
			continue
		}
		text, err := src.ReadText(path, scriptLimit)
		if err != nil {
			return nil, err
		}
		growth, err := characterGrowth(text)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", path, err)
		}
		jobs[job] = growth
	}
	if len(jobs) == 0 {
		return nil, fmt.Errorf("matching PVF has no supported character templates")
	}
	return jobs, nil
}

func characterGrowth(text string) (CharacterGrowth, error) {
	var out CharacterGrowth
	out.SourceHash = fmt.Sprintf("%X", sha256.Sum256([]byte(text)))
	headers := growthHeader.FindAllStringSubmatchIndex(text, -1)
	var branch int
	seen := map[[2]int]bool{}
	var base map[string]json.Number
	for i, h := range headers {
		end := len(text)
		if i+1 < len(headers) {
			end = headers[i+1][0]
		}
		values := map[string]json.Number{}
		for _, row := range statValue.FindAllStringSubmatch(text[h[1]:end], -1) {
			key := statKeys[strings.ToLower(row[1])]
			if key == "" {
				continue
			}
			if _, ok := values[key]; !ok {
				values[key] = json.Number(row[2])
			}
		}
		if strings.EqualFold(text[h[2]:h[3]], "initial value") {
			out.Initial = values
			continue
		}
		if h[4] >= 0 {
			n, err := strconv.ParseInt(text[h[4]:h[5]], 10, 32)
			if err != nil {
				return out, err
			}
			branch = int(n)
		}
		stage := 0
		if h[6] >= 0 {
			stage = int(text[h[6]] - '0')
		}
		pair := [2]int{branch, stage}
		if branch <= 0 || seen[pair] {
			return out, fmt.Errorf("duplicate or unowned growth branch %v", pair)
		}
		seen[pair] = true
		out.Growth = append(out.Growth, GrowthCurve{branch, stage, values})
		if pair == [2]int{1, 0} {
			base = values
		}
	}
	for _, field := range []string{"hp_max", "mp_max", "strength", "vitality", "intelligence", "spirit"} {
		if out.Initial[field] == "" || base[field] == "" {
			return out, fmt.Errorf("missing initial/base growth field %q", field)
		}
	}
	slices.SortFunc(out.Growth, func(a, b GrowthCurve) int {
		if a.Branch == b.Branch {
			return a.Stage - b.Stage
		}
		if a.Branch < b.Branch {
			return -1
		}
		return 1
	})
	out.Stats = map[string]int64{}
	for _, key := range statKeys {
		var n int64
		if value := out.Initial[key]; value != "" {
			r, ok := new(big.Rat).SetString(value.String())
			if !ok {
				return out, fmt.Errorf("invalid stat value %s", key)
			}
			r.Mul(r, big.NewRat(10, 1))
			v := new(big.Int).Quo(r.Num(), r.Denom())
			if !v.IsInt64() {
				return out, fmt.Errorf("stat overflow: %s", key)
			}
			n = v.Int64()
		}
		minValue, maxValue := int64(0), int64(math.MaxUint16)
		switch key {
		case "hp_max", "mp_max", "inventory_limit", "move_speed", "weight":
			maxValue = math.MaxUint32
		default:
			if strings.HasSuffix(key, "_resistance") {
				minValue, maxValue = math.MinInt16, math.MaxInt16
			}
		}
		bonus := int64(0)
		if key == "hp_max" {
			bonus = 9800
		}
		if key == "mp_max" {
			bonus = 10500
		}
		if n < minValue-bonus || n > maxValue-bonus {
			return out, fmt.Errorf("stat outside native range: %s", key)
		}
		out.Stats[key] = n + bonus
	}
	return out, nil
}
