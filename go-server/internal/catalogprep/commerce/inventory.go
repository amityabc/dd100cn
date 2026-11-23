package commerce

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"math"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"s9a1/server/internal/catalogprep/gm"
	"s9a1/server/internal/storage"
)

type Inventory struct {
	SalePrices        map[uint32]uint32
	Disassembly       map[uint32]storage.DisassemblyRecipe
	RepairDefinitions map[uint32]storage.RepairDefinition
	Discard           map[uint32]storage.DiscardPolicy
	SortMetadata      map[uint32]SortMetadata
	GmApcs            map[uint32]string
}

type SortMetadata struct {
	TemplateId   uint32
	ResourceHash string
	Info         struct {
		Grade       int
		Type        string
		EmblemColor *uint16
	}
}

var discardRestriction = regexp.MustCompile("(?i)delet|discard|drop|quest|lock|period|expir|contract")
var discardFields = regexp.MustCompile("(?im)^\\[/?([^\\]\\r\\n]+)\\]")
var sortType = regexp.MustCompile("^\x60\\[([^\\]\\r\\n\x60]+)\\]\\x60(?:\\s|$)")
var sortSockets = regexp.MustCompile("\x60\\[([ABCDSM]) socket\\]\\x60")
var disjointIndependent = regexp.MustCompile("(?ims)^\\[particular item\\]\\s*(.*?)^\\[/particular item\\]")
var disjointCube = regexp.MustCompile("(?ims)^\\[cube index\\]\\s*(.*?)^\\[/cube index\\]")

func BuildInventory(items *Items, ids []uint32, templates map[uint32]*ItemTemplate, avatarSort []uint32, prepared ...map[uint32]*ItemTemplate) (*Inventory, error) {
	x, err := inventoryItems(items, ids, templates, avatarSort, prepared...)
	if err != nil {
		return nil, err
	}
	x.GmApcs, err = gmAPCs(items.source)
	return x, err
}

func inventoryItems(items *Items, ids []uint32, templates map[uint32]*ItemTemplate, avatarSort []uint32, prepared ...map[uint32]*ItemTemplate) (*Inventory, error) {
	x := &Inventory{SalePrices: map[uint32]uint32{}, Disassembly: map[uint32]storage.DisassemblyRecipe{}, RepairDefinitions: map[uint32]storage.RepairDefinition{},
		Discard: map[uint32]storage.DiscardPolicy{}, SortMetadata: map[uint32]SortMetadata{}}
	price, err := items.source.ReadText("equipment/pricetable.tbl", scriptLimit)
	if err != nil {
		return nil, err
	}
	rateText, err := commerceSection(price, "rate")
	if err != nil {
		return nil, err
	}
	sellRate := firstNumeric(rateText, -1)
	if sellRate < 1 || sellRate > 1000 || math.Trunc(sellRate) != sellRate {
		return nil, fmt.Errorf("equipment sell rate invalid")
	}
	rateText, err = gm.ItemField(price, "repair cost")
	if err != nil {
		return nil, err
	}
	repairRate, _ := strconv.ParseFloat(rateText, 32)
	crafted := unitRepairRates(items.source, "etc/upgrade.etc") && unitRepairRates(items.source, "etc/amplifyupgrade.etc")
	disjoint, err := readDisjoint(items.source)
	if err != nil {
		return nil, err
	}
	unique := map[uint32]bool{}
	for _, id := range ids {
		if id != 0 {
			unique[id] = true
		}
	}
	for _, id := range sortedIDs(unique) {
		r, err := items.Source(id)
		if err != nil {
			return nil, err
		}
		fields, err := gm.ItemFields(r.Text)
		if err != nil {
			return nil, err
		}
		t := templates[id]
		if len(prepared) > 0 {
			t = prepared[0][id]
		}
		if t != nil {
			if d, valid, err := discardPolicy(r, t); err != nil {
				return nil, err
			} else if valid {
				x.Discard[id] = d
			}
		}
		if t := templates[id]; t != nil {
			if s, err := sortMetadata(r, fields); err == nil && t.ResourceHash == r.Hash {
				x.SortMetadata[id] = s
			}
		}
		if r.Equipment {
			if def, valid := repairDefinition(r, fields, float32(repairRate), crafted); valid {
				x.RepairDefinitions[id] = def
			}
		}
		value, err := itemValue(fields)
		if err != nil {
			return nil, err
		}
		if value < 0 && !r.Equipment {
			rawVal, _ := commerceSection(r.Text, "value")
			rawPrice, _ := commerceSection(r.Text, "price")
			if rawVal == "" && rawPrice == "" {
				value = 0
			}
		}
		block1, err := commerceSection(r.Text, "not for sale")
		if err != nil {
			return nil, err
		}
		block2, err := commerceSection(r.Text, "cannot sell")
		if err != nil {
			return nil, err
		}
		if value < 0 || value > math.MaxUint32 || math.Trunc(value) != value || firstNumeric(block1, 0) != 0 || firstNumeric(block2, 0) != 0 {
			continue
		}
		if r.Equipment {
			x.SalePrices[id] = uint32(max(1, uint64(value)*uint64(sellRate)/1000))
			d, ok := disjoint.recipe(r, value)
			if !ok {
				continue
			}
			supported := true
			for _, reward := range append(slices.Clone(d.Additional), d.Cube, d.Expanded) {
				if reward != 0 && (items.equipment[int64(reward)] != "" || items.stackable[int64(reward)] == "") {
					supported = false
					break
				}
			}
			if supported {
				x.Disassembly[id] = d
			}
		} else {
			x.SalePrices[id] = uint32(value) / 5
		}
	}
	for _, id := range avatarSort {
		if _, known := x.SortMetadata[id]; known {
			continue
		}
		r, err := items.Source(id)
		if err != nil {
			continue
		}
		fields, err := gm.ItemFields(r.Text)
		if err != nil {
			continue
		}
		if meta, err := sortMetadata(r, fields); err == nil {
			x.SortMetadata[id] = meta
		}
	}
	return x, nil
}

