package commerce

import (
	"encoding/json"
	"fmt"
	"math"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"s9a1/server/internal/catalogprep/gm"
	"s9a1/server/internal/storage"
)

type consumableScript struct{ fields []gm.Field }

func (s consumableScript) values(name string) []string { return matchingFields(s.fields, name) }
func (s consumableScript) has(name string) bool        { return len(s.values(name)) != 0 }

func (s consumableScript) value(name string) (string, error) {
	values := s.values(name)
	if len(values) == 0 {
		return "", nil
	}
	if len(values) > 1 && (name != "attach type" || slices.ContainsFunc(values[1:], func(v string) bool { return v != values[0] })) {
		return "", fmt.Errorf("ambiguous consumable field %s", name)
	}
	return values[0], nil
}

func (s consumableScript) number(name string, fallback, limit uint32, lastWrite bool) (uint32, error) {
	values := s.values(name)
	if len(values) > 1 && !lastWrite {
		return 0, fmt.Errorf("ambiguous consumable number %s", name)
	}
	for _, v := range values {
		n, err := giftInt(v)
		if err != nil || n > limit {
			return 0, fmt.Errorf("invalid consumable number %s", name)
		}
		fallback = n
	}
	return fallback, nil
}

func (s consumableScript) block(name string, adjacent bool) bool {
	v, close := s.values(name), s.values("/"+name)
	if len(v) != len(close) || len(v) > 1 || len(close) > 0 && close[0] != "" {
		return false
	}
	if !adjacent || len(v) == 0 {
		return true
	}
	at := slices.IndexFunc(s.fields, func(f gm.Field) bool { return f.Name == name })
	return at+1 < len(s.fields) && s.fields[at+1].Name == "/"+name
}

type Consumables struct {
	Potions      map[uint32]storage.PotionDefinition
	FatigueItems map[uint32]storage.FatigueItemDefinition
}

func BuildConsumables(items *Items, potionIDs, fatigueIDs []uint32, overrides map[uint32]*ItemTemplate) (*Consumables, error) {
	out := &Consumables{Potions: map[uint32]storage.PotionDefinition{}, FatigueItems: map[uint32]storage.FatigueItemDefinition{}}
	ids := map[uint32]byte{8008: 1}
	for _, id := range potionIDs {
		ids[id] |= 1
	}
	for _, id := range fatigueIDs {
		ids[id] |= 2
	}
	for _, id := range sortedIDs(ids) {
		if items.stackable[int64(id)] == "" || items.equipment[int64(id)] != "" {
			continue
		}
		r, err := items.Source(id)
		if err != nil {
			return nil, err
		}
		if !strings.Contains(r.Text, "[add fatigue]") && (ids[id]&1 == 0 || !strings.Contains(r.Text, "[waste]")) {
			continue
		}
		t := overrides[id]
		if t == nil {
			t, err = preparedTemplate(r)
		}
		if err != nil || t.Kind != 2 || t.FirstBagSlot != 65 || t.LastBagSlot != 120 {
			continue
		}
		fields, err := gm.ItemFields(r.Text)
		if err != nil {
			continue
		}
		s := consumableScript{fields}
		if ids[id]&1 != 0 {
			if d, err := parsePotion(r, t, s); err == nil {
				out.Potions[id] = d
			}
		}
		if ids[id]&2 != 0 && strings.Contains(r.Text, "[add fatigue]") {
			if d, err := parseFatigueItem(r, t, s); err == nil {
				out.FatigueItems[id] = d
			}
		}
	}
	return out, nil
}

var potionAllowed = strings.Split("name|name2|explain|basic explain|flavor text|grade|rarity|usable job|/usable job|attach type|minimum level|maximum level|weight|price|value|icon|field image|stackable type|item group name|move wav|stack limit|hp recovery|mp recovery|active status recovery|cool time|cooltime group|stat change|/stat change|stat change duration|summon apc|expert type|sub type|icon mark|usable expired item|expiration date|usable period|daily delete item|weekly delete item|impossible contents|/impossible contents|item category|/item category", "|")
var potionOculusFields = []string{"weekly delete item", "action usable place", "/action usable place", "action usable dungeon", "/action usable dungeon", "impossible contents", "/impossible contents"}
var potionWaste = regexp.MustCompile("^`\\[waste\\]`\\s+[014]$")
var potionRecoveryRow = regexp.MustCompile("^`([+%])`\\s+([0-9]+(?:\\.[0-9]+)?)\\s+([0-9]+)\\s+`myself`$")
var potionStatusRow = regexp.MustCompile("^`([^`]+)`\\s+([0-9]+)\\s+`myself`$")
var potionStatDuration = regexp.MustCompile("^([0-9]+)\\s+`myself`$")
var potionStatRow = regexp.MustCompile("`\\+`\\s+([0-9]+)\\s+`([^`]+)`")

