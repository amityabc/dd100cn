package commerce

import (
	"fmt"
	"math"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"s9a1/server/internal/catalogprep/gm"
	"s9a1/server/internal/storage"
)

const sealPrefix = "etc/randomoption/"

type sealTables struct {
	cost      map[[3]int]int
	types     map[[3]int][]int
	quantity  map[[4]int][2]int
	quality   map[[2]int][2]int
	selection map[sealSelection][]storage.MagicSealWeight
	groups    map[int][]storage.MagicSealWeight
	counts    map[int][]storage.MagicSealWeight
	bands     []storage.MagicSealBand
}

type sealSelection struct {
	rarity, job int
	label       string
	tier        int
}

var optionGroupBlock = regexp.MustCompile("(?ims)^\\[option group\\]\\s*(.*?)^\\[/option group\\]")
var sealToken = regexp.MustCompile("\\s*(\x60[^\x60\\r\\n]*\x60|-?[0-9]+(?:\\.[0-9]+)?)")

func BuildMagicSeal(items *Items, ids []uint32) (map[uint32]storage.MagicSealDefinition, error) {
	out := map[uint32]storage.MagicSealDefinition{}
	var tables *sealTables
	for _, id := range ids {
		if items.equipment[int64(id)] == "" {
			continue
		}
		resource, err := items.Source(id)
		if err != nil {
			return nil, err
		}
		flag, err := commerceSection(resource.Text, "random option")
		if err != nil {
			return nil, err
		}
		if flag != "1" {
			continue
		}
		if tables == nil {
			tables, err = readSealTables(items.source)
			if err != nil {
				return nil, err
			}
		}
		def, ok, err := tables.build(resource)
		if err != nil {
			return nil, err
		}
		if ok {
			out[id] = def
		}
	}
	return out, nil
}

func (t *sealTables) build(r ItemSource) (storage.MagicSealDefinition, bool, error) {
	var out storage.MagicSealDefinition
	levelText, eh := commerceSection(r.Text, "minimum level")
	rarityText, er := commerceSection(r.Text, "rarity")
	groupText, eg := commerceSection(r.Text, "item group name")
	if eh != nil || er != nil || eg != nil {
		return out, false, fmt.Errorf("magic seal definition %d has duplicate scalar fields", r.Id)
	}
	rarity, _ := strconv.Atoi(rarityText)
	level, _ := strconv.Atoi(levelText)
	if rarity != 2 && rarity != 3 || level < 1 || level > 255 || !singleLiteral.MatchString(groupText) {
		return out, false, nil
	}
	cost, costOK := t.cost[[3]int{rarity, level, 1}]
	weights := t.types[[3]int{rarity, -1, level}]
	if !costOK || len(weights) != 3 {
		return out, false, nil
	}
	out = storage.MagicSealDefinition{TemplateID: r.Id, ResourceHash: r.Hash, GoldCost: uint32(cost), Tiers: []storage.MagicSealTier{}, Counts: t.counts[rarity], QualityBands: t.bands}
	label := strings.Trim(groupText, "`")
	for tier := 1; tier <= 3; tier++ {
		q, ok := t.quantity[[4]int{rarity, -1, tier, level}]
		quality, good := t.quality[[2]int{rarity, tier}]
		groups := t.selection[sealSelection{rarity, -1, label, tier}]
		if !ok || !good || len(groups) == 0 {
			return storage.MagicSealDefinition{}, false, nil
		}
		x := storage.MagicSealTier{Type: tier, Weight: weights[tier-1], MinimumCount: q[0], MaximumCount: q[1], MinimumQuality: quality[0], MaximumQuality: quality[1], Groups: []storage.MagicSealGroup{}}
		options := map[int]bool{}
		for _, g := range groups {
			values := t.groups[g.Value]
			x.Groups = append(x.Groups, storage.MagicSealGroup{Weight: g.Weight, Options: values})
			if g.Weight > 0 {
				for _, v := range values {
					if v.Weight > 0 {
						options[v.Value] = true
					}
				}
			}
		}
		if len(options) < q[1] || !slices.ContainsFunc(out.Counts, func(w storage.MagicSealWeight) bool { return w.Weight > 0 && w.Value >= q[0] && w.Value <= q[1] }) {
			return out, false, fmt.Errorf("magic seal %d cannot select its complete number of distinct options", r.Id)
		}
		out.Tiers = append(out.Tiers, x)
	}
	return out, true, nil
}

