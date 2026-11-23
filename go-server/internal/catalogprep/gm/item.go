package gm

import (
	"encoding/binary"
	"errors"
	"fmt"
	"math"
	"regexp"
	"slices"
	"strings"

	"s9a1/server/internal/itemdata"
)

type ItemTemplate = itemdata.ItemTemplate
type WeaponQualification = itemdata.WeaponQualification
type weaponJob = itemdata.WeaponJob

var (
	equipmentLabel       = regexp.MustCompile("^`\\[([^\\]]+)\\]`(?:\\s+[0-9]+)?$")
	stackableLabel       = regexp.MustCompile("^`\\[([^\\]]+)\\]`((?:\\s+[0-9]+)+)$")
	genericLabel         = regexp.MustCompile("^`\\[([^\\]]+)\\]`(?:\\s+[0-9]+)*$")
	specialLabel         = regexp.MustCompile("^`\\[(?:[^\\]]+ avatar|creature|artifact (?:red|blue|green))\\]`(?:\\s+[0-9]+)?$")
	tradeLimit           = regexp.MustCompile("^1\\s+`\\[account\\]`$")
	freshCardType        = "`[material expert job]` 1"
	unsupportedItemField = regexp.MustCompile("(?i)expiration|expiry|usable period|avatar|creature|artifact|package|contract|required skill|required job skill")
	unsupportedGiftField = regexp.MustCompile("(?i)trade limit|spend trade|expiry|expiration|daily delete|weekly delete|monthly delete|create date|packagable|avatar|creature|artifact|socket|seal count")
	ordinaryRecovery     = regexp.MustCompile("^`[+%]`\\s+[0-9]+(?:\\.[0-9]+)?\\s+[0-9]+\\s+`myself`$")
	equipmentSlots       = map[string]uint16{"weapon": 12, "title name": 13, "coat": 14, "shoulder": 15, "pants": 16, "waist": 18, "shoes": 17,
		"amulet": 19, "wrist": 20, "ring": 21, "support": 22, "magic stone": 23, "earring": 25, "flag": 32, "support weapon": 24, "name tag": 30,
		"charm": 31, "talisman": 34, "talisman addition": 35, "talisman third": 36, "odd": 37}
)

