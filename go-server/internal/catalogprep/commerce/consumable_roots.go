package commerce

import (
	"encoding/json"
	"fmt"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"s9a1/server/internal/catalogprep/gm"
	"s9a1/server/internal/storage"
)

var couponIDs = []uint32{
	10149709, 10152156, 10152158, 10157925, 10159825, 10164965, 2660237, 2660417, 10007903, 10008055,
	490704516, 490705127, 490705128, 490705695, 490705696, 490706543, 490706544, 490707055, 490707056,
	490707383, 490707491, 490707734, 490707737, 490708957, 490708960, 490709106, 490709307, 490709309,
	490709471, 490709474, 490709683, 10092474, 10093791, 490001934, 490002122, 490002123, 10302218,
	490006980, 490013842, 490014422, 490016592, 490016927, 490017198, 10313712, 490017739, 490018087,
	10314763, 490018853, 490019000, 490019490, 490020053, 490020182, 490020582, 490020816, 490021480,
	490021862, 490022521, 490022589, 10318918, 490022725, 490023078, 490023311, 10320798,
}

var couponFields = []string{
	"name", "explain", "flavor text", "grade", "rarity", "usable job", "/usable job", "attach type",
	"minimum level", "icon", "icon mark", "stackable type", "move wav", "stack limit", "action type", "/action type",
	"action usable place", "/action usable place", "expiration date", "usable period", "impossible contents", "/impossible contents",
	"item category", "/item category",
}

var quotedPlace = regexp.MustCompile("`(\\[[^`]+\\])`")

func BuildContainerCoupons(items *Items) (map[uint32]storage.ContainerCoupon, error) {
	out := map[uint32]storage.ContainerCoupon{}
	for _, id := range couponIDs {
		src, err := items.Source(id)
		if err != nil {
			return nil, err
		}
		fields, err := gm.ItemFields(src.Text)
		if err != nil || !strictFieldSet(src.Text, couponFields) || cashField(src, "stackable type") != "`[etc]` 0" ||
			!slices.ContainsFunc(fields, func(f gm.Field) bool { return f.Name == "/action type" }) {
			continue
		}
		action := cashField(src, "action type")
		if action != "`[upgrade cargo]`" && action != "`[upgrade inventory]`" {
			continue
		}
		place := cashField(src, "action usable place")
		places := map[string]bool{}
		valid := true
		for _, p := range quotedPlace.FindAllStringSubmatch(place, -1) {
			if places[p[1]] || p[1] != "[village]" && p[1] != "[seria room]" && p[1] != "[dont use assult]" {
				valid = false
				break
			}
			places[p[1]] = true
		}
		if !valid || len(places) == 0 || strings.TrimSpace(quotedPlace.ReplaceAllString(place, "")) != "" {
			continue
		}
		t, err := src.Gift()
		if err != nil || t.Kind != 2 || t.FirstBagSlot != 65 || t.LastBagSlot != 120 {
			continue
		}
		raw, err := json.Marshal(t)
		if err != nil {
			return nil, err
		}
		out[id] = storage.ContainerCoupon{Template: raw, Cargo: action == "`[upgrade cargo]`", Village: places["[village]"],
			Seria: places["[seria room]"], ForbidScene8: places["[dont use assult]"]}
	}
	return out, nil
}

func BuildCargoGift(items *Items) (*ItemTemplate, error) {
	const id = 490709682
	const hash = "0DAA9FDDEE456F543BE4D58D085926E1DADDFA61B7AC62CD2D7064193442BD68"
	src, err := items.Source(id)
	if err != nil {
		return nil, err
	}
	if !strings.EqualFold(src.Hash, hash) {
		return nil, fmt.Errorf("cargo stage gift source revision is unverified")
	}
	allowed := []string{"name", "explain", "flavor text", "grade", "rarity", "usable job", "/usable job", "attach type", "minimum level",
		"icon", "stackable type", "move wav", "expiration date", "stack limit", "booster info", "etc", "/etc", "/booster info", "hide booster info",
		"action usable place", "/action usable place"}
	if !strictFieldSet(src.Text, allowed) || cashField(src, "stackable type") != "`[booster]` 0" || cashField(src, "etc") != "1 490709687 30000 1" ||
		cashField(src, "hide booster info") != "1" || cashField(src, "action usable place") != "`[seria room]` `[village]` `[dont use assult]`" {
		return nil, fmt.Errorf("cargo stage gift complete definition differs")
	}
	t, err := src.Gift()
	if err != nil {
		return nil, err
	}
	if t.Kind != 2 || t.FirstBagSlot != 65 || t.LastBagSlot != 120 || t.ResourceExpiration == nil {
		return nil, fmt.Errorf("cargo stage gift source metadata is incomplete")
	}
	return t, nil
}