func readSealTables(a gm.ItemArchive) (*sealTables, error) {
	t := &sealTables{
		cost: map[[3]int]int{}, types: map[[3]int][]int{}, quantity: map[[4]int][2]int{}, quality: map[[2]int][2]int{},
		selection: map[sealSelection][]storage.MagicSealWeight{}, groups: map[int][]storage.MagicSealWeight{}, counts: map[int][]storage.MagicSealWeight{},
	}
	sets := map[string]string{}
	for _, path := range []string{"RandomizedOptionOverall1.etc", "RandomizedOptionOverall2.etc", "OptionQuantity.etc", "RandomOption.lst", "OptionGrouping.etc", "OptionGroupSelection.etc", "OptionNumbering.etc"} {
		s, err := a.ReadText(sealPrefix+path, scriptLimit)
		if err != nil {
			return nil, err
		}
		sets[path] = s
	}
	rows := func(source, tag string, width int, put func([]int) bool) error {
		body, err := commerceSection(source, tag)
		if err != nil {
			return err
		}
		values, err := sealIntegers(body)
		if err != nil || len(values) == 0 || len(values)%width != 0 {
			return fmt.Errorf("magic seal PVF table %q has invalid columns", tag)
		}
		for at := 0; at < len(values); at += width {
			if !put(values[at : at+width]) {
				return fmt.Errorf("magic seal PVF table %q has invalid or repeated values", tag)
			}
		}
		return nil
	}
	o1, o2 := sets["RandomizedOptionOverall1.etc"], sets["RandomizedOptionOverall2.etc"]
	if err := rows(o2, "break seal cost", 4, func(r []int) bool {
		return r[3] >= 0 && uniqueMap(t.cost, [3]int{r[0], r[1], r[2]}, r[3])
	}); err != nil {
		return nil, err
	}
	if err := rows(o1, "option type", 6, func(r []int) bool {
		return positiveWeights(r[3:]) && uniqueMap(t.types, [3]int{r[0], r[1], r[2]}, r[3:])
	}); err != nil {
		return nil, err
	}
	if err := rows(o2, "different weight", 4, func(r []int) bool {
		return r[2] >= 0 && r[3] >= r[2] && r[3] <= 100 && uniqueMap(t.quality, [2]int{r[0], r[1]}, [2]int{r[2], r[3]})
	}); err != nil {
		return nil, err
	}
	quantity := sets["OptionQuantity.etc"]
	if err := rows(quantity, "option quantity", 6, func(r []int) bool {
		return r[4] >= 1 && r[5] >= r[4] && r[5] <= 3 && uniqueMap(t.quantity, [4]int{r[0], r[1], r[2], r[3]}, [2]int{r[4], r[5]})
	}); err != nil {
		return nil, err
	}
	ratios, err := commerceBlock(quantity, "quantity ratio")
	if err != nil {
		return nil, err
	}
	for rarity, tag := range map[int]string{2: "common", 3: "unique"} {
		var v []int
		err := rows(ratios, tag, 2, func(r []int) bool {
			v = append(v, r[0])
			t.counts[rarity] = append(t.counts[rarity], storage.MagicSealWeight{Value: r[0], Weight: r[1]})
			return true
		})
		slices.Sort(v)
		if err != nil || !slices.Equal(v, []int{1, 2, 3}) || !sealWeights(t.counts[rarity]) {
			return nil, fmt.Errorf("magic seal quantity weights for rarity %d are invalid", rarity)
		}
	}
	items, err := sealTokens(sets["RandomOption.lst"])
	if err != nil || len(items) == 0 || len(items)%2 != 0 {
		return nil, fmt.Errorf("magic seal option index is incomplete")
	}
	ids := map[int]bool{}
	for i := 0; i < len(items); i += 2 {
		id, err := strconv.Atoi(items[i])
		if err != nil || id < 1 || id > 255 || ids[id] || !singleLiteral.MatchString(items[i+1]) {
			return nil, fmt.Errorf("magic seal option index is invalid")
		}
		ids[id] = true
	}
	groups := optionGroupBlock.FindAllStringSubmatch(sets["OptionGrouping.etc"], -1)
	if len(groups) == 0 {
		return nil, fmt.Errorf("magic seal option groups are absent")
	}
	for _, group := range groups {
		row, err := sealIntegers(group[1])
		if err != nil || len(row) < 3 || len(row)%2 != 1 {
			return nil, fmt.Errorf("magic seal option group shape is invalid")
		}
		var out []storage.MagicSealWeight
		seen := map[int]bool{}
		for i := 1; i < len(row); i += 2 {
			if !ids[row[i]] || seen[row[i]] {
				return nil, fmt.Errorf("magic seal option group has invalid option identity")
			}
			seen[row[i]] = true
			out = append(out, storage.MagicSealWeight{Value: row[i], Weight: row[i+1]})
		}
		if !sealWeights(out) || !uniqueMap(t.groups, row[0], out) {
			return nil, fmt.Errorf("magic seal option groups have repeated identity or invalid weights")
		}
	}
	selection, err := commerceSection(sets["OptionGroupSelection.etc"], "choose option group")
	if err != nil {
		return nil, err
	}
	tokens, err := sealTokens(selection)
	if err != nil || len(tokens) == 0 {
		return nil, fmt.Errorf("magic seal group selection is undefined")
	}
	for len(tokens) > 0 {
		if len(tokens) < 5 {
			return nil, fmt.Errorf("magic seal group selection header is incomplete")
		}
		rarity, e := strconv.Atoi(tokens[0])
		job, je := strconv.Atoi(tokens[1])
		label := tokens[2]
		tier, te := strconv.Atoi(tokens[3])
		n, ne := strconv.Atoi(tokens[4])
		tokens = tokens[5:]
		if e != nil || je != nil || te != nil || ne != nil || n < 1 || n > 3 || len(tokens) < 2*n || !singleLiteral.MatchString(label) {
			return nil, fmt.Errorf("magic seal group selection row is malformed")
		}
		var weights []storage.MagicSealWeight
		for i := range n {
			group, ge := strconv.Atoi(tokens[i])
			weight, we := strconv.Atoi(tokens[i+n])
			if ge != nil || we != nil || t.groups[group] == nil {
				return nil, fmt.Errorf("magic seal source option group is unavailable")
			}
			weights = append(weights, storage.MagicSealWeight{Value: group, Weight: weight})
		}
		tokens = tokens[2*n:]
		if !sealWeights(weights) || !uniqueMap(t.selection, sealSelection{rarity, job, strings.Trim(label, "`"), tier}, weights) {
			return nil, fmt.Errorf("magic seal selection has duplicated keys or invalid weights")
		}
	}
	value, err := commerceSection(sets["OptionNumbering.etc"], "option value ratio")
	if err != nil {
		return nil, err
	}
	tokens, err = sealTokens(value)
	if err != nil || len(tokens) == 0 || len(tokens)%3 != 0 {
		return nil, fmt.Errorf("magic seal quality ratio length is invalid")
	}
	var allWeights []int
	for n := 0; n < len(tokens); n += 3 {
		min, e := strconv.ParseFloat(tokens[n], 64)
		max, me := strconv.ParseFloat(tokens[n+1], 64)
		w, we := strconv.Atoi(tokens[n+2])
		if e != nil || me != nil || we != nil || min < 0 || max < min || max > 1 {
			return nil, fmt.Errorf("magic seal quality range is invalid")
		}
		t.bands = append(t.bands, storage.MagicSealBand{Minimum: min, Maximum: max, Weight: w})
		allWeights = append(allWeights, w)
	}
	if !positiveWeights(allWeights) {
		return nil, fmt.Errorf("magic seal quality weight sum is invalid")
	}
	return t, nil
}