func parseItem(id uint32, equipment bool, s script, hash string, raw []byte, resolve func(int32) (string, error)) (*ItemTemplate, bool, error) {
	if equipment {
		kind, err := s.section("equipment type")
		if err != nil {
			return nil, false, err
		}
		if specialLabel.MatchString(kind) {
			return nil, true, nil
		}
	}
	if result, err := parseGift(id, equipment, s, hash, raw, resolve); err == nil {
		return result, false, nil
	}
	if !equipment && id <= 2 {
		return nil, false, errors.New("Virtual balances require their wallet owner")
	}
	if s.has("usable period after unsealing") {
		return nil, false, errors.New("GM acquisition needs the deferred unsealing lifetime owner")
	}
	expiry, err := s.section("title expiry date")
	if err != nil {
		return nil, false, err
	}
	if expiry != "" && !absoluteGrammar.MatchString(expiry) {
		return nil, false, errors.New("GM title expiration grammar is unsupported")
	}
	kindField := "stackable type"
	if equipment {
		kindField = "equipment type"
	}
	kind, err := s.section(kindField)
	if err != nil {
		return nil, false, err
	}
	match := genericLabel.FindStringSubmatch(kind)
	if match == nil {
		return nil, false, errors.New("GM item has no valid resource classification")
	}
	label := match[1]
	minimum, err := s.section("minimum level")
	if err != nil {
		return nil, false, err
	}
	if minimum == "" || minimum == "0" {
		minimum = "1"
	}
	jobs, err := s.section("usable job")
	if err != nil {
		return nil, false, err
	}
	if _, err := parseJobs(jobs); jobs == "" || !equipment && err != nil {
		jobs = "`[all]`"
	}
	plain := script{fields: []field{{name: "minimum level", value: minimum}, {name: "usable job", value: jobs}}}
	if !equipment {
		storage := "waste"
		switch label {
		case "material", "material instant item":
			storage = "material"
		case "material expert job", "quest":
			storage = label
		}
		kind = "`[" + storage + "]` 0"
	}
	plain.fields = append(plain.fields, field{name: kindField, value: kind})
	if expiry != "" {
		plain.fields = append(plain.fields, field{name: "expiration date", value: expiry})
	}
	for _, f := range s.fields {
		switch f.name {
		case "durability", "stack limit", "attach type", "trade limit", "trade limit max", "expiration date", "usable period", "daily delete item", "weekly delete item", "fatiguetime monthly delete":
			if f.name == "trade limit" && s.get("attach type") == "`[account]`" {
				continue
			}
			if f.name == "usable period" && absoluteGrammar.MatchString(f.value) {
				f.name = "expiration date"
			}
			if f.value == "`[trade limit] 1`" {
				f.value = "`[trade limit]`"
			}
			plain.fields = append(plain.fields, f)
		case "required job skill", "/required job skill":
			if equipment {
				plain.fields = append(plain.fields, f)
			}
		}
	}
	if equipment && (s.has("required skill") || s.has("/required skill")) && !plain.has("required job skill") {
		plain.fields = append(plain.fields, field{name: "required job skill"}, field{name: "/required job skill"})
	}
	result, err := parseGift(id, equipment, plain, hash, raw, resolve)
	if err != nil {
		return nil, false, err
	}
	result.PvfGiftItem = true
	if !equipment {
		switch label {
		case "avatar emblem":
			result.FirstBagSlot, result.LastBagSlot = 289, 360
		case "feed", "creature", "creature expitem":
			result.PetConsumable, result.FirstBagSlot, result.LastBagSlot = true, 216, 271
		}
	}
	if s.has("stack in account common item inventory") {
		result.AccountCommonInventory = true
	}
	if !validItem(result) {
		return nil, false, errors.New("GM acquisition template is invalid")
	}
	return result, false, nil
}

