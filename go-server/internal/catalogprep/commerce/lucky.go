package commerce

import (
	"encoding/json"
	"fmt"
	"math"
	"regexp"
	"slices"
	"strings"

	"s9a1/server/internal/catalogprep/gm"
	"s9a1/server/internal/storage"
)

const luckyConfigPath = "etc/chn_luckyboxsystem.etc"

var luckyFields = strings.Split("lucky box type random|lucky box result grade random|result item group|result grade|item list|treasure gage info|treasure gage max|multi open bonus multiply count|limited item random|new limited item info|new limited item info end date|new limited item URL|old limited item info|box ani path|box open ani path|mini box ani path|box item ani path|mini box item ani path|back shine ani path", "|")
var luckyToken = regexp.MustCompile("`[^`]*`|[0-9]+")
var luckyType = map[string]byte{"`rare`": 1, "`unique`": 2, "`epic`": 3, "`mythology`": 4}
var luckyGrade = map[string]byte{"`grade b`": 1, "`grade a`": 2, "`grade s`": 3, "`grade ss`": 4}

func readLuckyBox(items *Items, src ItemSource, templates map[uint32]*ItemTemplate) (storage.LuckyBoxRule, error) {
	var d storage.LuckyBoxRule
	if cashField(src, "action type") != "`[ancient hero lucky box]`" || cashField(src, "action usable place") != "`[seria room]` `[dont use assult]`" {
		return d, fmt.Errorf("lucky box source place or action differs")
	}
	t, err := src.Gift()
	if err != nil {
		return d, err
	}
	text, hash, err := sourceTextAndHash(items.source, luckyConfigPath)
	if err != nil {
		return d, err
	}
	fields, err := gm.ItemFields(text)
	if err != nil {
		return d, err
	}
	for _, f := range fields {
		if !slices.Contains(luckyFields, strings.TrimLeft(f.Name, "/")) {
			return d, fmt.Errorf("lucky configuration contains an unsupported field: %s", f.Name)
		}
	}
	s := consumableScript{fields}
	for _, name := range []string{"lucky box type random", "lucky box result grade random", "treasure gage info", "limited item random"} {
		if !s.has(name) || !s.block(name, true) {
			return d, fmt.Errorf("lucky box block is incomplete: %s", name)
		}
	}
	v := func(name string) string { v, _ := s.value(name); return v }
	d.Source, err = json.Marshal(t)
	if err != nil {
		return d, err
	}
	d.ConfigurationHash = hash
	d.Types, err = parseLuckyWeights(v("lucky box type random"), luckyType)
	if err != nil || !positiveLuckyWeight(d.Types) {
		return d, fmt.Errorf("lucky type weights are out of bounds")
	}
	tokens, err := luckyTokens(v("lucky box result grade random"))
	if err != nil || len(tokens) != 48 {
		return d, fmt.Errorf("lucky box requires every native grade row")
	}
	d.Grades = map[byte][]storage.LuckyWeight{}
	for i := 0; i < len(tokens); i += 3 {
		typ, grade := luckyType[tokens[i]], luckyGrade[tokens[i+1]]
		weight, err := giftInt(tokens[i+2])
		if err != nil || typ == 0 || grade == 0 {
			return d, fmt.Errorf("lucky box grade rows are invalid")
		}
		d.Grades[typ] = append(d.Grades[typ], storage.LuckyWeight{Value: grade, Weight: weight})
	}
	for i := byte(1); i <= 4; i++ {
		if !fourWeights(d.Grades[i]) || !positiveLuckyWeight(d.Grades[i]) {
			return d, fmt.Errorf("lucky box grade weights are incomplete")
		}
	}
	pending := map[uint32]*ItemTemplate{src.Id: t}
	resolve := func(id uint32) (*ItemTemplate, error) {
		if known := pending[id]; known != nil {
			return known, nil
		}
		if items.stackable[int64(id)] == "" || items.equipment[int64(id)] != "" {
			return nil, fmt.Errorf("lucky reward %d is not an ordinary stackable", id)
		}
		r, err := items.Gift(id)
		if err == nil {
			pending[id] = r
		}
		return r, err
	}
	d.Rewards = map[byte][]storage.LuckyReward{}
	markers := []string{"result item group", "result grade", "item list", "/item list", "/result item group"}
	for _, marker := range markers {
		if len(s.values(marker)) != 4 {
			return d, fmt.Errorf("lucky box requires four complete reward groups")
		}
	}
	for i := 0; i < len(fields); i++ {
		f := fields[i]
		if f.Name != markers[0] {
			continue
		}
		if i+len(markers) > len(fields) || f.Value != "" {
			return d, fmt.Errorf("lucky reward group incomplete")
		}
		for j, marker := range markers {
			if fields[i+j].Name != marker || j > 2 && fields[i+j].Value != "" {
				return d, fmt.Errorf("lucky reward group incomplete")
			}
		}
		grade := luckyGrade[fields[i+1].Value]
		if grade == 0 || d.Rewards[grade] != nil {
			return d, fmt.Errorf("lucky reward group grade unsupported or repeated")
		}
		d.Rewards[grade], err = parseLuckyRewards(fields[i+2].Value, resolve)
		if err != nil {
			return d, err
		}
		i += len(markers) - 1
	}
	if len(d.Rewards) != 4 {
		return d, fmt.Errorf("lucky reward groups are missing")
	}
	gauge, err := parseLuckyWeights(v("treasure gage info"), luckyGrade)
	if err != nil {
		return d, err
	}
	d.TreasureGaugeMaximum, err = s.number("treasure gage max", 0, math.MaxInt32, false)
	if err != nil || d.TreasureGaugeMaximum == 0 {
		return d, fmt.Errorf("lucky treasure gauge maximum is invalid")
	}
	d.MultiOpenMultiplier, err = s.number("multi open bonus multiply count", 0, math.MaxInt32, false)
	if err != nil || d.MultiOpenMultiplier == 0 {
		return d, fmt.Errorf("lucky selected-box multiplier is invalid")
	}
	d.TreasureGaugeIncrements = map[byte]uint32{}
	for _, row := range gauge {
		if row.Weight > d.TreasureGaugeMaximum {
			return d, fmt.Errorf("lucky treasure gauge increment exceeds its range")
		}
		d.TreasureGaugeIncrements[row.Value] = row.Weight
	}
	d.LimitedRewards, err = parseLuckyRewards(v("limited item random"), resolve)
	if err != nil {
		return d, err
	}
	for id, t := range pending {
		templates[id] = t
	}
	return d, nil
}