func firstNumeric(text string, fallback float64) float64 {
	parts := numbers(text)
	if len(parts) == 0 {
		return fallback
	}
	n, err := strconv.ParseFloat(parts[0], 64)
	if err != nil {
		return fallback
	}
	return n
}

func itemValue(fields []gm.Field) (float64, error) {
	skills := false
	values := map[string]string{}
	for _, f := range fields {
		switch f.Name {
		case "all skill item container":
			if skills {
				return -1, fmt.Errorf("nested equipment skill container")
			}
			skills = true
		case "/all skill item container":
			if !skills {
				return -1, fmt.Errorf("unmatched equipment skill container")
			}
			skills = false
		case "value", "price":
			if skills {
				continue
			}
			if _, exists := values[f.Name]; exists {
				return -1, fmt.Errorf("ambiguous commerce field: %s", f.Name)
			}
			values[f.Name] = f.Value
		}
	}
	if skills {
		return -1, fmt.Errorf("unterminated equipment skill container")
	}
	return firstNumeric(values["value"], firstNumeric(values["price"], -1)), nil
}

func repairDefinition(r ItemSource, fields []gm.Field, rate float32, crafted bool) (storage.RepairDefinition, bool) {
	def := storage.RepairDefinition{TemplateID: r.Id, ResourceHash: r.Hash, CostRate: rate, SupportsCrafted: crafted}
	cost, err := unsigned(uniqueScalar(fields, "repair price"), 31)
	grade, ge := unsigned(uniqueScalar(fields, "grade"), 31)
	durability, de := unsigned(uniqueScalar(fields, "durability"), 16)
	if err != nil || ge != nil || de != nil || grade > 255 || durability == 0 || !(rate > 0 && rate <= 1) ||
		cost*(grade+5) > math.MaxInt32 {
		return def, false
	}
	def.Grade, def.RepairPrice, def.MaximumDurability = int(grade), int(cost), uint16(durability)
	value := float32(cost*(grade+5)/10) * rate
	value /= float32(durability)
	value *= float32(durability)
	value += 0.5
	return def, value < math.MaxInt32
}

func uniqueScalar(fields []gm.Field, name string) string {
	seen := false
	var value string
	for _, f := range fields {
		if f.Name != name {
			continue
		}
		if seen {
			return ""
		}
		seen, value = true, f.Value
	}
	return value
}

func unitRepairRates(a gm.ItemArchive, path string) bool {
	text, err := a.ReadText(path, scriptLimit)
	if err != nil {
		return false
	}
	block, err := commerceBlock(text, "repair cost rate by upgrade level")
	if err != nil {
		return false
	}
	values := strings.Fields(block)
	if len(values) != 108 {
		return false
	}
	for _, v := range values {
		f, err := strconv.ParseFloat(v, 32)
		if err != nil || f != 1 {
			return false
		}
	}
	return true
}

