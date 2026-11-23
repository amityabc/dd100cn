package commerce

import (
	"encoding/json"
	"fmt"
	"math"
	"reflect"
	"regexp"
	"slices"
	"strings"

	"s9a1/server/internal/storage"
)

var fatigueAllowed = strings.Split("name|name2|explain|basic explain|flavor text|grade|rarity|usable job|attach type|minimum level|maximum level|icon|icon mark|field image|weight|price|value|stackable type|item group name|move wav|use wav|stack limit|item category|impossible contents|expiration date|usable period|daily delete item|weekly delete item|cool time|cooltime group|cooltime maintenance|action usable place|use action packet|action type|need material|purchase limit|daily purchase limit|reset daily purchase item|total usable count", "|")
var fatigueBlocks = []string{"action type", "usable job", "item category", "impossible contents", "action usable place", "need material", "purchase limit"}
var fatigueAction = regexp.MustCompile("^`\\[add fatigue\\]`\\s+([0-9]+)$")
var fatigueWeeklyPurchase = regexp.MustCompile("^`account` `weekly` [1-9][0-9]*$")
var fatigueDailyPurchase = regexp.MustCompile("^`account` [1-9][0-9]*$")

func parseFatigueItem(src ItemSource, t *ItemTemplate, s consumableScript) (storage.FatigueItemDefinition, error) {
	var d storage.FatigueItemDefinition
	for _, f := range s.fields {
		n := strings.TrimLeft(f.Name, "/")
		if !slices.Contains(fatigueAllowed, n) || f.Name != n && !slices.Contains(fatigueBlocks, n) {
			return d, fmt.Errorf("fatigue item field unsupported: %s", n)
		}
		if _, err := s.value(n); err != nil {
			return d, err
		}
	}
	for _, name := range fatigueBlocks {
		if !s.block(name, false) {
			return d, fmt.Errorf("fatigue item block incomplete: %s", name)
		}
	}
	v := func(name string) string { value, _ := s.value(name); return value }
	if v("stackable type") != "`[expert town potion]` 0" || v("item group name") != "" && v("item group name") != "`potion`" {
		return d, fmt.Errorf("fatigue item is outside finite expert-town consumables")
	}
	a, err := src.Gift()
	if err != nil || !reflect.DeepEqual(a, t) {
		return d, fmt.Errorf("fatigue item acquisition differs")
	}
	m := fatigueAction.FindStringSubmatch(v("action type"))
	if m == nil {
		return d, fmt.Errorf("fatigue action is incomplete")
	}
	amount, err := giftInt(m[1])
	if err != nil || amount == 0 || amount > 156 {
		return d, fmt.Errorf("fatigue recovery parameters exceed the supported range")
	}
	min, e1 := s.number("minimum level", 1, 100, false)
	max, e2 := s.number("maximum level", 100, 100, false)
	packet, e3 := s.number("use action packet", 0, 1, false)
	if e1 != nil || e2 != nil || e3 != nil || min == 0 || min != uint32(t.MinimumLevel) || max < min || packet != 1 ||
		s.has("cooltime maintenance") && v("cooltime maintenance") != "1 1 1" {
		return d, fmt.Errorf("fatigue levels or action packet metadata differs")
	}
	places, err := fatigueTags(v("action usable place"), quotedPlace)
	if err != nil {
		return d, err
	}
	for _, label := range places {
		switch label {
		case "[village]", "[seria room]":
			d.UsablePlaces = append(d.UsablePlaces, strings.Trim(label, "[]"))
		case "[dont use assult]":
		default:
			return d, fmt.Errorf("fatigue usable place unsupported")
		}
	}
	if len(d.UsablePlaces) == 0 {
		return d, fmt.Errorf("fatigue source has no supported usable place")
	}
	for k, allowed := range map[string][]string{"item category": {"event"}, "impossible contents": {"gift", "ban redeemitem", "charac cargo", "hardcore mode"}} {
		values, err := fatigueTags(v(k), giftRestrictionFlag)
		if err != nil || slices.ContainsFunc(values, func(n string) bool { return !slices.Contains(allowed, n) }) {
			return d, fmt.Errorf("fatigue item has an unsupported restriction: %s", k)
		}
	}
	if s.has("need material") {
		cost, err := giftInts(v("need material"), false)
		if err != nil || len(cost)%2 != 0 || slices.ContainsFunc(cost, func(n uint32) bool { return n == 0 || n > math.MaxInt32 }) {
			return d, fmt.Errorf("fatigue purchase materials are malformed")
		}
	}
	if s.has("purchase limit") && !fatigueWeeklyPurchase.MatchString(v("purchase limit")) ||
		s.has("daily purchase limit") && !fatigueDailyPurchase.MatchString(v("daily purchase limit")) || v("reset daily purchase item") != "" {
		return d, fmt.Errorf("fatigue purchase limitations are unsupported")
	}
	daily, e1 := s.number("total usable count", 0, math.MaxUint8, false)
	cooldown, e2 := s.number("cool time", 0, math.MaxInt32, false)
	group, e3 := s.number("cooltime group", 0, math.MaxUint32, false)
	if e1 != nil || e2 != nil || e3 != nil || s.has("total usable count") && daily == 0 {
		return d, fmt.Errorf("fatigue use limit or cooldown is invalid")
	}
	d.Amount, d.MaximumLevel, d.DailyLimit, d.CooldownMilliseconds = uint16(amount), byte(max), byte(daily), cooldown
	if s.has("cooltime group") {
		d.CooldownGroup = &group
	}
	d.Template, err = json.Marshal(t)
	return d, err
}

func fatigueTags(text string, pattern *regexp.Regexp) ([]string, error) {
	out := []string{}
	if strings.TrimSpace(pattern.ReplaceAllString(text, "")) != "" {
		return nil, fmt.Errorf("fatigue tag list is malformed")
	}
	for _, m := range pattern.FindAllStringSubmatch(text, -1) {
		if slices.Contains(out, m[1]) {
			return nil, fmt.Errorf("fatigue item repeats a restriction")
		}
		out = append(out, m[1])
	}
	return out, nil
}
