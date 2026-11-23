package commerce

import (
	"math"
	"regexp"
	"slices"

	"s9a1/server/internal/catalogprep/gm"
	"s9a1/server/internal/storage"
)

type upgradePrice struct {
	types, rarity, rarity100, cost, cost100 []float32
	levels                                  map[int][2]float32
}

type craftMaterial struct{ id, count uint32 }
type craftUpgradeRow struct {
	failure, penalty, loss [2]int
	material               craftMaterial
}
type craftUpgrade struct {
	price                upgradePrice
	rows                 []craftUpgradeRow
	destroy, correction  map[int]int
	weight               map[int]float32
	bonusID              uint32
	bonusLevel, minGrade int
	hash                 string
}

func (p *craftParser) price(fields []gm.Field, optionalLevels bool) upgradePrice {
	price := upgradePrice{
		types:  p.array(fields, "type", 13),
		rarity: p.array(fields, "cost weights by rarity", 8), rarity100: p.array(fields, "cost weights by rarity 100lv", 8),
		cost: p.array(fields, "cost", 0), cost100: p.array(fields, "cost 100lv", 5),
		levels: map[int][2]float32{},
	}
	values := p.tokens(p.field(fields, "cost weight by upgrade level", optionalLevels))
	p.check(len(values)%3 == 0, "level cost row is incomplete")
	for i := 0; i+2 < len(values); i += 3 {
		level := p.ints(values[i], 0, 31)
		_, duplicate := price.levels[level]
		p.check(!duplicate, "duplicate level cost")
		price.levels[level] = [2]float32{p.float(values[i+1]), p.float(values[i+2])}
	}
	return price
}

var craftRarity = []string{"`common`", "`uncommon`", "`rare`", "`unique`", "`epic`", "`chronicle`", "`legendary`", "`mythology`"}

func craftNamed[T any](p *craftParser, fields []gm.Field, tag string, convert func(string) T) map[int]T {
	values := p.tokens(p.field(fields, tag, false))
	p.check(len(values)%2 == 0, "incomplete "+tag+" pair")
	out := map[int]T{}
	for i := 0; i+1 < len(values); i += 2 {
		rarity := slices.Index(craftRarity, values[i])
		_, duplicate := out[rarity]
		p.check(rarity >= 0 && !duplicate, "unknown or duplicate rarity")
		out[rarity] = convert(values[i+1])
	}
	return out
}

func parseCraftUpgrade(text, table, hash string, optionalLevels bool) (craftUpgrade, error) {
	p := craftParser{name: table}
	fields := p.script(text)
	var values []string
	for _, f := range fields {
		if f.Name != "table type" {
			continue
		}
		tokens := p.tokens(f.Value)
		if len(tokens) > 0 && tokens[0] == "`"+table+"`" {
			p.check(values == nil, "duplicate named table")
			values = tokens[1:]
		}
	}
	if !p.check(len(values) >= 24 && len(values)%24 == 0 && len(values)/24 <= 255, "expected a named table of 24-column rows") {
		return craftUpgrade{}, p.err
	}
	out := craftUpgrade{hash: hash, price: p.price(fields, optionalLevels)}
	for row := range slices.Chunk(values, 24) {
		for _, v := range row {
			p.float(v)
		}
		m := craftMaterial{uint32(p.ints(row[14], 1, math.MaxInt32)), uint32(p.ints(row[15], 0, math.MaxInt32))}
		out.rows = append(out.rows, craftUpgradeRow{
			failure: [2]int{p.ints(row[8], 0, 100000), p.ints(row[9], 0, 100000)},
			penalty: [2]int{p.ints(row[10], 1, 3), p.ints(row[12], 1, 3)},
			loss:    [2]int{p.ints(row[11], 0, 31), p.ints(row[13], 0, 31)}, material: m,
		})
	}
	p.scalar(fields, "notice", 31)
	out.bonusID = uint32(p.scalar(fields, "disjoint bonus item", math.MaxInt32))
	out.bonusLevel = p.scalar(fields, "upgrade const for bonus item count", 255)
	out.minGrade = p.scalar(fields, "equip level const", 512)
	intRarity := func(s string) int { return p.ints(s, 0, 512) }
	out.destroy = craftNamed(&p, fields, "destroy level by rarity", intRarity)
	out.correction = craftNamed(&p, fields, "correction grade by rarity", intRarity)
	out.weight = craftNamed(&p, fields, "upgrade failed bonus weight by rarity", p.float)
	if optionalLevels {
		values := p.tokens(p.field(fields, "amplification const", false))
		p.check(len(values) >= 128 && len(values) <= 1024 && len(values)%4 == 0, "invalid amplification constants")
		for _, value := range values {
			p.ints(value, 0, math.MaxUint16)
		}
	}
	return out, p.err
}