type oculusPotionStats struct {
	JumpPower, AttackSpeed, MoveSpeed int
	DurationMilliseconds              uint32
}

func readOculusPotionStats(id uint32) (*oculusPotionStats, string) {
	switch id {
	case 10313330, 490015881:
		return &oculusPotionStats{20, 0, 0, 30000}, "`+` 20 `jump power`"
	case 10313331, 490015882:
		return &oculusPotionStats{0, 150, 150, 60000}, "`+` 150 `attack speed` `+` 150 `move speed`"
	case 10313949, 490015883:
		return &oculusPotionStats{0, 0, 180, 60000}, "`+` 180 `move speed`"
	}
	return nil, ""
}

func parsePotion(src ItemSource, t *ItemTemplate, s consumableScript) (storage.PotionDefinition, error) {
	d := storage.PotionDefinition{TemplateID: src.Id, ResourceHash: src.Hash, UsableJobs: t.UsableJobs, Dungeons: []uint32{}}
	id := src.Id
	stats, statSource := readOculusPotionStats(id)
	oculus := stats != nil || id == 10312870 || id == 490015880
	value := func(name string) string { v, _ := s.value(name); return v }
	finishPrice := id == 6500 && value("finish point price") == "30" || id == 6501 && value("finish point price") == "20"
	for _, f := range s.fields {
		if !slices.Contains(potionAllowed, f.Name) && !(finishPrice && f.Name == "finish point price") && !(oculus && slices.Contains(potionOculusFields, f.Name)) {
			return d, fmt.Errorf("unsupported potion field %s", f.Name)
		}
		if f.Name != "cool time" && f.Name != "cooltime group" {
			if _, err := s.value(f.Name); err != nil {
				return d, err
			}
		}
	}
	kind, group := value("stackable type"), value("item group name")
	if !potionWaste.MatchString(kind) || group != "" && group != "`potion`" {
		return d, fmt.Errorf("potion is not a supported consumable")
	}
	if oculus {
		if err := applyOculusPotion(&d, t, s, stats, statSource); err != nil {
			return d, err
		}
	} else {
		for _, name := range []string{"impossible contents", "item category", "stat change"} {
			if !s.block(name, false) {
				return d, fmt.Errorf("potion metadata block is incomplete: %s", name)
			}
		}
		if q := value("impossible contents"); q != "" && q != "`gift`" && q != "`ban redeemitem`" || value("item category") != "" {
			return d, fmt.Errorf("potion content restriction is unsupported")
		}
		if s.has("expiration date") || s.has("usable period") || s.has("daily delete item") || s.has("weekly delete item") {
			a, err := src.Gift()
			if err != nil || !t.PvfGiftItem || !sameOptional(t.ResourceExpiration, a.ResourceExpiration) || t.RelativeLifetimeSeconds != a.RelativeLifetimeSeconds ||
				t.DailyDeletion != a.DailyDeletion || !sameOptional(t.WeeklyDeletionDay, a.WeeklyDeletionDay) {
				return d, fmt.Errorf("potion lifetime differs from its complete acquisition template")
			}
		}
	}
	var err error
	d.HP, err = parsePotionRecovery(value("hp recovery"), s.has("hp recovery"))
	if err != nil {
		return d, err
	}
	d.MP, err = parsePotionRecovery(value("mp recovery"), s.has("mp recovery"))
	if err != nil {
		return d, err
	}
	if stats == nil && (s.has("stat change") || s.has("/stat change") || s.has("stat change duration")) {
		d.NativeSelfStatChanges, err = parsePotionStats(kind, value("stat change"), value("stat change duration"))
		if err != nil {
			return d, err
		}
	}
	if s.has("active status recovery") {
		if err := applyPotionStatus(&d, kind, value("active status recovery")); err != nil {
			return d, err
		}
	}
	minimum, e1 := s.number("minimum level", 1, 255, false)
	maximum, e2 := s.number("maximum level", 255, 255, false)
	cooldown, e3 := s.number("cool time", 0, math.MaxInt32, true)
	cooldownGroup, e4 := s.number("cooltime group", 0, math.MaxUint32, true)
	if e1 != nil || e2 != nil || e3 != nil || e4 != nil || minimum == 0 || minimum > maximum || uint32(t.MinimumLevel) != minimum {
		return d, fmt.Errorf("invalid potion level or cooldown metadata")
	}
	d.MinimumLevel, d.MaximumLevel, d.CooldownMilliseconds = byte(minimum), byte(maximum), cooldown
	if s.has("cooltime group") {
		d.CooldownGroup = &cooldownGroup
	}
	if stats != nil && (minimum != 1 && stats.JumpPower == 0 || minimum != 45 && stats.JumpPower != 0 || maximum != 255 || cooldown != stats.DurationMilliseconds || cooldownGroup != 10) {
		return d, fmt.Errorf("Oculus potion level or cooldown qualification is inconsistent")
	}
	other := d.NativeSelfStatChanges != nil || d.NativeAnyStatusRecoveryLevel != nil || d.NativeSelfStatusRecovery != nil
	if s.has("summon apc") {
		if value("summon apc") != "5409 60 60 900000 1" || kind != "`[waste]` 0" || value("expert type") != "`[doll_controller]`" || value("sub type") != "2" ||
			value("usable expired item") != "" || t.ResourceExpiration != nil || t.RelativeLifetimeSeconds != 0 || t.DailyDeletion || t.WeeklyDeletionDay != nil || cooldown != 900000 || cooldownGroup != 100 ||
			other || d.HP != nil || d.MP != nil {
			return d, fmt.Errorf("APC summon resource is outside the qualified No.28 native contract")
		}
		d.NativeApcSummon = &storage.PotionSummon{ActorCode: 5409, Level: 60, DurationMilliseconds: 900000, NativeFlag: 1}
	} else if s.has("expert type") || s.has("sub type") || s.has("usable expired item") {
		return d, fmt.Errorf("potion profession or expiry effect is unsupported")
	}
	switch id {
	case 8, 1085, 6500, 6501:
		if other || d.HP != nil || d.MP != nil || (id == 8 || id == 1085) && kind != "`[waste]` 0" || (id == 6500 || id == 6501) && kind != "`[waste]` 1" {
			return d, fmt.Errorf("built-in potion has an incompatible effect layout")
		}
		switch id {
		case 8:
			d.HP = &storage.PotionRecovery{Percent: true, NativeFloatPercent: true, NativeWideMaximum: true, Amount: "30", DurationMilliseconds: 1000}
			d.MP = &storage.PotionRecovery{Percent: true, NativeFloatPercent: true, Amount: "30", DurationMilliseconds: 1000}
		case 1085:
			d.MP = &storage.PotionRecovery{Percent: true, NativeFloatPercent: true, NativeMultiplyThenScale: true, Amount: "30", DurationMilliseconds: 100}
		case 6500:
			d.HP = &storage.PotionRecovery{Percent: true, NativeFloatPercent: true, NativeMultiplyThenScale: true, NativeWideMaximum: true, Amount: "50", DurationMilliseconds: 100}
		case 6501:
			d.MP = &storage.PotionRecovery{Percent: true, NativeFloatPercent: true, NativeMultiplyThenScale: true, Amount: "50", DurationMilliseconds: 100}
		}
	}
	if !other && stats == nil && d.HP == nil && d.MP == nil && d.NativeApcSummon == nil {
		return d, fmt.Errorf("potion has no supported effect")
	}
	d.AcquisitionTemplate, err = json.Marshal(t)
	return d, err
}

