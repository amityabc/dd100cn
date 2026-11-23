package dungeons

import (
	"cmp"
	"context"
	"fmt"
	"maps"
	"math"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"s9a1/server/internal/catalogprep/commerce"
	"s9a1/server/internal/catalogprep/gm"
	"s9a1/server/internal/catalogprep/pvftext"
)

type NativeDropCandidate struct {
	Id                          uint32
	Grade, MinimumLevel, Rarity int
	CreationWeight              uint32
	DefinitionHash              string
	Template                    *gm.ItemTemplate
	Equipment                   bool
	RequiresMagicSeal           bool                `json:",omitempty"`
	CanRandomUpgrade            bool                `json:",omitempty"`
	Recipe                      bool                `json:",omitempty"`
	Creature                    bool                `json:",omitempty"`
	Artifact                    *gm.SpecialTemplate `json:",omitempty"`
	EquipmentType               string              `json:"-"`
}

type dropTag struct {
	name       string
	start, end int
}

func dropTags(text string) ([]dropTag, bool) {
	var tags []dropTag
	for at := 0; at < len(text); at++ {
		switch text[at] {
		case 0x60:
			close := strings.IndexByte(text[at+1:], 0x60)
			if close < 0 {
				return nil, false
			}
			at += close + 1
		case '[':
			end := strings.IndexByte(text[at+1:], ']')
			if end < 0 || end+1 > 130 {
				return nil, false
			}
			end += at + 1
			name := strings.TrimSpace(text[at+1 : end])
			if name == "" || strings.ContainsAny(name, "[\x60\r\n") || len(tags) >= 4096 {
				return nil, false
			}
			tags = append(tags, dropTag{name, at, end + 1})
			at = end
		}
	}
	return tags, true
}
func tagValue(text string, tags []dropTag, i int) string {
	end := len(text)
	if i+1 < len(tags) {
		end = tags[i+1].start
	}
	return strings.TrimSpace(text[tags[i].end:end])
}
func evolutionSource(text string, t []dropTag) bool {
	start, end := -1, -1
	for i, v := range t {
		switch v.name {
		case "emancipate":
			if start != -1 {
				return false
			}
			start = i
		case "/emancipate":
			if end != -1 {
				return false
			}
			end = i
		}
	}
	if start == -1 || end <= start || tagValue(text, t, start) != "" || tagValue(text, t, end) != "" {
		return false
	}
	fields := make([]string, 0, end-start-1)
	for _, f := range t[start+1 : end] {
		fields = append(fields, f.name)
	}
	withPairs := slices.Equal(fields, []string{"input", "/input", "output", "/output", "emancipate type"})
	if !withPairs && !slices.Equal(fields, []string{"emancipate type"}) {
		return false
	}
	if _, err := strconv.ParseUint(tagValue(text, t, end-1), 10, 31); err != nil {
		return false
	}
	if withPairs {
		for _, at := range []int{start + 1, start + 3} {
			v := strings.Fields(tagValue(text, t, at))
			if len(v) == 0 || len(v) > 512 || len(v)%2 != 0 || tagValue(text, t, at+1) != "" {
				return false
			}
			for _, n := range v {
				if i, e := strconv.ParseUint(n, 10, 31); e != nil || i == 0 {
					return false
				}
			}
		}
	}
	return true
}

var unsupportedDropTag = regexp.MustCompile(`(?i)^(required(?:\s|$)|permission(?:\s|$)|authority(?:\s|$)|binding(?:\s|$)|account(?:\s|$)|character(?:\s|$)|expiration|expiry|usable period|emancipate|equipment duration$|socket|magic option|unseal)`)
var canUpgradeType = regexp.MustCompile("^\x60\\[(weapon|coat|shoulder|pants|shoes|waist|amulet|wrist|ring|support|magic stone|earring)\\]\x60(?:\\s+\\d+)?$")
var noUpgrade = regexp.MustCompile(`(?im)^[ \t]*\[(?:upgrade impossible|cannot upgrade|no upgrade)\]`)
var nativeStackable = regexp.MustCompile("^\x60\\[(?:material|waste|recipe|throw)\\]\x60(?:\\s+(?:0|1|2|3|4|8|10))?$")
var throwDrop = regexp.MustCompile("^\x60\\[throw\\]\x60\\s+[123]$")
var creatureType = regexp.MustCompile("^\x60\\[(?:creature|artifact red|artifact blue|artifact green)\\]\x60(?:\\s|$)")
var recipeType = regexp.MustCompile("^\x60\\[recipe\\]\x60(?:\\s|$)")
var upgradeWord = regexp.MustCompile("(?i)\x60upgrade\x60")
var attachLiteral = regexp.MustCompile("^\x60\\[([^\\]]+)\\]\x60$")

