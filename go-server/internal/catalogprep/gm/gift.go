package gm

import (
	"errors"
	"fmt"
	"math"
	"regexp"
	"strings"
	"time"
)

var (
	couponGrammar       = regexp.MustCompile("^\x60\\[coupon\\]\\x60\\s+[1-9][0-9]*\\s+0$")
	tradeMaximum        = regexp.MustCompile("^[0-9]+$")
	reinforcementTicket = regexp.MustCompile("^(7|10|11|12|13)\\s+(50|90|100)\\s+\x60fixed\x60\\s+-1$")
)

func parseGift(id uint32, equipment bool, s script, hash string, raw []byte, resolve func(int32) (string, error)) (*ItemTemplate, error) {
	return parseGiftMode(id, equipment, s, hash, raw, resolve, GiftDefault)
}

func parseGiftMode(id uint32, equipment bool, s script, hash string, raw []byte, resolve func(int32) (string, error), mode GiftMode) (*ItemTemplate, error) {
	if !equipment && id <= 2 {
		return nil, errors.New("Gift virtual balance requires its wallet owner, not an ordinary inventory instance")
	}
	if !equipment && pinnedCard(id) != "" {
		return parseOrdinary(id, false, s, hash, raw, resolve)
	}
	monsterSource := false
	if !equipment && mode == GiftMonster {
		var err error
		monsterSource, err = monsterAcquisition(s)
		if err != nil {
			return nil, err
		}
	}
	attach, err := s.section("attach type")
	if err != nil {
		return nil, err
	}
	sealed := attach == "`[sealing]`" || equipment && attach == "`[sealing trade]`"
	calendar := false
	for _, f := range s.fields {
		name := strings.ToLower(f.name)
		if strings.Contains(name, "daily delete") || strings.Contains(name, "weekly delete") || strings.Contains(name, "monthly delete") {
			calendar = true
			break
		}
	}
	if existing, err := parseOrdinary(id, equipment, s, hash, raw, resolve); err == nil && !calendar && !s.has("stack in account common item inventory") {
		if isOculusStat(id) && !equipment {
			return nil, errors.New("Native Oculus stat potion requires its complete calendar contract")
		}
		if sealed {
			existing.PvfGiftItem, existing.InitialSealed = true, true
		}
		if !validItem(existing) {
			return nil, errors.New("Gift sealed item requires a complete equipment template")
		}
		return existing, nil
	}
	kind, err := s.section("stackable type")
	if err != nil {
		return nil, err
	}
	tickets := append(s.values("equipment reinforcement ticket"), s.values("equipment amplify reinforcement ticket")...)
	if !equipment && !monsterSource && kind == "`[etc]` 1" && len(tickets) != 0 {
		limit := s.get("stack limit")
		maximum, err := exactUint(limit, 32)
		if len(tickets) != 1 || !reinforcementTicket.MatchString(tickets[0]) || limit != "" && (err != nil || maximum == 0) ||
			s.has("action type") {
			return nil, errors.New("Reinforcement ticket acquisition metadata is outside the verified intact source family")
		}
	}
	emblem := kind == "`[avatar emblem]` 0"
	feed := kind == "`[feed]` 1"
	creature := kind == "`[creature]` 1"
	avatarSource := kind == "`[avatar gift]` 0" && s.has("avatar gift type") ||
		kind == "`[etc]` 0" && s.has("avatar select ability change")
	if emblem {
		targets := s.values("avatar emblem target type")
		if len(targets) == 0 {
			return nil, errors.New("Gift emblem socket target is unsupported")
		}
		for _, value := range targets {
			labels, err := quotedLabels(value)
			if err != nil || len(labels) == 0 {
				return nil, errors.New("Gift emblem socket target is unsupported")
			}
			for _, x := range labels {
				if _, ok := socketTypes[x]; !ok {
					return nil, errors.New("Gift emblem socket target is unsupported")
				}
			}
		}
	}
	if creature && s.has("creature minimum level") {
		if _, err := exactUint(s.get("creature minimum level"), 31); err != nil {
			return nil, errors.New("Gift creature use qualification is malformed")
		}
	}
	trade, err := s.section("trade limit")
	if err != nil {
		return nil, err
	}
	accountAfterFirst := trade != ""
	if accountAfterFirst && (!tradeLimit.MatchString(trade) || attach != "" && attach != "`[free]`") {
		return nil, errors.New("Gift acquisition trade limit requires an unsupported instance policy")
	}
	maxTrades, err := s.section("trade limit max")
	if err != nil {
		return nil, err
	}
	if s.has("trade limit max") {
		v, err := exactUint(maxTrades, 32)
		if err != nil || v > 15 {
			return nil, errors.New("Gift trade-count maximum is outside its verified fresh-instance range")
		}
	}
	for _, f := range s.fields {
		if strings.HasPrefix(f.name, "/") || !unsupportedGiftField.MatchString(f.name) {
			continue
		}
		switch f.name {
		case "maintain socket stat":
			if monsterSource {
				continue
			}
		case "expiration date", "avatar", "creature", "special avatar", "avatar package preview info", "avatar booster preview",
			"trade limit max", "daily delete item", "weekly delete item", "fatiguetime monthly delete":
			continue
		case "avatar emblem target type":
			if emblem {
				continue
			}
		case "avatar gift type", "avatar select ability change", "use only with period avatar":
			if avatarSource {
				continue
			}
		case "creature minimum level":
			if feed || creature {
				continue
			}
		case "trade limit":
			if accountAfterFirst {
				continue
			}
		}
		return nil, fmt.Errorf("Gift acquisition requires unsupported instance metadata: %s", f.name)
	}
	switch attach {
	case "", "`[free]`", "`[account]`", "`[trade]`", "`[trade delete]`", "`[sealing]`":
	default:
		if !(equipment && (attach == "`[sealing trade]`" || attach == "`[avatar trade]`") ||
			!equipment && (attach == "`[trade limit]`" || attach == "`[trade limit account]`")) {
			return nil, fmt.Errorf("Gift acquisition requires unsupported attachment or sealing state: %s", attach)
		}
	}
	expiration, err := s.section("expiration date")
	if err != nil {
		return nil, err
	}
	exp, err := parseExpiration(expiration, false)
	if err != nil {
		return nil, err
	}
	usablePeriod, err := s.section("usable period")
	if err != nil {
		return nil, err
	}
	life, err := parseLifetime(usablePeriod)
	if err != nil {
		return nil, err
	}
	if life == 0 && usablePeriod == "" && s.has("usable period") {
		return nil, errors.New("Gift relative lifetime is empty")
	}
	daily, weekly, monthly, err := parseDeletion(s, false)
	if err != nil {
		return nil, err
	}
	if monthly && (equipment || len(s.values("fatiguetime monthly delete")) != 1 || s.get("fatiguetime monthly delete") != "") {
		return nil, errors.New("Monthly deletion requires the exact empty stackable marker")
	}
	var t *ItemTemplate
	if equipment {
		plain := script{}
		for _, f := range s.fields {
			if f.name != "usable period" && f.name != "expiration date" {
				plain.fields = append(plain.fields, f)
			}
		}
		t, err = parseOrdinary(id, true, plain, hash, raw, resolve)
	} else {
		match := stackableLabel.FindStringSubmatch(kind)
		if match == nil {
			return nil, errors.New("Gift acquisition stackable subtype is unsupported")
		}
		label, sub := match[1], strings.TrimSpace(match[2])
		coupon := couponGrammar.MatchString(kind)
		recovery := label == "waste" && (sub == "1" || sub == "4") && basicRecovery(s)
		throwable := label == "throw" && (sub == "1" || sub == "2" || sub == "3")
		monsterCard := label == "material expert job" && sub == "1" && s.get("item category") == "`monster card`"
		if monsterCard && (s.get("sub type") != "1" || s.get("minimum level") != "1" || s.get("usable job") != "`[all]`" ||
			!(attach == "" || attach == "`[free]`" || attach == "`[trade]`" || attach == "`[account]`") || s.has("action type")) {
			return nil, errors.New("Gift monster card requires its complete ordinary acquisition classification")
		}
		ordinaryMaterial := label == "material" && (sub == "2" || sub == "4")
		if _, shared := sharedSubtype(id); shared && sub == "4" {
			ordinaryMaterial = false
		}
		intactOrdinary := label == "disguise" || label == "gold kaleido" || label == "upgrade limit cube" || label == "material" && sub == "4" && ordinaryMaterial
		if intactOrdinary && mode != GiftMonster && (s.get("minimum level") != "1" || s.get("usable job") != "`[all]`") {
			return nil, errors.New("Intact gift source is outside its verified ordinary acquisition qualifications")
		}
		questTicket := qualifyingQuestTicket(s) || mode == GiftQuestTicket
		etcSource := questTicket || s.get("action type") == "`[level up ticket]`" || len(tickets) != 0 || supportedActionSource(s)
		lottery := (mode == GiftShop || mode == GiftBattlePass || monsterSource) && label == "upgradable legacy" && sub == "1"
		monsterOrdinary := mode == GiftMonster && (label == "material" && (sub == "2" || sub == "3" || sub == "4" || sub == "8" || sub == "10") || label == "material expert job" && sub == "1" || label == "waste" && (sub == "1" || sub == "3" || sub == "4" || sub == "5"))
		switch sub {
		case "0":
		case "1":
			allow := label == "booster" || label == "material" || label == "feed" || label == "recipe" || creature || monsterCard ||
				recovery || throwable || label == "etc" && (etcSource || isExperienceBook(id)) || label == "waste" && isOculusStat(id) ||
				label == "random upgradable legacy" && s.get("action type") == "`[randombox]` 1" ||
				label == "lucky box" && s.get("action type") == "`[ancient hero lucky box]`" ||
				label == "ex book" || lottery
			if !allow && !monsterOrdinary && !monsterSource {
				return nil, errors.New("Gift acquisition stackable subtype requires an unsupported item owner")
			}
		case "2", "3", "4":
			if !(ordinaryMaterial || recovery || throwable || monsterOrdinary) {
				return nil, errors.New("Gift acquisition stackable subtype requires an unsupported item owner")
			}
		case "6":
			if label != "booster selection" {
				return nil, errors.New("Gift acquisition stackable subtype requires an unsupported item owner")
			}
		case "1000":
			if label != "item upgrad buff usual upgrade" && label != "item upgrad buff amplify upgrade" {
				return nil, errors.New("Gift acquisition stackable subtype is unsupported")
			}
			if !validUpgradeBuff(s, label) {
				return nil, errors.New("Upgrade buff reward is outside its verified intact source family")
			}
		default:
			if !coupon && !monsterOrdinary {
				return nil, errors.New("Gift acquisition stackable subtype is unsupported")
			}
		}
		storage := "waste"
		switch label {
		case "material", "quest", "material expert job":
			storage = label
		case "only effect":
			if !monsterSource {
				return nil, errors.New("Gift acquisition requires an unsupported special item owner")
			}
		case "recipe":
			if sub != "1" && (sub != "0" || mode != GiftMonster) {
				return nil, errors.New("Gift acquisition requires an unsupported special item owner")
			}
		case "throw":
			if !throwable {
				return nil, errors.New("Gift acquisition requires an unsupported special item owner")
			}
		case "unlimited waste":
			if sub != "0" || s.has("action type") {
				return nil, errors.New("Gift acquisition requires an unsupported special item owner")
			}
		case "avatar emblem", "feed", "creature", "random upgradable legacy", "lucky box", "coupon", "disguise",
			"gold kaleido", "upgrade limit cube", "avatar gift", "waste", "etc", "expert town potion",
			"booster selection", "booster", "contract", "usable cera package", "cera package", "cera booster", "booster random",
			"enchant waste", "auto enchant", "dye", "town and dungeon", "equipment effect", "item upgrad buff usual upgrade", "item upgrad buff amplify upgrade":
		case "legacy":
			if id != 490014161 && id != 10310786 && mode != GiftBattlePass {
				return nil, errors.New("Gift acquisition requires an unsupported special item owner")
			}
		case "upgradable legacy":
			if !lottery {
				return nil, errors.New("Gift acquisition requires an unsupported special item owner")
			}
		default:
			return nil, errors.New("Gift acquisition requires an unsupported special item owner")
		}
		minimum, err := s.section("minimum level")
		if err != nil {
			return nil, err
		}
		if minimum == "" || questTicket && minimum == "0" {
			minimum = "1"
		}
		jobs, err := s.section("usable job")
		if err != nil {
			return nil, err
		}
		if (id >= 10312271 && id <= 10312274 || id >= 10312935 && id <= 10312950 || id >= 10319993 && id <= 10319997) && jobs == "`[account]`" &&
			attach == "`[account]`" && kind == "`[booster]` 0" && minimum == "1" {
			jobs = "`[all]`"
		}
		limit, err := s.section("stack limit")
		if err != nil {
			return nil, err
		}
		plain := script{fields: []field{{name: "minimum level", value: minimum}, {name: "usable job", value: jobs}, {name: "stackable type", value: "`[" + storage + "]` 0"}, {name: "stack limit", value: limit}}}
		t, err = parseOrdinary(id, false, plain, hash, raw, resolve)
		if err != nil {
			return nil, err
		}
	}
	if err != nil {
		return nil, err
	}
	if emblem {
		t.FirstBagSlot, t.LastBagSlot = 289, 360
	}
	if feed || creature {
		t.PetConsumable, t.FirstBagSlot, t.LastBagSlot = true, 216, 271
	}
	if s.has("stack in account common item inventory") {
		if equipment || kind != "`[material]` 0" || attach != "`[account]`" || len(s.values("stack in account common item inventory")) != 1 || s.get("stack in account common item inventory") != "" {
			return nil, errors.New("Account common reward requires its complete native material marker")
		}
		t.AccountCommonInventory = true
	}
	t.PvfGiftItem, t.ResourceExpiration, t.RelativeLifetimeSeconds, t.DailyDeletion, t.WeeklyDeletionDay, t.FatigueTimeMonthlyDeletion, t.AccountAfterFirstTrade, t.InitialSealed =
		true, exp, life, daily, weekly, monthly, accountAfterFirst, sealed
	if !validItem(t) {
		return nil, errors.New("Gift acquisition template is invalid")
	}
	return t, nil
}

