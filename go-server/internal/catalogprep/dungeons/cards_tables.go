package dungeons

import (
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
)

type EquipmentGeneration = dungeon.EquipmentGeneration

const cardCommonPath = "etc/itemdropinfo_common.etc"
const cardClearPath = "etc/itemdropinfo_clearreward.etc"
const cardRankPath = "etc/ranksysteminfo.etc"
const cardParameterPath = "etc/(r)serverparameter.etc"

type cardGold struct {
	Amount   uint32
	Variance int
}
type cardWindow struct{ Down, Up byte }
type cardBlank struct{ Weight, Item, Quantity uint32 }
type cardRank struct {
	Hash    string
	Grades  []int
	Bonuses []json.Number
}
type freeCardProbability struct {
	Minimum, Maximum byte
	Probability      uint32
}
type cardProbabilityProfile struct {
	Minimum, Maximum          byte
	Probability, SpecialGroup uint32
}
type cardTables struct {
	common, clear, commonHash, clearHash string
	gold                                 map[byte]cardGold
	difficultyRates, difficultyBonuses   []json.Number
	soloPartyRate                        json.Number
	rank                                 cardRank
	kinds, itemTypes, rarity             []uint32
	profiles                             map[string][]cardProbabilityProfile
	windows                              map[byte]cardWindow
	mapRates                             map[byte]uint32
	blank                                []cardBlank
	costs                                map[byte]uint32
	createRateParts                      uint32
}

type cardInput struct{ err error }

func (r *cardInput) fail(format string, args ...any) {
	if r.err == nil {
		r.err = fmt.Errorf(format, args...)
	}
}
func (r *cardInput) section(text, name string) string {
	s, err := pvftext.Section(text, name)
	if err != nil {
		r.fail("%v", err)
	}
	return s
}
func (r *cardInput) uint(token string, min, max uint32) uint32 {
	n, err := strconv.ParseUint(token, 10, 32)
	if err != nil || n < uint64(min) || n > uint64(max) {
		r.fail("invalid card resource unsigned integer %q", token)
	}
	return uint32(n)
}
func (r *cardInput) ints(text string) []int {
	v, err := pvftext.Integers(text)
	if err != nil {
		r.fail("%v", err)
	}
	return v
}
func (r *cardInput) numbers(text string, min, max uint32) []uint32 {
	t := strings.Fields(text)
	out := make([]uint32, len(t))
	for i, v := range t {
		out[i] = r.uint(v, min, max)
	}
	return out
}
func (r *cardInput) width(name string, len, width, minimum, maximum int) bool {
	if len%width != 0 || len < minimum*width || len > maximum*width {
		r.fail("incomplete or oversize card table %s", name)
		return false
	}
	return true
}

var unsignedDecimal = regexp.MustCompile("^(?:[0-9]+\\.?[0-9]*|\\.[0-9]+)$")
var signedDecimal = regexp.MustCompile("^[+-]?(?:[0-9]+\\.?[0-9]*|\\.[0-9]+)$")
var flexibleDecimal = regexp.MustCompile("^[+-]?(?:[0-9]+\\.?[0-9]*|\\.[0-9]+)(?:[Ee][+-]?[0-9]+)?$")

func (r *cardInput) decimals(text string, count int, pattern *regexp.Regexp, minimum float64, exclusive bool) []json.Number {
	tokens := strings.Fields(text)
	if len(tokens) != count {
		r.fail("card numeric table requires %d entries", count)
	}
	out := make([]json.Number, 0, len(tokens))
	for _, v := range tokens {
		f, err := strconv.ParseFloat(v, 64)
		if err != nil || !pattern.MatchString(v) || math.IsInf(f, 0) || f < minimum || exclusive && f == minimum {
			r.fail("invalid card resource decimal %q", v)
			continue
		}
		mantissa, exponent, _ := strings.Cut(strings.ToLower(v), "e")
		var sign string
		if strings.HasPrefix(mantissa, "-") {
			sign = "-"
		}
		mantissa = strings.TrimLeft(mantissa, "+-")
		whole, fraction, point := strings.Cut(mantissa, ".")
		whole = strings.TrimLeft(whole, "0")
		if whole == "" {
			whole = "0"
		}
		name := sign + whole
		if point && fraction != "" {
			name += "." + fraction
		}
		if exponent != "" {
			name += "e" + exponent
		}
		out = append(out, json.Number(name))
	}
	return out
}

func canonicalCardDecimal(v json.Number) string {
	s := v.String()
	if strings.ContainsRune(s, '.') && !strings.ContainsAny(s, "eE") {
		s = strings.TrimRight(strings.TrimRight(s, "0"), ".")
	}
	if s == "-0" {
		s = "0"
	}
	return s
}