func luckyTokens(text string) ([]string, error) {
	if strings.TrimSpace(luckyToken.ReplaceAllString(text, "")) != "" {
		return nil, fmt.Errorf("lucky table contains unsupported tokens")
	}
	return luckyToken.FindAllString(text, -1), nil
}

func parseLuckyWeights(text string, label map[string]byte) ([]storage.LuckyWeight, error) {
	p, err := luckyTokens(text)
	if err != nil || len(p) != 8 {
		return nil, fmt.Errorf("lucky weights are incomplete")
	}
	var rows []storage.LuckyWeight
	for i := 0; i < len(p); i += 2 {
		v, ok := label[p[i]]
		w, err := giftInt(p[i+1])
		if !ok || err != nil {
			return nil, fmt.Errorf("lucky weighted label is unsupported")
		}
		rows = append(rows, storage.LuckyWeight{Value: v, Weight: w})
	}
	if !fourWeights(rows) {
		return nil, fmt.Errorf("lucky weighted labels are repeated")
	}
	return rows, nil
}

func fourWeights(w []storage.LuckyWeight) bool {
	if len(w) != 4 {
		return false
	}
	var bits uint8
	for _, row := range w {
		bits |= 1 << row.Value
	}
	return bits == 0b11110
}

func positiveLuckyWeight(w []storage.LuckyWeight) bool {
	var total uint64
	for _, row := range w {
		total += uint64(row.Weight)
	}
	return total > 0 && total <= math.MaxInt32
}

func parseLuckyRewards(text string, resolve func(uint32) (*ItemTemplate, error)) ([]storage.LuckyReward, error) {
	p, err := giftInts(text, false)
	if err != nil || len(p)%4 != 0 || len(p) > 16384 {
		return nil, fmt.Errorf("lucky reward tuple is incomplete")
	}
	var rows []storage.LuckyReward
	var sum uint64
	for i := 0; i < len(p); i += 4 {
		id, count, weight, flag := p[i], p[i+1], p[i+2], p[i+3]
		if id == 0 || id == math.MaxUint32 || count == 0 || count > math.MaxInt32 || flag > 1 {
			return nil, fmt.Errorf("lucky reward identity, amount, or flag is invalid")
		}
		t, err := resolve(id)
		if err != nil {
			return nil, err
		}
		raw, err := json.Marshal(t)
		if err != nil {
			return nil, err
		}
		rows = append(rows, storage.LuckyReward{Template: raw, Count: count, Weight: weight, Flag: flag})
		sum += uint64(weight)
	}
	if sum == 0 || sum > math.MaxInt32 {
		return nil, fmt.Errorf("lucky reward weight total is invalid")
	}
	return rows, nil
}