func parseOrdinary(id uint32, equipment bool, s script, hash string, raw []byte, resolve func(int32) (string, error)) (*ItemTemplate, error) {
	if !equipment {
		if t, err, qualified := pinnedItem(id, s, hash); qualified {
			return t, err
		}
		for _, name := range []string{"hp recovery", "mp recovery", "active status recovery", "stat change"} {
			values := s.values(name)
			if len(values) > 1 {
				return nil, fmt.Errorf("Ambiguous commerce field: %s", name)
			}
			if len(values) == 1 && values[0] != "" {
				for _, calendar := range []string{"daily delete item", "weekly delete item", "fatiguetime monthly delete", "usable period", "expiration date"} {
					if s.has(calendar) {
						return nil, errors.New("Recovery item requires complete lifetime acquisition metadata")
					}
				}
				break
			}
		}
	}
	minValue, err := s.section("minimum level")
	if err != nil {
		return nil, err
	}
	level, err := parseInteger(minValue, 1, 255, 1)
	if err != nil {
		return nil, err
	}
	jobValue, err := s.section("usable job")
	if err != nil {
		return nil, err
	}
	jobs, err := parseJobs(jobValue)
	if err != nil {
		return nil, err
	}
	t := &ItemTemplate{TemplateId: id, FirstBagSlot: 9, LastBagSlot: 64, StackLimit: 1, WornSlot: math.MaxUint16, MinimumLevel: byte(level), UsableJobs: jobs,
		ResourceHash: hash, WeaponQualification: WeaponQualification{Verified: true, Jobs: []weaponJob{}, Valid: true}}
	pinnedEmblem := id >= 2500000 && id <= 2500004 || id >= 2510000 && id <= 2510004 || id >= 2520000 && id <= 2520004 || id >= 2530000 && id <= 2530004
	scope := 0
	for _, f := range s.fields {
		switch f.name {
		case "if":
			scope++
		case "/if":
			scope--
		case "required job skill", "required skill":
			continue
		}
		if !strings.HasPrefix(f.name, "/") && unsupportedItemField.MatchString(f.name) &&
			!(pinnedEmblem && f.name == "avatar emblem target type") &&
			!(equipment && scope > 0 && f.name == "avatar count in my pocket" && validAvatarCondition(f.value)) {
			return nil, errors.New("Item requires unsupported expiry, special detail or permission rules")
		}
	}
	if equipment {
		kind, err := s.section("equipment type")
		if err != nil {
			return nil, err
		}
		match := equipmentLabel.FindStringSubmatch(kind)
		if match == nil {
			return nil, errors.New("Equipment type grammar unsupported")
		}
		label := strings.ToLower(match[1])
		worn, ok := equipmentSlots[label]
		if !ok {
			return nil, errors.New("Special equipment requires an independent detail owner")
		}
		t.Kind, t.WornSlot, t.WornSlotIsNative = 1, worn, true
		minimum := uint64(1)
		switch label {
		case "weapon":
			t.Kind, t.WornSlotIsNative, minimum = 0, false, 0
		case "flag":
			minimum = 0
		}
		durability, err := s.section("durability")
		if err != nil {
			return nil, err
		}
		d, err := parseInteger(durability, minimum, math.MaxUint16, 45)
		if err != nil {
			return nil, err
		}
		t.MaximumDurability = uint16(d)
	} else {
		kind, err := s.section("stackable type")
		if err != nil {
			return nil, err
		}
		match := stackableLabel.FindStringSubmatch(kind)
		if match == nil {
			return nil, errors.New("Stackable type grammar unsupported")
		}
		label, sub := strings.ToLower(match[1]), strings.TrimSpace(match[2])
		exp := s.get("attach type")
		shared, sharedItem := sharedSubtype(id)
		var exactMaterial bool
		switch id {
		case 3166, 3167:
			exactMaterial = level == 1 && jobs == math.MaxUint16 && exp == "`[free]`" && s.get("item group name") == "`element stuff`"
		case 3150, 3155, 3242, 10313056, 10313057, 10313058, 10313060:
			group := "`mineral stuff`"
			attach := "`[trade]`"
			if id == 3150 || id == 3155 || id == 3242 {
				attach = "`[free]`"
			}
			if id == 3155 || id == 3242 {
				group = "`stuff etc`"
			}
			exactMaterial = level == 1 && jobs == math.MaxUint16 && exp == attach && s.get("item group name") == group
		}
		card := s.get("item category") == "`monster card`" && kind == freshCardType && s.get("sub type") == "1" && exp == ""
		if pinned := pinnedCard(id); pinned != "" && !(card && level == 1 && jobs == math.MaxUint16 && s.get("int data") == pinned && tradeLimit.MatchString(s.get("trade limit"))) {
			return nil, errors.New("Monster card differs from its verified personal-bag and first-trade acquisition shape")
		}
		switch sub {
		case "0":
		case "1":
			if !(card || label == "material" && exactMaterial || label == "waste" && basicRecovery(s)) {
				return nil, errors.New("Stackable subtype requires a verified ordinary material template")
			}
		case "2", "8", "10":
			if !sharedItem {
				return nil, errors.New("Stackable subtype requires an unsupported material/detail owner")
			}
		case "4":
			if !(sharedItem || label == "waste" && basicRecovery(s)) {
				return nil, errors.New("Stackable subtype requires an unsupported use/detail owner")
			}
		default:
			return nil, errors.New("Stackable type grammar unsupported")
		}
		if sharedItem {
			group := "`cube stuff`"
			if shared == "10" {
				group = "`cloth stuff`"
			}
			if level != 1 || jobs != math.MaxUint16 || minValue != "1" || jobValue != "`[all]`" ||
				shared == "0" && exp != "`[account]`" || shared != "0" && (exp != "`[free]`" || s.get("item group name") != group) {
				return nil, errors.New("Shared material requires its exact pinned permission and classification shape")
			}
			if label != "material" || sub != shared {
				return nil, errors.New("Shared material type differs from its exact pinned template")
			}
		}
		t.Kind = 2
		switch label {
		case "avatar emblem":
			if !pinnedEmblem || level != 1 || jobs != math.MaxUint16 || exp != "`[trade delete]`" || sub != "0" ||
				s.get("avatar emblem target type") != "`["+string(rune('A'+(id-2500000)/10000))+" socket]`" {
				return nil, errors.New("Shop emblem requires its exact pinned ordinary core and permission shape")
			}
			t.FirstBagSlot, t.LastBagSlot = 289, 360
		case "unlimited waste":
			if id != 10312154 || !strings.EqualFold(hash, "89574935CFBDF3891CC9B64B95957A1ED5FDEFDEB5B9B30CBE6C762A4561A6F6") {
				return nil, errors.New("Stackable requires an unsupported use/detail owner")
			}
			t.FirstBagSlot, t.LastBagSlot = 65, 120
		case "enchant waste":
			if sub != "0" {
				return nil, errors.New("Stackable requires an unsupported use/detail owner")
			}
			t.FirstBagSlot, t.LastBagSlot = 65, 120
		default:
			first, last, ok := bagFor(label)
			if !ok {
				return nil, errors.New("Stackable requires an unsupported use/detail owner")
			}
			t.FirstBagSlot, t.LastBagSlot = first, last
		}
		limit, err := s.section("stack limit")
		if err != nil {
			return nil, err
		}
		n, err := parseInteger(limit, 1, math.MaxUint32, math.MaxUint32)
		if err != nil {
			return nil, err
		}
		t.StackLimit, t.MonsterCard = uint32(n), card
		trade := s.get("trade limit")
		if (card || label == "enchant waste") && trade != "" {
			if !tradeLimit.MatchString(trade) || exp != "" && exp != "`[free]`" {
				if card {
					return nil, errors.New("Monster card trade policy requires its native account-bound shape")
				}
				return nil, errors.New("Enchant bead trade policy requires its existing verified fresh-instance shape")
			}
			t.PvfGiftItem, t.AccountAfterFirstTrade = true, true
		}
	}
	t.WeaponQualification, err = parseQualification(s, t.Kind, t.UsableJobs, raw, resolve)
	if err != nil {
		return nil, err
	}
	if !validItem(t) {
		return nil, errors.New("Ordinary item template is invalid")
	}
	return t, nil
}