func uniqueMap[K comparable, V any](m map[K]V, key K, v V) bool {
	if _, found := m[key]; found {
		return false
	}
	m[key] = v
	return true
}

func sealTokens(s string) ([]string, error) {
	var out []string
	at := 0
	for _, m := range sealToken.FindAllStringSubmatchIndex(s, -1) {
		if m[0] != at {
			return nil, fmt.Errorf("magic seal has an unexpected resource token")
		}
		out = append(out, s[m[2]:m[3]])
		at = m[1]
	}
	if strings.TrimSpace(s[at:]) != "" {
		return nil, fmt.Errorf("magic seal has an unexpected resource token")
	}
	return out, nil
}

func sealIntegers(s string) ([]int, error) {
	tokens, err := sealTokens(s)
	if err != nil {
		return nil, err
	}
	out := make([]int, 0, len(tokens))
	for _, v := range tokens {
		n, err := strconv.ParseInt(v, 10, 32)
		if err != nil {
			return nil, err
		}
		out = append(out, int(n))
	}
	return out, nil
}

func positiveWeights(v []int) bool {
	total := int64(0)
	for _, w := range v {
		if w < 0 {
			return false
		}
		total += int64(w)
	}
	return total > 0 && total <= math.MaxInt32
}

func sealWeights(v []storage.MagicSealWeight) bool {
	weights := make([]int, 0, len(v))
	for _, x := range v {
		weights = append(weights, x.Weight)
	}
	return positiveWeights(weights)
}