func discardPolicy(r ItemSource, t *ItemTemplate) (storage.DiscardPolicy, bool, error) {
	var none storage.DiscardPolicy
	if t.FateCollection || t.AccountCommonInventory || t.PetConsumable || t.Kind == 2 && t.FirstBagSlot != 65 && t.FirstBagSlot != 121 {
		return none, false, nil
	}
	attach, err := commerceSection(r.Text, "attach type")
	if err != nil {
		return none, false, err
	}
	if attach != "`[free]`" && attach != "`[account]`" {
		return none, false, nil
	}
	for _, f := range discardFields.FindAllStringSubmatch(r.Text, -1) {
		name := strings.ToLower(f[1])
		if name == "expiration date" && t.ResourceExpiration != nil || name == "usable period" && t.RelativeLifetimeSeconds > 0 ||
			name == "daily delete item" && t.DailyDeletion || name == "weekly delete item" && t.WeeklyDeletionDay != nil ||
			name == "fatiguetime monthly delete" && t.FatigueTimeMonthlyDeletion {
			continue
		}
		if discardRestriction.MatchString(name) {
			return none, false, nil
		}
	}
	return storage.DiscardPolicy{Template: r.Id, ResourceHash: r.Hash}, true, nil
}

func sortMetadata(r ItemSource, fields []gm.Field) (SortMetadata, error) {
	out := SortMetadata{TemplateId: r.Id, ResourceHash: r.Hash}
	invalid := fmt.Errorf("sort metadata for template %d is invalid", r.Id)
	for _, f := range fields {
		if f.Name != "rarity" {
			continue
		}
		n, err := unsigned(f.Value, 8)
		if err != nil || n > 9 {
			return out, invalid
		}
		out.Info.Grade = int(n)
	}
	raw, err := gm.ItemField(r.Text, "equipment type")
	if err != nil {
		return out, err
	}
	if raw == "" {
		raw, err = gm.ItemField(r.Text, "stackable type")
	}
	if err != nil {
		return out, err
	}
	m := sortType.FindStringSubmatch(raw)
	if m == nil || len(m[1]) > 96 {
		return out, invalid
	}
	out.Info.Type = strings.ToLower(m[1])
	if m[1] != "avatar emblem" {
		return out, nil
	}
	target, err := gm.ItemField(r.Text, "avatar emblem target type")
	if err != nil {
		return out, err
	}
	sockets := sortSockets.FindAllStringSubmatch(target, -1)
	if len(sockets) < 1 || len(sockets) > 6 || strings.TrimSpace(sortSockets.ReplaceAllString(target, "")) != "" {
		return out, invalid
	}
	var color uint16
	seen := map[string]bool{}
	for _, socket := range sockets {
		if seen[socket[1]] {
			return out, invalid
		}
		seen[socket[1]] = true
		color |= map[string]uint16{"A": 1, "B": 2, "C": 4, "D": 8, "S": 16, "M": 0xFFEF}[socket[1]]
	}
	out.Info.EmblemColor = &color
	return out, nil
}

func gmAPCs(a gm.ItemArchive) (map[uint32]string, error) {
	index, err := a.ReadIndex("aicharacter")
	if err != nil {
		return nil, err
	}
	out := map[uint32]string{}
	for id, path := range index {
		if id < 1 || id > 80000 {
			continue
		}
		text, err := a.ReadText(path, scriptLimit)
		if err != nil || strings.TrimSpace(text) == "" {
			continue
		}
		data, err := a.Read(path, scriptLimit)
		if err != nil {
			continue
		}
		d := sha256.Sum256(data)
		out[uint32(id)] = strings.ToUpper(hex.EncodeToString(d[:]))
	}
	return out, nil
}

type disjointPool struct {
	items     []uint32
	constants []float64
}
type disjointRules struct {
	base                 []float64
	cube                 uint32
	excluded, events     map[uint32]int
	additional, expanded []disjointPool
}

func readDisjoint(a gm.ItemArchive) (*disjointRules, error) {
	text, err := a.ReadText("etc/disjoint.etc", scriptLimit)
	if err != nil {
		return nil, err
	}
	d := &disjointRules{}
	raw, err := commerceSection(text, "cube creation const")
	if err != nil {
		return nil, err
	}
	d.base, err = asFloatFields(raw)
	if err != nil {
		return nil, err
	}
	for _, cube := range disjointCube.FindAllStringSubmatch(text, -1) {
		if !strings.Contains(cube[1], "[no element]") {
			continue
		}
		val, err := asFloatFields(cube[1])
		if err != nil {
			return nil, err
		}
		if len(val) == 0 || !disjointID(val[0]) || d.cube != 0 {
			return nil, fmt.Errorf("disassembly cube mapping is absent or ambiguous")
		}
		d.cube = uint32(val[0])
	}
	events, err := commerceBlock(text, "event result")
	if err != nil {
		return nil, err
	}
	e, err := cashIntegers(events, 3)
	if err != nil {
		return nil, err
	}
	d.events = map[uint32]int{}
	for i := 0; i < len(e); i += 3 {
		d.events[e[i]]++
	}
	ind, err := commerceBlock(text, "independent result")
	if err != nil {
		return nil, err
	}
	for _, item := range disjointIndependent.FindAllStringSubmatch(ind, -1) {
		raw, err := commerceBlock(item[1], "input index")
		if err != nil {
			return nil, err
		}
		for id := range numberSet(raw) {
			d.events[id]++
		}
	}
	ex, err := commerceBlock(text, "exclude additional result expand")
	if err != nil {
		return nil, err
	}
	d.excluded = numberSet(ex)
	d.additional, err = disjointPools(text, "additional result", "additional result const", 4)
	if err != nil {
		return nil, err
	}
	d.expanded, err = disjointPools(text, "additional result expand", "additional result expand const", 3)
	if err != nil {
		return nil, err
	}
	return d, nil
}