// A disabled item leaves the pool. An unavailable acquisition stays in the
// lottery with its original weight and produces an empty selection.
func dropAcquisition(text string, tags []dropTag, equipment bool) (disabled, valid, sealed, magic bool) {
	closes := map[string]bool{}
	attach, random := "", ""
	seenAttach, seenRandom := false, false
	valid = true
	for i, t := range tags {
		n := strings.ToLower(t.name)
		if strings.HasPrefix(n, "/") {
			closes[n] = true
		}
		switch n {
		case "attach type":
			if seenAttach {
				valid = false
			}
			seenAttach = true
			attach = tagValue(text, tags, i)
		case "random option":
			if seenRandom {
				valid = false
			}
			seenRandom = true
			random = tagValue(text, tags, i)
		}
	}
	var scopes []string
	evol := equipment && evolutionSource(text, tags)
	for _, t := range tags {
		n := strings.ToLower(t.name)
		if strings.HasPrefix(n, "/") {
			if len(scopes) > 0 && scopes[len(scopes)-1] == n {
				scopes = scopes[:len(scopes)-1]
			} else {
				valid = false
			}
			continue
		}
		if equipment && n == "disable random drop" && len(scopes) == 0 {
			return true, false, false, false
		}
		if unsupportedDropTag.MatchString(n) {
			effect := equipment && n == "equipment duration" && (slices.Equal(scopes, []string{"/then"}) || slices.Equal(scopes, []string{"/multiple then", "/then"}))
			condition := equipment && n == "character" && slices.Equal(scopes, []string{"/if"})
			qualification := equipment && len(scopes) == 0 && (t.name == "required job skill" || t.name == "required skill") && (t.start == 0 || text[t.start-1] == '\n') && text[t.start:t.end] == "["+t.name+"]"
			recipe := evol && (t.name == "emancipate" && len(scopes) == 0 || t.name == "emancipate type" && slices.Equal(scopes, []string{"/emancipate"}))
			if !(effect || condition || qualification || recipe) {
				valid = false
			}
		}
		if closes["/"+n] {
			scopes = append(scopes, "/"+n)
		}
	}
	m := attachLiteral.FindStringSubmatch(attach)
	if m == nil {
		return false, false, false, false
	}
	a := m[1]
	sealed = a == "sealing" || a == "sealing trade"
	valid = valid && len(scopes) == 0 && (a == "free" || equipment && (a == "trade" || sealed)) && (random == "" || random == "0" || equipment && random == "1")
	return false, valid, sealed, valid && random == "1"
}