func pinnedItem(id uint32, s script, hash string) (*ItemTemplate, error, bool) {
	kind := s.get("stackable type")
	t := &ItemTemplate{TemplateId: id, Kind: 2, FirstBagSlot: 65, LastBagSlot: 120, WornSlot: math.MaxUint16, StackLimit: math.MaxUint32,
		MinimumLevel: 1, UsableJobs: math.MaxUint16, ResourceHash: hash, WeaponQualification: WeaponQualification{Jobs: []weaponJob{}, Valid: true}}
	var expected string
	switch id {
	case 10306388, 10306389, 10306390, 10312155, 10164956, 10164957, 10164958, 10164959, 10162991, 10162992, 10162993, 10162994:
		if kind != "`[quest]` 2" || s.get("grade") != "1" || s.get("rarity") != "1" || s.get("usable job") != "`[all]`" ||
			s.get("attach type") != "`[trade]`" || s.get("minimum level") != "1" || s.get("weight") != "10" {
			return nil, errors.New("Collection quest item resource is unsupported"), true
		}
		t.FirstBagSlot, t.LastBagSlot = 177, 232
		return t, nil, true
	case 10162711:
		if kind != "`[throw]` 2" || s.get("usable job") != "`[all]`" || s.get("attach type") != "`[trade]`" ||
			s.get("minimum level") != "0" || s.get("unlimited waste") != "1" || s.get("cool time") != "1500" || s.get("weight") != "0" ||
			s.get("price") != "0" || strings.Join(strings.Fields(s.get("int data")), " ") != "76328 0 0 450 0 100" || s.get("stack limit") != "" {
			return nil, errors.New("Quest spice acquisition resource is unsupported"), true
		}
		t.WeaponQualification.Verified = true
		return t, nil, true
	case 490708126:
		expected = "`[random disguise]` 0"
	case 490708133:
		expected = "`[random upgradable legacy]` 1"
	case 490708134:
		expected = "`[material]` 1"
		t.FirstBagSlot, t.LastBagSlot = 121, 176
	case 490709750:
		expected = "`[waste]` 0"
		t.CashPackageReward = true
	case 490709751, 490709752:
		expected = "`[expert town potion]` 0"
		t.CashPackageReward = true
	case 490709586:
		expected = "`[coupon]` 81 0"
		t.CashPackageReward = true
	case 490709620, 490709621, 490709622, 490709623, 490709624, 490709625, 490709626, 490709627:
		expected = "`[booster selection]` 0"
		t.CashPackageReward = true
	case 490709628:
		expected = "`[material]` 0"
		t.CashPackageReward = true
		t.FirstBagSlot, t.LastBagSlot = 121, 176
	default:
		if kind != "`[usable cera package]` 0" {
			return nil, nil, false
		}
		expected = kind
		t.CashPackage = true
	}
	if kind != expected || s.get("usable job") != "`[all]`" || s.get("attach type") != "`[account]`" {
		return nil, errors.New("Cash or disguise grant requires its intact account-bound resource type"), true
	}
	level, err := parseInteger(s.get("minimum level"), 1, 255, 1)
	if err != nil {
		return nil, err, true
	}
	stack, err := parseInteger(s.get("stack limit"), 1, math.MaxUint32, math.MaxUint32)
	if err != nil {
		return nil, err, true
	}
	t.MinimumLevel, t.StackLimit = byte(level), uint32(stack)
	if t.CashPackage || t.CashPackageReward {
		raw := strings.Trim(s.get("expiration date"), "`")
		date, err := time.ParseInLocation("2006-01-02 15:04:05", raw, utcPlus8)
		if err != nil {
			return nil, errors.New("Cash package absolute resource expiry is unavailable"), true
		}
		value := date.Unix()
		t.ResourceExpiration = &value
	}
	if t.CashPackage {
		values := strings.Fields(s.get("package data"))
		if len(values) < 2 || len(values) > 510 || len(values)%2 != 0 {
			return nil, errors.New("Cash package complete contents are invalid"), true
		}
		for _, v := range values {
			x, e := exactUint(v, 32)
			if e != nil || x == 0 || x == math.MaxUint32 {
				return nil, errors.New("Cash package complete contents are invalid"), true
			}
		}
	}
	return t, nil, true
}

