package quest

import (
	"encoding/json"
	"math"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"s9a1/server/internal/catalogprep/gm"
	"s9a1/server/internal/questwire"
)

var experienceBooks = map[uint32]uint32{1034: 100, 1035: 1000, 1036: 10000, 1037: 100000, 1231: 1000000, 1232: 10000000}
var experienceAction = regexp.MustCompile("^\x60\\[(expUp|expUpByPercent|expUpByTable)\\]\x60\\s+(\\d+(?:\\s+\\d+)*)$")
var weeklyPurchase = regexp.MustCompile("^\x60account\x60 \x60weekly\x60 [1-9]\\d*$")
var dailyPurchase = regexp.MustCompile("^\x60account\x60 ([1-9]\\d*)$")
var levelTicketContents = regexp.MustCompile("^(?:\x60(?:hardcore mode|gift|ban redeemitem)\x60\\s*)*$")
var capsuleEffect = regexp.MustCompile("^\x60\\[levelUpByTable\\]\x60\\s+(\\d+)\\s+(\\d+)\\s+(\\d+)$")

type expBand = struct {
	MinimumLevel, MaximumLevel byte
	Amount                     uint32
}

var bookFields = strings.FieldsFunc("name|explain|grade|attach type|rarity|weight|usable job|/usable job|minimum level|stack limit|icon|field image|stackable type|price|value|move wav|use wav", func(r rune) bool { return r == '|' })
var experienceFields = strings.Split("name|name2|explain|basic explain|flavor text|grade|rarity|usable job|attach type|minimum level|maximum level|icon|icon mark|field image|weight|price|value|stackable type|item group name|move wav|use wav|stack limit|increase status type|impossible jobs|item category|impossible contents|expiration date|usable period|daily delete item|weekly delete item|cool time|cooltime group|action usable place|display exp up info|need material|purchase limit", "|")
var levelFields = strings.Split("name|explain|flavor text|grade|rarity|usable job|attach type|cool time|icon|icon mark|stackable type|move wav|minimum level|maximum level|action type|usable game server type|levelup range|stack limit|impossible contents|expiration date|usable period|daily delete item|weekly delete item", "|")
var capsuleFields = strings.Split("name|explain|flavor text|grade|rarity|usable job|attach type|minimum level|icon|icon mark|field image|stackable type|item group name|move wav|maximum level|expiration date|action type|increase status type|action usable place|display exp up result level|display new area ui|stack limit|price|value|cool time|cooltime group|usable period|item category|impossible contents|need material|daily purchase limit", "|")

func experienceItem(id uint32, text string, template *gm.ItemTemplate, raw json.RawMessage) (questwire.ExperienceItem, error) {
	s := readItemScript(text)
	var out questwire.ExperienceItem
	out.Template, out.MaximumLevel, out.Bands, out.UsablePlaces = raw, 99, []expBand{}, []string{}
	if template.Kind != 2 || template.FirstBagSlot != 65 {
		s.fail("native experience template does not use consumable slots")
	}
	if value := experienceBooks[id]; value != 0 {
		s.allowed(bookFields, false, true)
		s.balanced("usable job")
		if s.count("usable job") != 1 || s.get("stackable type") != "\x60[etc]\x60 1" {
			s.fail("invalid native experience book core")
		}
		s.number("price", 0, math.MaxUint32, false)
		s.number("value", 0, math.MaxUint32, false)
		out.NativeBook, out.Value = true, value
		return out, s.err
	}
	s.allowed(experienceFields, true, false)
	s.balanced("increase status type", "usable job", "impossible jobs", "impossible contents", "item category", "action usable place", "need material", "purchase limit")
	if s.get("stackable type") != "\x60[etc]\x60 0" || !anyOf(s.get("item group name"), "", "\x60potion\x60") {
		s.fail("invalid native experience item core")
	}
	effect := experienceAction.FindStringSubmatch(s.get("increase status type"))
	if len(effect) != 3 {
		s.fail("unknown experience effect")
		return out, s.err
	}
	values := s.numbers(effect[2])
	if len(values) == 0 || slices.ContainsFunc(values, func(n uint32) bool { return n == 0 || n > math.MaxInt32 }) {
		s.fail("invalid native experience arguments")
		return out, s.err
	}
	switch effect[1] {
	case "expUpByPercent":
		out.Kind = 1
	case "expUpByTable":
		out.Kind = 2
	}
	if out.Kind == 2 {
		if len(values)%3 != 0 || len(values) > 300 {
			s.fail("invalid experience level rows")
			return out, s.err
		}
		var last uint32
		for row := range slices.Chunk(values, 3) {
			if row[0] <= last || row[0] > row[1] || row[1] > 100 {
				s.fail("overlapping experience level bands")
			}
			out.Bands = append(out.Bands, expBand{byte(row[0]), byte(row[1]), row[2]})
			last = row[1]
		}
	} else {
		if len(values) != 1 {
			s.fail("invalid number of experience effect values")
		}
		out.Value = values[0]
	}
	minimum, maximum := s.number("minimum level", 1, 100, false), s.number("maximum level", 99, 100, false)
	if minimum == 0 || minimum != uint32(template.MinimumLevel) || maximum < minimum {
		s.fail("invalid experience item level thresholds")
	}
	s.number("display exp up info", 0, 1, false)
	out.MaximumLevel = byte(maximum)
	out.CooldownMilliseconds = s.number("cool time", 0, math.MaxInt32, false)
	if s.count("cooltime group") > 0 {
		group := s.number("cooltime group", 0, math.MaxUint32, false)
		out.CooldownGroup = &group
	}
	out.UsablePlaces = s.tags("action usable place", true, "village", "seria room", "dungeon")
	s.tags("impossible contents", false, "hardcore mode", "gift", "ban redeemitem", "charac cargo")
	s.tags("item category", false, "event")
	s.purchaseMaterial(math.MaxInt32, false)
	if s.count("purchase limit") != 0 && !weeklyPurchase.MatchString(s.get("purchase limit")) {
		s.fail("invalid experience purchase limit")
	}
	out.ImpossibleJobs = s.jobs("impossible jobs", true)
	return out, s.err
}