func craftValidEquipment(e storage.CraftEquipment) bool {
	return e.TargetEquipmentSlot >= 12 && e.TargetEquipmentSlot <= 25 && e.TargetEquipmentSlot != 13
}

func craftRound(v float32) (int, bool) {
	if math.IsNaN(float64(v)) || v < 0 || v >= 2147483648 {
		return 0, false
	}
	rounded := float32(v + 0.5)
	return int(rounded), rounded < 2147483648
}

func (p upgradePrice) gold(e storage.CraftEquipment, level int) (int64, bool) {
	if !craftValidEquipment(e) {
		return 0, false
	}
	slot := e.TargetEquipmentSlot - 12
	if slot > 12 {
		slot--
	}
	if e.TargetEquipmentSlot == 24 {
		slot = 0
	}
	cost, rarity, index := p.cost, p.rarity, e.Grade
	if e.MinimumLevel >= 96 {
		cost, rarity, index = p.cost100, p.rarity100, e.MinimumLevel-96
	}
	if index >= len(cost) || slot >= len(p.types) {
		return 0, false
	}
	gold, ok := craftRound(cost[index] * p.types[slot])
	if !ok {
		return 0, false
	}
	gold, ok = craftRound(float32(gold) * rarity[e.Rarity])
	if !ok {
		return 0, false
	}
	if row, exists := p.levels[min(level, 18)]; exists {
		column := 1
		if e.TargetEquipmentSlot == 12 {
			column = 0
		}
		gold, ok = craftRound(float32(gold) * row[column])
	}
	return int64(gold), ok
}

func (u craftUpgrade) addRewards(out map[uint32]uint32, e storage.CraftEquipment, attempt int) bool {
	weight, exists := u.weight[e.Rarity]
	if u.bonusID == 0 || attempt <= u.bonusLevel || e.Grade-u.correction[e.Rarity] < u.minGrade || !exists {
		return true
	}
	difference := attempt - u.bonusLevel
	value := float32(difference*difference) * weight
	if value < 0 || value >= 2147483648 || math.IsNaN(float64(value)) {
		return false
	}
	if count := uint32(value); count > 0 {
		if math.MaxUint32-out[u.bonusID] < count {
			return false
		}
		out[u.bonusID] += count
	}
	return true
}

func (u craftUpgrade) quote(e storage.CraftEquipment, level int, extra *craftUpgrade) *storage.CraftQuote {
	if level >= len(u.rows) {
		return nil
	}
	gold, ok := u.price.gold(e, level)
	if !ok {
		return nil
	}
	row := u.rows[level]
	column := 1
	if e.TargetEquipmentSlot == 12 {
		column = 0
	}
	penalty := row.penalty[column]
	if first, exists := u.destroy[e.Rarity]; exists && level >= first {
		penalty = 3
	}
	out := &storage.CraftQuote{Gold: gold, MaterialID: row.material.id, MaterialCount: row.material.count,
		FailureWeight: row.failure[column], PenaltyType: penalty, PenaltyLoss: row.loss[column], ResourceHash: u.hash,
		DestroyRewards: map[uint32]uint32{}, RemodelPenalties: []struct{ Loss, Weight int }{}}
	if !u.addRewards(out.DestroyRewards, e, level+1) {
		return nil
	}
	if extra != nil {
		if !extra.addRewards(out.DestroyRewards, e, level+1) {
			return nil
		}
		out.ResourceHash = craftHash(u.hash, extra.hash)
	}
	return out
}