func bagFor(label string) (uint16, uint16, bool) {
	switch label {
	case "waste":
		return 65, 120, true
	case "material":
		return 121, 176, true
	case "quest":
		return 177, 232, true
	case "material expert job":
		return 233, 288, true
	}
	return 0, 0, false
}

func validItem(t *ItemTemplate) bool {
	if t.TemplateId == 0 || t.TemplateId == math.MaxUint32 || t.MinimumLevel == 0 || t.UsableJobs == 0 || !t.WeaponQualification.Valid {
		return false
	}
	if (t.AccountAfterFirstTrade || t.InitialSealed || t.RelativeLifetimeSeconds != 0 || t.DailyDeletion || t.WeeklyDeletionDay != nil) && !t.PvfGiftItem {
		return false
	}
	if t.FirstBagSlot > t.LastBagSlot || t.StackLimit == 0 || t.DailyDeletion && t.WeeklyDeletionDay != nil ||
		t.FatigueTimeMonthlyDeletion && (t.Kind != 2 || t.DailyDeletion || t.WeeklyDeletionDay != nil) {
		return false
	}
	if t.CashPackage || t.CashPackageReward {
		if t.Kind != 2 || t.ResourceExpiration == nil || *t.ResourceExpiration <= 0 || *t.ResourceExpiration > math.MaxInt32 {
			return false
		}
	}
	if t.Kind == 2 {
		return t.WornSlot == math.MaxUint16 && t.MaximumDurability == 0 &&
			(!t.FateCollection || t.StackLimit == 1) && (!t.AccountCommonInventory || !t.FateCollection) &&
			(!t.PetConsumable || !t.AccountCommonInventory && !t.FateCollection && t.FirstBagSlot == 216 && t.LastBagSlot == 271)
	}
	return !t.FateCollection && !t.AccountCommonInventory && !t.PetConsumable &&
		t.StackLimit == 1 && t.WornSlot >= 12 && t.WornSlot <= 32 &&
		(t.MaximumDurability > 0 || t.Kind == 0 || t.WornSlotIsNative && t.WornSlot == 32)
}