func applyOculusPotion(d *storage.PotionDefinition, t *ItemTemplate, s consumableScript, stats *oculusPotionStats, effect string) error {
	normal := slices.Contains([]uint32{10312870, 10313330, 10313331, 10313949}, t.TemplateId)
	banRedeem := t.TemplateId != 490015883
	wantDungeons, wantType := "400000389", "`[waste]` 0"
	d.Dungeons = []uint32{400000389}
	if normal {
		wantDungeons, d.Dungeons = "100000007 100002669", []uint32{100000007, 100002669}
	}
	if stats != nil {
		wantType = "`[waste]` 1"
	}
	v := func(name string) string {
		value, _ := s.value(name)
		return strings.Join(strings.Fields(value), " ")
	}
	if !t.PvfGiftItem || t.WeeklyDeletionDay == nil || *t.WeeklyDeletionDay != 4 || v("weekly delete item") != "4" || v("action usable place") != "`[dungeon]`" ||
		v("action usable dungeon") != wantDungeons || banRedeem && v("impossible contents") != "`ban redeemitem`" || !banRedeem && (s.has("impossible contents") || s.has("/impossible contents")) ||
		v("attach type") != "`[account]`" || v("stackable type") != wantType {
		return fmt.Errorf("Oculus potion acquisition or dungeon permissions differ")
	}
	weekly := byte(4)
	d.WeeklyDeletionDay = &weekly
	if stats == nil {
		return nil
	}
	for _, n := range []string{"usable job", "stat change", "action usable place", "action usable dungeon", "impossible contents"} {
		if n == "impossible contents" && !banRedeem {
			continue
		}
		if !s.has(n) || !s.block(n, true) {
			return fmt.Errorf("Oculus stat potion has incomplete block %s", n)
		}
	}
	if v("stat change") != effect || v("stat change duration") != strconv.FormatUint(uint64(stats.DurationMilliseconds), 10)+" `myself`" ||
		v("usable job") != "`[all]`" || t.UsableJobs != math.MaxUint16 || t.StackLimit != 5 || v("stack limit") != "5" || t.DailyDeletion || t.RelativeLifetimeSeconds != 0 ||
		t.ResourceExpiration != nil || s.has("hp recovery") || s.has("mp recovery") {
		return fmt.Errorf("Oculus stat potion raw values or acquisition differ")
	}
	var err error
	d.NativeSelfStats, err = json.Marshal(stats)
	return err
}