type craftForgeRow struct {
	success int
	weight  float32
}
type craftForge struct {
	maximum           int
	rows              []craftForgeRow
	grades, levels    map[int]craftMaterial
	rarity, rarity100 []float32
	hash              string
}

func parseCraftForge(text, hash string) (craftForge, error) {
	p := craftParser{name: "forge"}
	fields := p.script(text)
	out := craftForge{maximum: p.scalar(fields, "separate upgrade max", 8), hash: hash}
	p.scalar(fields, "level", 255)
	tokens := p.tokens(p.field(fields, "table", false))
	p.check(out.maximum > 0 && len(tokens)%4 == 0 && len(tokens)/4 >= out.maximum && len(tokens)/4 <= 255, "invalid forge stages")
	for i := 0; i+3 < len(tokens); i += 4 {
		p.ints(tokens[i], 0, math.MaxInt32)
		p.ints(tokens[i+1], 0, math.MaxInt32)
		weight := p.float(tokens[i+3])
		p.check(weight > 0, "forge material weight must be positive")
		out.rows = append(out.rows, craftForgeRow{p.ints(tokens[i+2], 0, 10000), weight})
	}
	materials := func(tag string) map[int]craftMaterial {
		values := p.tokens(p.field(fields, tag, false))
		p.check(len(values) > 0 && len(values)%3 == 0 && len(values) <= 4096, "invalid forge material rows")
		rows := map[int]craftMaterial{}
		for i := 0; i+2 < len(values); i += 3 {
			grade := p.ints(values[i], 0, 512)
			_, duplicate := rows[grade]
			p.check(!duplicate, "duplicate forge material grade")
			rows[grade] = craftMaterial{uint32(p.ints(values[i+1], 1, math.MaxInt32)), uint32(p.ints(values[i+2], 0, math.MaxInt32))}
		}
		return rows
	}
	out.grades = materials("item weights by grade")
	out.levels = materials("item weights by grade 100lv")
	out.rarity = p.array(fields, "item weights by rarity", 8)
	out.rarity100 = p.array(fields, "item weights by rarity 100lv", 8)
	return out, p.err
}

func (f craftForge) quote(e storage.CraftEquipment, level int) *storage.CraftQuote {
	if e.TargetEquipmentSlot != 12 || e.Grade >= 108 || level >= f.maximum {
		return nil
	}
	material, exists := f.grades[e.Grade]
	rarity := f.rarity
	if e.MinimumLevel >= 96 {
		material, exists = f.levels[e.MinimumLevel]
		rarity = f.rarity100
	}
	if !exists {
		return nil
	}
	stage := f.rows[level]
	factor := float32(rarity[e.Rarity]*stage.weight) + 0.0000001
	count := float32(material.count) * factor
	if count < 0 || count >= 2147483648 || math.IsNaN(float64(count)) {
		return nil
	}
	return &storage.CraftQuote{MaterialID: material.id, MaterialCount: max(1, uint32(count)),
		FailureWeight: (10000 - stage.success) * 10, PenaltyType: 1, ResourceHash: f.hash,
		DestroyRewards: map[uint32]uint32{}, RemodelPenalties: []struct{ Loss, Weight int }{}}
}

type craftRemodelRow struct {
	success   int
	material  craftMaterial
	penalties []struct{ Loss, Weight int }
}
type craftRemodel struct {
	price  upgradePrice
	groups map[int][]craftRemodelRow
	breaks map[int]craftMaterial
	hash   string
}