func prepareDropCandidate(src gm.ItemSource) (NativeDropCandidate, bool) {
	result := NativeDropCandidate{Id: src.Id, DefinitionHash: src.Hash, Equipment: src.Equipment}
	// Most PVF items do not declare a creation rate. This cheap rejection avoids
	// performing several full-text section scans on the unused entries.
	if !strings.Contains(strings.ToLower(src.Text), "[creation rate]") {
		return result, false
	}
	field := func(n string) string {
		s, err := pvftext.Section(src.Text, n)
		if err != nil {
			return "\x00"
		}
		return s
	}
	num := func(n string) int {
		v, err := strconv.ParseInt(field(n), 10, 32)
		if err != nil {
			return -1
		}
		return int(v)
	}
	result.Grade, result.Rarity = num("grade"), num("rarity")
	weight := num("creation rate")
	name := "stackable type"
	if src.Equipment {
		name = "equipment type"
	}
	kind := field(name)
	if kind == "\x00" {
		return result, false
	}
	if src.Equipment {
		if start := strings.Index(kind, "["); start >= 0 {
			if end := strings.Index(kind[start:], "]"); end >= 0 {
				result.EquipmentType = kind[start+1 : start+end]
			}
		}
	}
	result.Creature = src.Equipment && creatureType.MatchString(kind)
	result.Recipe = !src.Equipment && recipeType.MatchString(kind)
	result.MinimumLevel = num("minimum level")
	if result.Creature && field("minimum level") == "" {
		result.MinimumLevel = 1
	}
	if result.Grade < 1 || result.Grade > 255 || result.Rarity < 0 || result.Rarity > 7 || weight < 0 || result.MinimumLevel < 1 || result.MinimumLevel > 255 {
		return result, false
	}
	result.CreationWeight = uint32(weight)
	tags, ok := dropTags(src.Text)
	if !ok {
		return result, !src.Equipment
	}
	disabled, valid, sealed, magic := dropAcquisition(src.Text, tags, src.Equipment)
	if disabled {
		return result, false
	}
	if !valid {
		return result, true
	}
	var t *gm.ItemTemplate
	var err error
	if src.Equipment {
		result.RequiresMagicSeal = magic
		if result.Creature {
			sp, err := src.SpecialGift(nil, 0)
			if err == nil && sp.Template.Kind == 2 && !magic && sp.ResourcePeriod == 0 && sp.Template.ResourceExpiration == nil && sp.Template.RelativeLifetimeSeconds == 0 && !sp.Template.DailyDeletion && sp.Template.WeeklyDeletionDay == nil {
				sp.Template.InitialSealed = sealed
				result.Artifact = &sp.Template
			}
			return result, true
		}
		if magic && result.Rarity != 2 && result.Rarity != 3 {
			return result, true
		}
		t, err = src.Ordinary()
		if err == nil && t != nil {
			if sealed {
				t.InitialSealed = true
				t.PvfGiftItem = true
			}
			if canUpgradeType.MatchString(kind) && !noUpgrade.MatchString(src.Text) {
				contents, err := pvftext.Section(src.Text, "impossible contents")
				if err != nil {
					return result, false
				}
				result.CanRandomUpgrade = !upgradeWord.MatchString(contents)
			}
		}
	} else {
		if !nativeStackable.MatchString(kind) {
			return result, true
		}
		if kind == "\x60[recipe]\x60 1" || throwDrop.MatchString(kind) {
			t, err = src.Gift()
			if t != nil && (hasDropLifetime(t) || t.InitialSealed || t.AccountAfterFirstTrade || t.AccountCommonInventory) {
				t = nil
			}
		} else {
			t, err = src.Ordinary()
		}
	}
	if err == nil {
		result.Template = t
	}
	return result, true
}

func BuildDropCandidates(ctx context.Context, a Archive, items *commerce.Items) ([]NativeDropCandidate, error) {
	type entry struct {
		id            uint32
		path          string
		equip         bool
		chunk, offset int
	}
	rows := map[uint32]entry{}
	physical, _ := a.(interface{ PhysicalIndex(string) (int, int, bool) })
	for _, category := range []string{"equipment", "stackable"} {
		values, err := a.ReadIndex(category)
		if err != nil {
			return nil, err
		}
		for id, path := range values {
			if id <= 2 || id > math.MaxUint32 {
				continue
			}
			key := uint32(id)
			if _, dup := rows[key]; dup {
				continue
			}
			r := entry{id: key, path: path, equip: category == "equipment"}
			if physical != nil {
				r.chunk, r.offset, _ = physical.PhysicalIndex(path)
			}
			rows[key] = r
		}
	}
	order := slices.Collect(maps.Values(rows))
	slices.SortFunc(order, func(a, b entry) int {
		return cmp.Or(cmp.Compare(a.chunk, b.chunk), cmp.Compare(a.offset, b.offset), cmp.Compare(a.id, b.id))
	})
	out := []NativeDropCandidate{}
	var sealIDs []uint32
	for i, r := range order {
		if i%512 == 0 && ctx.Err() != nil {
			return nil, ctx.Err()
		}
		// Avoid fetching raw bytes and hashing the large class of ineligible
		// scripts. Parse and hash every candidate actually present in the pool.
		t, err := a.ReadText(r.path, budget)
		if err != nil {
			continue
		}
		if !strings.Contains(strings.ToLower(t), "[creation rate]") {
			continue
		}
		s, err := gm.ReadItemSource(a, r.id, r.path, r.equip)
		if err != nil {
			continue
		}
		c, ok := prepareDropCandidate(s)
		if !ok {
			continue
		}
		if c.RequiresMagicSeal && c.Template != nil {
			sealIDs = append(sealIDs, c.Id)
		}
		out = append(out, c)
		if len(out) > 65536 {
			return nil, fmt.Errorf("too many ordinary drop candidates")
		}
	}
	seals, err := commerce.BuildMagicSeal(items, sealIDs)
	if err != nil {
		return nil, err
	}
	for i := range out {
		c := &out[i]
		if c.RequiresMagicSeal {
			if _, ok := seals[c.Id]; !ok {
				c.Template = nil
			}
		}
	}
	slices.SortFunc(out, func(a, b NativeDropCandidate) int { return cmp.Compare(a.Id, b.Id) })
	return out, nil
}