type CashPackageContent struct {
	Template *ItemTemplate
	Count    uint32
}

type CashPackageUse struct {
	Template *ItemTemplate
	Contents []CashPackageContent
}

var cashPackageChildren = map[uint32][]uint32{
	490709749: {490709750, 1, 490709751, 2, 490709752, 1},
	490709585: {490709620, 1, 490709621, 1, 490709622, 1, 490709623, 1, 490709624, 1, 490709625, 1,
		490709626, 1, 490709627, 1, 490709628, 5, 490709586, 1},
}

func BuildCashPackageUse(items *Items) (map[uint32]CashPackageUse, error) {
	out := map[uint32]CashPackageUse{}
	for id, expected := range cashPackageChildren {
		if items.stackable[int64(id)] == "" {
			continue
		}
		src, err := items.Source(id)
		if err != nil {
			return nil, err
		}
		raw, err := commerceBlock(src.Text, "package data")
		if err != nil {
			return nil, err
		}
		parts := numbers(raw)
		if len(parts) != len(expected) {
			return nil, fmt.Errorf("cash package %d reward list differs", id)
		}
		for idx, part := range parts {
			value, err := strconv.ParseFloat(part, 64)
			if err != nil || value != float64(expected[idx]) {
				return nil, fmt.Errorf("cash package %d reward list differs", id)
			}
		}
		pkg := CashPackageUse{}
		for i := 0; i < len(expected); i += 2 {
			child := expected[i]
			if items.stackable[int64(child)] == "" {
				pkg.Contents = nil
				break
			}
			t, err := items.Gift(child)
			if err != nil || !t.CashPackageReward {
				return nil, fmt.Errorf("cash package %d reward %d has invalid acquisition metadata", id, child)
			}
			pkg.Contents = append(pkg.Contents, CashPackageContent{t, expected[i+1]})
		}
		if pkg.Contents == nil {
			continue
		}
		pkg.Template, err = src.Gift()
		if err != nil || !pkg.Template.CashPackage {
			return nil, fmt.Errorf("cash package %d source is invalid", id)
		}
		out[id] = pkg
	}
	return out, nil
}

var contractType = regexp.MustCompile("(?i)^`\\[(etc|contract)\\]`\\s+0$")
var contractDays = regexp.MustCompile(`([0-9]+)\s*天`)

func BuildContractGrants(items *Items, ids []uint32) map[uint32]storage.GiftContract {
	out := map[uint32]storage.GiftContract{}
	localize, ok := items.source.(interface{ LocalizedText(string) (string, error) })
	if !ok {
		return out
	}
	for _, id := range ids {
		if items.stackable[int64(id)] == "" {
			continue
		}
		src, err := items.Source(id)
		if err != nil || !contractType.MatchString(cashField(src, "stackable type")) {
			continue
		}
		name, err := localize.LocalizedText(cashField(src, "name"))
		if err != nil {
			continue
		}
		var typ byte
		switch {
		case strings.Contains(name, "霸王之契约"):
			typ = 22
		case strings.Contains(name, "达人之契约"), strings.Contains(name, "达人契约"):
			typ = 27
		case strings.Contains(name, "晶之"):
			typ = 97
		case strings.Contains(name, "成长之契约"), strings.Contains(name, "成长契约"):
			typ = 84
		default:
			continue
		}
		parts := contractDays.FindStringSubmatch(name)
		if len(parts) == 0 {
			continue
		}
		days, err := strconv.ParseUint(parts[1], 10, 32)
		if err == nil && days > 0 && days <= 3650 {
			out[id] = storage.GiftContract{PremiumType: typ, Days: uint32(days)}
		}
	}
	return out
}
