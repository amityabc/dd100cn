package commerce

import (
	"math"
	"regexp"
	"slices"
	"strings"

	"s9a1/server/internal/catalogprep/gm"
	"s9a1/server/internal/storage"
)

var beadAllowedTags = strings.FieldsFunc("name|explain|flavor text|grade|rarity|usable job|/usable job|minimum level|weight|price|sell price|cool time|icon|field image|stackable type|item group name|move wav|monster card id|/monster card id|enchant index|/enchant index|trade limit|attach type|stack limit|expert type|target item id|/target item id|bead limited usable item|/bead limited usable item|expiration date", func(c rune) bool { return c == '|' })
var craftCardString = regexp.MustCompile("`([^`\\r\\n]+)`")

func readCraftBead(items *Items, id uint32, path, text string) (storage.CraftBead, bool) {
	var out storage.CraftBead
	p := craftParser{name: "enchant"}
	fields, err := gm.ItemFields(text)
	if err != nil || p.field(fields, "stackable type", true) != "`[enchant waste]` 0" {
		return out, false
	}
	if id == 0 || id > math.MaxInt32 {
		return out, false
	}
	level := p.field(fields, "minimum level", true)
	if (level != "" && level != "1") || p.field(fields, "maximum level", true) != "" ||
		p.field(fields, "usable job", false) != "`[all]`" {
		return out, false
	}
	for _, f := range fields {
		if !slices.Contains(beadAllowedTags, f.Name) {
			return out, false
		}
	}
	expl := p.field(fields, "monster card id", true)
	fallback := p.field(fields, "enchant index", true)
	if expl != "" && fallback != "" {
		return out, false
	}
	if expl == "" {
		expl = fallback
	}
	rawID, ok := craftUnsigned(expl, math.MaxInt32)
	cardID := uint32(rawID)
	if !ok || rawID == 0 {
		return out, false
	}
	cardPath := items.stackable[int64(cardID)]
	if cardPath == "" {
		return out, false
	}
	cardText, cardHash, err := sourceTextAndHash(items.source, cardPath)
	if err != nil {
		return out, false
	}
	card := p.script(cardText)
	if p.field(card, "stackable type", false) != "`[material expert job]` 1" {
		return out, false
	}
	readStrings := func(s string) []string {
		out := []string{}
		at := 0
		for _, span := range craftCardString.FindAllStringSubmatchIndex(s, -1) {
			p.check(strings.TrimSpace(s[at:span[0]]) == "", "invalid card string list")
			out = append(out, strings.TrimSpace(s[span[2]:span[3]]))
			at = span[1]
		}
		p.check(strings.TrimSpace(s[at:]) == "", "invalid card string tail")
		return out
	}
	category := readStrings(p.field(card, "item category", false))
	if len(category) != 1 {
		return out, false
	}
	category[0] = strings.ToLower(category[0])
	if category[0] != "monster card" && !strings.HasPrefix(category[0], "monster card ") {
		return out, false
	}
	kinds := readStrings(p.field(card, "string data", false))
	if len(kinds) < 2 || kinds[0] == "" {
		return out, false
	}
	kinds = kinds[1:]
	seen := map[string]bool{}
	for i, kind := range kinds {
		kinds[i] = strings.ToLower(kind)
		if craftEquipSlots[kinds[i]] == 0 || seen[kinds[i]] {
			return out, false
		}
		seen[kinds[i]] = true
	}
	parseTargetIDs := func(tag string) []uint32 {
		ids := []uint32{}
		for _, token := range strings.Fields(p.field(fields, tag, true)) {
			raw, ok := craftUnsigned(token, math.MaxInt32)
			id := uint32(raw)
			p.check(ok && id != 0 && !slices.Contains(ids, id), "invalid enchant target ID")
			ids = append(ids, id)
		}
		return ids
	}
	levels := craftEnchantLevels(&p, card)
	targets, limited := parseTargetIDs("target item id"), parseTargetIDs("bead limited usable item")
	if p.err != nil {
		return out, false
	}
	_, hash, err := sourceTextAndHash(items.source, path)
	if err != nil {
		return out, false
	}
	return storage.CraftBead{BeadTemplateID: id, CardTemplateID: cardID, UpgradeLevels: levels, EquipmentTypes: kinds,
		TargetTemplateIDs: targets, LimitedTemplateIDs: limited, ResourceHash: hash, CardResourceHash: cardHash}, true
}

func craftEnchantLevels(p *craftParser, fields []gm.Field) []uint16 {
	levels := []uint16{}
	table, index, seen := false, false, false
	for _, f := range fields {
		switch {
		case f.Name == "enchant table":
			p.check(!table && !seen && f.Value == "", "nested or duplicate enchant table")
			table, seen = true, true
		case f.Name == "/enchant table":
			p.check(table && !index && f.Value == "", "unmatched enchant table")
			table = false
		case f.Name == "enchant index" && table:
			n, ok := craftUnsigned(f.Value, math.MaxUint8)
			p.check(!index && ok && !slices.Contains(levels, uint16(n)), "invalid enchant index")
			levels = append(levels, uint16(n))
			index = true
		case f.Name == "/enchant index" && table:
			p.check(index && f.Value == "", "unmatched enchant index")
			index = false
		}
	}
	p.check(!table && !index && (!seen || len(levels) > 0), "incomplete enchant table")
	if !seen {
		return []uint16{0}
	}
	slices.Sort(levels)
	return levels
}