func sharedSubtype(id uint32) (string, bool) {
	switch id {
	case 3033, 3034, 3035, 3036:
		return "4", true
	case 3037:
		return "2", true
	case 3262:
		return "8", true
	case 3051, 3052:
		return "10", true
	case 10100115, 10100116, 10099773, 10099774, 10099775, 10158124:
		return "0", true
	}
	return "", false
}

func pinnedCard(id uint32) string {
	return map[uint32]string{10312955: "526", 10312957: "527", 10312959: "528", 10312985: "530", 10312987: "529", 10312989: "532",
		10312991: "531", 10312993: "533", 10312995: "534", 10320036: "668", 10320038: "669", 10320040: "670"}[id]
}

func basicRecovery(s script) bool {
	group := s.get("item group name")
	if group != "" && group != "`potion`" || s.get("attach type") != "`[free]`" {
		return false
	}
	hp, mp := s.get("hp recovery"), s.get("mp recovery")
	return (hp != "" || mp != "") && (!s.has("hp recovery") || ordinaryRecovery.MatchString(hp)) &&
		(!s.has("mp recovery") || ordinaryRecovery.MatchString(mp))
}

var avatarCondition = regexp.MustCompile("^`(?:<|>|<=|>=)`\\s+[0-9]+$")

func validAvatarCondition(s string) bool { return avatarCondition.MatchString(s) }

type rawRequirement struct {
	kind  byte
	value int32
	text  string
}