func (r *cardInput) rank(a Archive) (cardRank, error) {
	rank, err := a.ReadText(cardRankPath, budget)
	if err != nil {
		return cardRank{}, err
	}
	param, err := a.ReadText(cardParameterPath, budget)
	if err != nil {
		return cardRank{}, err
	}
	rankHash, err := rawHash(a, cardRankPath)
	if err != nil {
		return cardRank{}, err
	}
	paramHash, err := rawHash(a, cardParameterPath)
	if err != nil {
		return cardRank{}, err
	}
	grades := r.numbers(r.section(rank, "rank grade"), 0, 255)
	if len(grades) < 5 || len(grades) > 10 {
		r.fail("incomplete card rank grades")
	}
	gn := make([]int, len(grades))
	parts := make([]string, len(grades))
	for i, g := range grades {
		if i > 0 && grades[i-1] <= g {
			r.fail("non-descending card rank grades")
		}
		gn[i] = int(g)
		parts[i] = strconv.Itoa(int(g))
	}
	bonuses := r.decimals(r.section(param, "clear rank exp bonusrate"), 5, signedDecimal, -1, true)
	bn := make([]string, len(bonuses))
	for i, b := range bonuses {
		bn[i] = canonicalCardDecimal(b)
	}
	identity := "ClearGoldRank/v1;declared-grades-only;five-bonus-slots-reversed\n" +
		cardRankPath + "|" + rankHash + "\n" + cardParameterPath + "|" + paramHash + "\n" + strings.Join(parts, "|") + "\n" + strings.Join(bn, "|") + "\n"
	return cardRank{hashText(identity), gn, bonuses}, r.err
}

var cardProfile = regexp.MustCompile("\\s*\x60([^\x60]+)\x60\\s*([^\x60]*)")

func (r *cardInput) profiles(clear string) map[string][]cardProbabilityProfile {
	max := r.numbers(r.section(clear, "drop prob count"), 1, 64)
	out := make(map[string][]cardProbabilityProfile)
	if len(max) != 1 {
		r.fail("paid card lacks probability row count")
		return out
	}
	body := r.section(clear, "drop prob")
	consumed := 0
	for _, m := range cardProfile.FindAllStringSubmatchIndex(body, -1) {
		if m[0] != consumed {
			r.fail("invalid card probability profile grammar")
			return out
		}
		consumed = m[1]
		name := body[m[2]:m[3]]
		rows := r.numbers(body[m[4]:m[5]], 0, math.MaxUint32)
		if len(name) > 128 || len(rows) != int(max[0])*4 || out[name] != nil {
			r.fail("incomplete or duplicate card probability profile %s", name)
			continue
		}
		next := uint32(1)
		for row := range slices.Chunk(rows, 4) {
			if row[0] != next || row[1] < row[0] || row[1] > 255 || row[2] > 10000 || row[3] > 65535 {
				r.fail("card probability row out of range")
			}
			out[name] = append(out[name], cardProbabilityProfile{byte(row[0]), byte(row[1]), row[2], row[3]})
			next = row[1] + 1
		}
		if next <= 100 {
			r.fail("card profile does not cover native character levels")
		}
	}
	if len(out) == 0 || len(out) > 32 || out["default"] == nil || strings.TrimSpace(body[consumed:]) != "" {
		r.fail("card probability profiles are missing or malformed")
	}
	return out
}

func (r *cardInput) solo(clear string) json.Number {
	if !strings.Contains(clear, "[party member drop bonusrate]") {
		return "1"
	}
	body := r.section(clear, "party member drop bonusrate")
	var solo json.Number
	seen := make(map[int64]bool)
	for _, block := range pvftext.Blocks(body, "dungeon type") {
		values := strings.Fields(block)
		if len(values) != 5 {
			r.fail("invalid card party row")
			continue
		}
		kind, err := strconv.ParseInt(values[0], 10, 32)
		if err != nil || seen[kind] {
			r.fail("invalid or repeated card party dungeon type")
			continue
		}
		seen[kind] = true
		rates := r.decimals(strings.Join(values[1:], " "), 4, unsignedDecimal, 0, false)
		if kind == -1 && len(rates) == 4 {
			solo = rates[0]
		}
	}
	if solo == "" {
		r.fail("card solo party rate absent")
	}
	return solo
}

func cardNeedsStackable(weights []uint32) bool {
	var total uint64
	for _, w := range weights {
		total += uint64(w)
	}
	return len(weights) == 4 && (weights[0] > 0 || weights[2] > 0 || total < 10000)
}

func CardNeedsStackableCandidates(a Archive) (bool, error) {
	text, err := a.ReadText(cardClearPath, budget)
	if err != nil {
		return false, err
	}
	r := &cardInput{}
	v := r.numbers(r.section(text, "drop item type prob"), 0, math.MaxUint32)
	if len(v) != 4 {
		r.fail("card item type requires four columns")
	}
	return cardNeedsStackable(v), r.err
}