func sameOptional[T comparable](a, b *T) bool {
	return a == nil && b == nil || a != nil && b != nil && *a == *b
}

func parsePotionRecovery(value string, present bool) (*storage.PotionRecovery, error) {
	if !present {
		return nil, nil
	}
	m := potionRecoveryRow.FindStringSubmatch(value)
	if m == nil {
		return nil, fmt.Errorf("potion recovery value, duration or target is unsupported")
	}
	v, e1 := strconv.ParseFloat(m[2], 64)
	d, e2 := strconv.ParseUint(m[3], 10, 31)
	if e1 != nil || e2 != nil || v <= 0 || v > math.MaxInt32 || m[1] == "%" && v > 100 {
		return nil, fmt.Errorf("potion recovery value or duration is unsupported")
	}
	amount := strings.TrimLeft(m[2], "0")
	if strings.HasPrefix(amount, ".") {
		amount = "0" + amount
	}
	return &storage.PotionRecovery{Percent: m[1] == "%", Amount: json.Number(amount), DurationMilliseconds: uint32(d)}, nil
}

func applyPotionStatus(d *storage.PotionDefinition, typ, value string) error {
	m := potionStatusRow.FindStringSubmatch(value)
	if typ != "`[waste]` 0" || m == nil {
		return fmt.Errorf("potion recovery requires ordinary self status use")
	}
	n, err := strconv.ParseUint(m[2], 10, 31)
	if err != nil {
		return err
	}
	level := uint32(n)
	if m[1] == "any" {
		d.NativeAnyStatusRecoveryLevel = &level
		return nil
	}
	status := map[string]byte{"freeze": 1, "poison": 2, "stun": 3, "blind": 5, "stone": 7, "sleep": 8, "burn": 9, "bleeding": 11, "confuse": 15}[m[1]]
	if status == 0 {
		return fmt.Errorf("potion single status recovery is unsupported")
	}
	d.NativeSelfStatusRecovery, err = json.Marshal(struct {
		Status       byte
		MaximumLevel uint32
	}{status, level})
	return err
}

func parsePotionStats(typ, rows, duration string) (json.RawMessage, error) {
	m := potionStatDuration.FindStringSubmatch(duration)
	if typ != "`[waste]` 0" || m == nil {
		return nil, fmt.Errorf("stat potion requires finite ordinary self use")
	}
	d, err := strconv.ParseUint(m[1], 10, 31)
	if err != nil || d == 0 {
		return nil, fmt.Errorf("stat potion duration is invalid")
	}
	type statChange struct {
		Stat   byte
		Amount int32
	}
	out := struct {
		DurationMilliseconds uint32
		Changes              []statChange
	}{DurationMilliseconds: uint32(d)}
	seen := map[byte]bool{}
	at := 0
	for _, r := range potionStatRow.FindAllStringSubmatchIndex(rows, -1) {
		gap := rows[at:r[0]]
		if strings.TrimSpace(gap) != "" || at != 0 && gap == "" {
			return nil, fmt.Errorf("stat potion row grammar is invalid")
		}
		at = r[1]
		stat := map[string]byte{"all activestatus resistance": 1, "physical attack": 2, "magical attack": 3, "hp max": 4, "mp max": 5,
			"move speed": 6, "cast speed": 7, "attack speed": 8, "physical defense": 9, "magical defense": 10, "physical critical hit": 11,
			"physical critical hit rate": 11, "magical critical hit": 12, "magical critical hit rate": 12}[rows[r[4]:r[5]]]
		value, err := strconv.ParseInt(rows[r[2]:r[3]], 10, 32)
		if stat == 0 || seen[stat] || err != nil || value <= 0 {
			return nil, fmt.Errorf("stat potion value or property is unsupported")
		}
		seen[stat] = true
		out.Changes = append(out.Changes, statChange{stat, int32(value)})
	}
	if len(out.Changes) == 0 || at != len(rows) {
		return nil, fmt.Errorf("stat potion has incomplete rows")
	}
	return json.Marshal(out)
}