var (
	remodelGroups = regexp.MustCompile(`(?s)\[remodel group\](.*?)\[/remodel group\]`)
	remodelKey    = regexp.MustCompile(`\[group key index\]\s*([0-9]+)`)
	remodelBreak  = regexp.MustCompile(`\[item break bonus\]\s*([0-9]+)\s+([0-9]+)`)
)

func parseCraftRemodel(text, hash string) (craftRemodel, error) {
	p := craftParser{name: "remodel"}
	fields := p.script(text)
	out := craftRemodel{price: p.price(fields, true), groups: map[int][]craftRemodelRow{}, breaks: map[int]craftMaterial{}, hash: hash}
	p.scalar(fields, "notice", 31)
	for _, f := range fields {
		if f.Name != "table for remodel" {
			continue
		}
		values := p.tokens(f.Value)
		if !p.check(len(values) >= 5, "remodel table is incomplete") {
			break
		}
		group := p.ints(values[0], 1, 255)
		_, duplicate := out.groups[group]
		p.check(!duplicate, "duplicate remodel group")
		rows := []craftRemodelRow{}
		for at := 1; at < len(values); {
			if !p.check(len(values)-at >= 4, "remodel row is truncated") {
				break
			}
			row := craftRemodelRow{success: p.ints(values[at], 0, 100000), material: craftMaterial{
				uint32(p.ints(values[at+1], 1, math.MaxInt32)), uint32(p.ints(values[at+2], 0, math.MaxInt32))},
				penalties: []struct{ Loss, Weight int }{}}
			count := p.ints(values[at+3], 0, 8)
			at += 4
			if !p.check(count >= 0 && len(values)-at >= 2*count, "remodel penalty row is truncated") {
				break
			}
			total := 0
			for j := 0; j < count; j++ {
				loss, weight := p.ints(values[at], -1, 31), p.ints(values[at+1], 1, 100000)
				row.penalties = append(row.penalties, struct{ Loss, Weight int }{loss, weight})
				total += weight
				at += 2
			}
			p.check(count == 0 || total == 100000, "remodel penalty weights are incomplete")
			rows = append(rows, row)
		}
		p.check(len(rows) <= 127, "too many remodel rows")
		out.groups[group] = rows
	}
	p.check(len(out.groups) >= 1 && len(out.groups) <= 8, "invalid remodel group count")
	for _, block := range remodelGroups.FindAllStringSubmatch(text, -1) {
		key, bonus := remodelKey.FindStringSubmatch(block[1]), remodelBreak.FindStringSubmatch(block[1])
		if key == nil || bonus == nil {
			continue
		}
		group := p.ints(key[1], 1, 255)
		p.check(out.groups[group] != nil, "break reward references unknown remodel group")
		id, qty := uint32(p.ints(bonus[1], 0, math.MaxInt32)), uint32(p.ints(bonus[2], 0, math.MaxInt32))
		if id == 0 || qty == 0 {
			continue
		}
		p.check(out.breaks[group].id == 0, "duplicate remodel break reward")
		out.breaks[group] = craftMaterial{id, qty}
	}
	return out, p.err
}

func (r craftRemodel) quote(e storage.CraftEquipment, level int) *storage.CraftQuote {
	rows := r.groups[e.RemodelGroup]
	if level >= len(rows) {
		return nil
	}
	gold, ok := r.price.gold(e, level)
	if !ok {
		return nil
	}
	row := rows[level]
	out := &storage.CraftQuote{Gold: gold, MaterialID: row.material.id, MaterialCount: row.material.count,
		FailureWeight: row.success, PenaltyType: 1, ResourceHash: r.hash, DestroyRewards: map[uint32]uint32{}, RemodelPenalties: row.penalties}
	if bonus, exists := r.breaks[e.RemodelGroup]; exists {
		out.DestroyRewards[bonus.id] = bonus.count
	}
	return out
}