func readCardTables(a Archive) (cardTables, error) {
	var t cardTables
	r := &cardInput{}
	var err error
	if t.common, err = a.ReadText(cardCommonPath, budget); err != nil {
		return t, err
	}
	if t.clear, err = a.ReadText(cardClearPath, budget); err != nil {
		return t, err
	}
	if t.commonHash, err = rawHash(a, cardCommonPath); err != nil {
		return t, err
	}
	if t.clearHash, err = rawHash(a, cardClearPath); err != nil {
		return t, err
	}
	if t.rank, err = r.rank(a); err != nil {
		return t, err
	}
	s := t.clear
	t.gold, t.costs, t.windows, t.mapRates = make(map[byte]cardGold), make(map[byte]uint32), make(map[byte]cardWindow), make(map[byte]uint32)
	t.blank = []cardBlank{}
	levels := r.numbers(r.section(t.common, "gold drop ref table"), 0, math.MaxUint32)
	if r.width("gold drop ref table", len(levels), 3, 1, 255) {
		for row := range slices.Chunk(levels, 3) {
			level := byte(row[0])
			if row[0] == 0 || row[0] > 255 || row[1] == 0 || row[2] > 99 || t.gold[level].Amount != 0 {
				r.fail("invalid or duplicate card gold level")
			}
			t.gold[level] = cardGold{row[1], int(row[2])}
		}
	}
	costs := r.numbers(r.section(s, "gold card cost table"), 1, math.MaxUint32)
	if r.width("gold card cost table", len(costs), 2, 1, 255) {
		for row := range slices.Chunk(costs, 2) {
			if row[0] > 255 || t.costs[byte(row[0])] != 0 {
				r.fail("invalid or duplicate card cost")
			}
			t.costs[byte(row[0])] = row[1]
		}
	}
	windows := r.numbers(r.section(s, "item drop ref table"), 0, 255)
	if r.width("item drop ref table", len(windows), 3, 1, 255) {
		for row := range slices.Chunk(windows, 3) {
			_, exists := t.windows[byte(row[0])]
			if row[0] == 0 || exists {
				r.fail("invalid or duplicate card item window")
			}
			t.windows[byte(row[0])] = cardWindow{byte(row[1]), byte(row[2])}
		}
	}
	maximum := len(t.gold)
	if maximum < 100 || len(t.costs) != maximum || len(t.windows) != maximum {
		r.fail("card tables lack common complete level domain")
	}
	for level := 1; level <= maximum; level++ {
		if _, ok := t.windows[byte(level)]; !ok || t.costs[byte(level)] == 0 || t.gold[byte(level)].Amount == 0 {
			r.fail("incomplete card level %d", level)
		}
	}
	rates := r.numbers(r.section(s, "reward item rate per map max count"), 0, 10000)
	if r.width("reward item rate per map max count", len(rates), 2, 1, 255) {
		for row := range slices.Chunk(rates, 2) {
			_, ok := t.mapRates[byte(row[0])]
			if ok || row[0] == 0 || row[0] > 255 {
				r.fail("invalid or duplicate card map rate")
			}
			t.mapRates[byte(row[0])] = row[1]
		}
	}
	b := r.numbers(r.section(s, "gold card blank item"), 1, math.MaxUint32)
	if r.width("gold card blank item", len(b), 3, 0, 256) {
		for row := range slices.Chunk(b, 3) {
			t.blank = append(t.blank, cardBlank{row[0], row[1], row[2]})
		}
	}
	t.difficultyRates = r.decimals(r.section(s, "dungeon difficulty gold drop bonusrate"), 5, unsignedDecimal, 0, false)
	t.difficultyBonuses = r.decimals(r.section(s, "dungeon difficulty drop bonusrate"), 5, flexibleDecimal, 0, false)
	t.soloPartyRate = r.solo(s)
	t.kinds = r.numbers(r.section(s, "drop kind prob"), 0, math.MaxUint32)
	if strings.Contains(s, "[drop kind prob]") && len(t.kinds) != 2 {
		r.fail("card drop kind requires two columns")
	}
	t.itemTypes = r.numbers(r.section(s, "drop item type prob"), 0, math.MaxUint32)
	t.rarity = r.numbers(r.section(s, "basis of rarity dicision"), 0, 1000004)
	if len(t.itemTypes) != 4 || len(t.rarity) != 8 || !slices.IsSorted(t.rarity) || t.rarity[len(t.rarity)-1] < 1_000_000 {
		r.fail("invalid card item type or rarity columns")
	}
	t.profiles = r.profiles(s)
	for _, name := range []string{"default", "event"} {
		rows := t.profiles[name]
		if len(rows) > 0 && int(rows[len(rows)-1].Maximum) < maximum {
			r.fail("card profile %s does not cover resource-level domain", name)
		}
	}
	raw := strings.Fields(r.section(s, "gold card create rate"))
	if len(raw) == 1 && unsignedDecimal.MatchString(raw[0]) {
		if v, ok := new(big.Rat).SetString(raw[0]); ok {
			v.Mul(v, big.NewRat(10000, 1))
			if v.IsInt() && v.Num().IsUint64() && v.Num().Uint64() <= 10000 {
				t.createRateParts = uint32(v.Num().Uint64())
				return t, r.err
			}
		}
	}
	r.fail("card creation rate does not have a supported exact scale")
	return t, r.err
}