func parseQualification(s script, kind byte, jobs uint16, raw []byte, resolve func(int32) (string, error)) (WeaponQualification, error) {
	q := WeaponQualification{Verified: true, Jobs: []weaponJob{}, Valid: true}
	open, close, scalar := s.values("required job skill"), s.values("/required job skill"), s.values("required skill")
	if len(open) == 0 && len(close) == 0 && len(scalar) == 0 && !s.has("/required skill") {
		return q, nil
	}
	if kind != 0 || len(raw)%5 != 0 {
		return q, errors.New("Weapon required-job-skill field must be unique and native")
	}
	rows := make([]rawRequirement, len(raw)/5)
	labels := map[string][]int{}
	for i := range rows {
		data := raw[i*5:]
		row := rawRequirement{kind: data[0], value: int32(binary.LittleEndian.Uint32(data[1:]))}
		if row.kind == 3 || row.kind == 6 {
			var err error
			row.text, err = resolve(row.value)
			if err != nil {
				return q, err
			}
		}
		if row.kind == 3 {
			labels[row.text] = append(labels[row.text], i)
		}
		rows[i] = row
	}
	start, end := labels["[required job skill]"], labels["[/required job skill]"]
	if len(scalar) != 0 || len(labels["[required skill]"])+len(labels["[/required skill]"]) != 0 {
		if len(start)+len(end)+len(open)+len(close) > 0 {
			return q, errors.New("Weapon cannot combine scalar and per-job skill qualifications")
		}
		if len(scalar) == 0 {
			return q, errors.New("Legacy weapon skill differs from its verified native job family")
		}
		count, err := countFieldsInScope(s, "required skill", nil)
		if err != nil {
			return q, err
		}
		if count != 1 {
			return q, errors.New("Legacy weapon skill differs from its verified native job family")
		}
		v, err := exactUint(scalar[0], 16)
		var family uint16
		switch v {
		case 33:
			family = 1<<0 | 1<<9
		case 95:
			family = 1 << 11
		case 34:
			family = 1<<1 | 1<<7
		case 91:
			family = 1<<3 | 1<<8 | 1<<10
		case 83:
			family = 1 << 6
		}
		if err != nil || len(scalar) != 1 || jobs == 0 || jobs&family != jobs {
			return q, errors.New("Legacy weapon skill differs from its verified native job family")
		}
		lit := labels["[required skill]"]
		if len(lit) != 1 || lit[0]+1 >= len(rows) || rows[lit[0]+1].kind != 0 || rows[lit[0]+1].value != int32(v) ||
			lit[0]+2 < len(rows) && rows[lit[0]+2].kind != 3 || len(labels["[/required skill]"]) != 0 {
			return q, errors.New("Legacy weapon skill text and native scalar do not agree")
		}
		for i := range 16 {
			if jobs&(1<<i) != 0 {
				q.Jobs = append(q.Jobs, weaponJob{Job: byte(i), Skill: uint16(v)})
			}
		}
		return q, nil
	}
	if len(open) != 1 || len(close) != 1 || len(start) != 1 || len(end) != 1 {
		return q, errors.New("Weapon required-job-skill field must be unique and native")
	}
	first, last := start[0]+1, end[0]
	if last <= first || (last-first)%2 != 0 || last+1 < len(rows) && rows[last+1].kind != 3 {
		return q, errors.New("Weapon requirement must be a nonempty closed job/skill vector")
	}
	var shown []string
	var seen uint16
	for i := first; i < last; i += 2 {
		r, k := rows[i], rows[i+1]
		o := jobOrdinal(strings.TrimSuffix(strings.TrimPrefix(r.text, "["), "]"))
		if r.kind != 6 || o < 0 || r.text != "["+jobLabels[o]+"]" || k.kind != 0 || k.value <= 0 || k.value >= 512 || seen&(1<<o) != 0 {
			return q, errors.New("Weapon requirement has an unknown, duplicate or invalid literal/I32 pair")
		}
		seen |= 1 << o
		q.Jobs = append(q.Jobs, weaponJob{Job: byte(o), Skill: uint16(k.value)})
		shown = append(shown, fmt.Sprintf("`%s` %d", r.text, k.value))
	}
	if strings.Join(strings.Fields(open[0]), " ") != strings.Join(shown, " ") {
		return q, errors.New("Decoded weapon requirement differs from its complete native vector")
	}
	slices.SortFunc(q.Jobs, func(a, b weaponJob) int { return int(a.Job) - int(b.Job) })
	return q, nil
}

func countFieldsInScope(s script, name string, scope []string) (int, error) {
	closes := make(map[string]bool)
	for _, f := range s.fields {
		if strings.HasPrefix(f.name, "/") {
			closes[f.name] = true
		}
	}
	var stack []string
	count := 0
	for _, f := range s.fields {
		if strings.HasPrefix(f.name, "/") {
			if len(stack) == 0 || stack[len(stack)-1] != f.name {
				return 0, errors.New("Item field has an unmatched scope")
			}
			stack = stack[:len(stack)-1]
			continue
		}
		if f.name == name && slices.Equal(stack, scope) {
			count++
		}
		closeName := "/" + f.name
		if closes[closeName] {
			stack = append(stack, closeName)
		} else if f.name == "if" || f.name == "then" || f.name == "multiple then" {
			return 0, errors.New("Item effect scope is incomplete")
		}
	}
	if len(stack) != 0 {
		return 0, errors.New("Item field has an incomplete scope")
	}
	return count, nil
}