func levelItem(text string, raw json.RawMessage) (questwire.LevelTicket, error) {
	s := readItemScript(text)
	s.allowed(levelFields, true, true)
	s.balanced("usable job", "action type", "impossible contents")
	if s.get("stackable type") != "\x60[etc]\x60 1" ||
		s.get("action type") != "\x60[level up ticket]\x60" ||
		s.get("levelup range") != "1" || !anyOf(s.get("usable game server type"), "", "-1") ||
		!levelTicketContents.MatchString(s.get("impossible contents")) {
		s.fail("unsupported level ticket action")
	}
	minimum, maximum := s.number("minimum level", 1, 99, false), s.number("maximum level", 99, 99, false)
	if minimum == 0 || maximum < minimum {
		s.fail("invalid level ticket restrictions")
	}
	return questwire.LevelTicket{Template: raw, MaximumLevel: byte(maximum), CooldownMilliseconds: s.number("cool time", 0, math.MaxInt32, false)}, s.err
}

func capsule(text string, template *gm.ItemTemplate, raw json.RawMessage) (questwire.CapsuleItem, error) {
	s := readItemScript(text)
	var out questwire.CapsuleItem
	out.Template, out.NativeAction = raw, 193
	s.allowed(capsuleFields, true, true)
	s.balanced("usable job", "action type", "increase status type", "action usable place", "item category", "impossible contents", "need material")
	if !template.PvfGiftItem || template.Kind != 2 || template.FirstBagSlot != 65 || template.LastBagSlot != 120 ||
		s.get("stackable type") != "\x60[etc]\x60 0" || s.get("action type") != "\x60[jumping lv]\x60" || !anyOf(s.get("item group name"), "", "\x60potion\x60") {
		s.fail("unsupported capsule item core")
	}
	minimum, maximum := s.number("minimum level", 1, 99, false), s.number("maximum level", 0, 99, true)
	if row := capsuleEffect.FindStringSubmatch(s.get("increase status type")); len(row) == 4 {
		target := s.numbers(strings.Join(row[1:], " "))
		if len(target) != 3 || target[0] == 0 || target[0] > target[1] || target[1] != maximum || target[2] <= maximum ||
			target[2] > 100 || minimum != uint32(template.MinimumLevel) || minimum < target[0] || minimum > maximum {
			s.fail("invalid capsule target range")
		} else {
			out.TargetLevel = byte(target[2])
		}
	} else {
		s.fail("unknown capsule growth effect")
	}
	places := s.tags("action usable place", false, "[seria room]", "[dont use assult]")
	out.ForbidScene8 = slices.Contains(places, "[dont use assult]")
	if !slices.Contains(places, "[seria room]") {
		s.fail("capsule lacks native room qualification")
	}
	s.tags("impossible contents", false, "ban redeemitem", "hardcore mode")
	s.tags("item category", false, "event")
	s.number("display exp up result level", 1, 1, false)
	if s.number("display new area ui", 0, 0, false) != 0 {
		s.fail("unsupported capsule new area projection")
	}
	s.purchaseMaterial(math.MaxInt32, false)
	if s.count("daily purchase limit") != 0 {
		m := dailyPurchase.FindStringSubmatch(s.get("daily purchase limit"))
		if len(m) != 2 {
			s.fail("invalid capsule purchase limit")
		} else if _, err := strconv.ParseUint(m[1], 10, 31); err != nil {
			s.fail("invalid capsule purchase limit")
		}
	}
	out.MaximumLevel, out.UsablePlaces = byte(maximum), []string{"seria room"}
	out.CooldownMilliseconds = s.number("cool time", 0, math.MaxInt32, false)
	if s.count("cooltime group") > 0 {
		group := s.number("cooltime group", 0, math.MaxUint32, true)
		out.CooldownGroup = &group
	}
	return out, s.err
}