func qualifyingQuestTicket(s script) bool {
	for _, f := range s.fields {
		switch f.name {
		case "clear", "clear quest", "quest clear", "clear all quest", "clear all prev quest", "clear all pre quest":
			return true
		}
	}
	return false
}

func supportedActionSource(s script) bool {
	name := s.get("action type")
	return name == "`[open creature skin slot]`" || strings.HasPrefix(name, "`[change avatar]` ")
}

func validUpgradeBuff(s script, label string) bool {
	group := "351"
	if label == "item upgrad buff amplify upgrade" {
		group = "352"
	}
	minimum := s.get("minimum level")
	if s.get("usable job") != "`[all]`" || minimum != "" && minimum != "1" ||
		s.get("cool time") != "1800000" || s.get("cooltime group") != group ||
		s.get("cooltime maintenance") != "1 1 1" || s.get("effect maintenance") != "1 1 1 0" ||
		s.get("stat change duration") != "1800000 `myself`" ||
		s.get("action usable place") != "`[village]` `[seria room]`" {
		return false
	}
	for _, f := range s.fields {
		switch f.name {
		case "name", "explain", "flavor text", "grade", "rarity", "usable job", "/usable job", "attach type", "minimum level", "cool time", "cooltime group",
			"icon", "field image", "stackable type", "item group name", "move wav", "expiration date", "stack limit", "expert type",
			"cooltime maintenance", "effect maintenance", "stat change duration", "action usable place", "/action usable place":
		default:
			return false
		}
	}
	return true
}

func isOculusStat(id uint32) bool {
	switch id {
	case 10313330, 490015881, 10313331, 490015882, 10313949, 490015883:
		return true
	}
	return false
}

func isExperienceBook(id uint32) bool {
	switch id {
	case 1034, 1035, 1036, 1037, 1231, 1232:
		return true
	}
	return false
}