func disjointPools(text, field, coefficient string, width int) ([]disjointPool, error) {
	body, err := commerceSection(text, field)
	if err != nil {
		return nil, err
	}
	tail, err := commerceSection(text, coefficient)
	if err != nil {
		return nil, err
	}
	data, err := asFloatFields(body)
	if err != nil {
		return nil, err
	}
	coef, err := asFloatFields(tail)
	if err != nil {
		return nil, err
	}
	var out []disjointPool
	at, cp := 0, 0
	for at < len(data) {
		n := data[at]
		at++
		if n < 0 || n > 10000 || math.Trunc(n) != n || at+int(n) > len(data) || cp >= len(coef) {
			return nil, fmt.Errorf("disassembly output table truncated")
		}
		w := width
		if coef[cp] == 0 {
			w = 1
		}
		if cp+w > len(coef) || n > 0 && w == 1 {
			return nil, fmt.Errorf("disassembly coefficient table truncated")
		}
		p := disjointPool{items: []uint32{}, constants: coef[cp : cp+w]}
		for _, id := range data[at : at+int(n)] {
			if !disjointID(id) {
				return nil, fmt.Errorf("disassembly reward ID invalid")
			}
			p.items = append(p.items, uint32(id))
		}
		out = append(out, p)
		at += int(n)
		cp += w
	}
	if cp != len(coef) {
		return nil, fmt.Errorf("disassembly coefficient tail mismatch")
	}
	return out, nil
}

func asFloatFields(text string) ([]float64, error) {
	parts := numbers(text)
	out := make([]float64, 0, len(parts))
	for _, v := range parts {
		n, err := strconv.ParseFloat(v, 64)
		if err != nil {
			return nil, err
		}
		out = append(out, n)
	}
	return out, nil
}

func disjointID(n float64) bool { return n > 0 && n < math.MaxUint32 && math.Trunc(n) == n }
func (d *disjointRules) recipe(r ItemSource, value float64) (storage.DisassemblyRecipe, bool) {
	var out storage.DisassemblyRecipe
	if d.cube == 0 || d.events[r.Id] != 0 || value <= 0 {
		return out, false
	}
	field, err := commerceSection(r.Text, "disjoint")
	blocked, be := commerceSection(r.Text, "cannot disjoint")
	impossible, ie := commerceBlock(r.Text, "impossible contents")
	attach, ae := commerceSection(r.Text, "attach type")
	if err != nil || be != nil || ie != nil || ae != nil || firstNumeric(field, 1) == 0 || firstNumeric(blocked, 0) != 0 ||
		strings.Contains(strings.ToLower(impossible), "disjoint") || strings.Contains(strings.ToLower(attach), "trade delete") {
		return out, false
	}
	rarity, re := commerceSection(r.Text, "rarity")
	level, le := commerceSection(r.Text, "minimum level")
	grade := int(firstNumeric(rarity, -1))
	if re != nil || le != nil || grade < 0 || grade+1 >= len(d.base) || d.base[0] <= 0 || d.base[grade+1] < 0 {
		return out, false
	}
	cubes := max(1, math.Floor(value*d.base[grade+1]/d.base[0]))
	if cubes > math.MaxUint32 {
		return out, false
	}
	var additional, expanded disjointPool
	if grade < len(d.additional) {
		additional = d.additional[grade]
	}
	if grade < len(d.expanded) {
		expanded = d.expanded[grade]
	}
	if len(expanded.items) > 1 {
		return out, false
	}
	coeff := func(v []float64, i int) float64 {
		if i >= len(v) {
			return 0
		}
		return v[i]
	}
	out = storage.DisassemblyRecipe{Cube: d.cube, Cubes: uint32(cubes), Additional: emptySlice(additional.items), Level: max(1, int(firstNumeric(level, 1))),
		LowDivisor: coeff(additional.constants, 0), HighDivisor: coeff(additional.constants, 1), LowChance: coeff(additional.constants, 2),
		ExpandedDivisor: coeff(expanded.constants, 0), ExtraChance: coeff(expanded.constants, 1), ExpandedChance: coeff(expanded.constants, 2)}
	if len(expanded.items) == 1 && d.excluded[r.Id] == 0 {
		out.Expanded = expanded.items[0]
	}
	return out, true
}
