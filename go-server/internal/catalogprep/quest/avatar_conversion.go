package quest

import (
	"encoding/json"
	"math"
	"slices"
	"strings"

	"s9a1/server/internal/storage"
)

var conversionFields = strings.Split("name|explain|explain detail|flavor text|grade|rarity|usable job|/usable job|attach type|minimum level|stack limit|usable period|expiration date|daily delete item|weekly delete item|icon|icon mark|field image|move wav|stackable type|action type|/action type|use action packet|action usable place|/action usable place|need material|/need material|daily purchase limit|purchase limit|/purchase limit|string data|/string data|int data|/int data|stackable action limit indexes|/stackable action limit indexes|cool time|cooltime group|item category|/item category|price|value", "|")

func loadAvatarConversionSources(items ItemLookup, ids []uint32) map[uint32]storage.AvatarConversionSource {
	out := make(map[uint32]storage.AvatarConversionSource)
	for _, id := range ids {
		src, err := items.Source(id)
		if err != nil || src.Equipment {
			continue
		}
		s := readItemScript(src.Text)
		tally := make(map[string]int)
		for _, f := range s.fields {
			tally[f.name]++
			if !slices.Contains(conversionFields, f.name) || f.name != "int data" && tally[f.name] > 1 {
				s.fail("unknown or duplicate avatar action source field")
			}
		}
		if s.get("stackable type") != "\x60[etc]\x60 0" || s.get("item category") != "" {
			continue
		}
		s.number("price", 0, math.MaxUint32, false)
		s.number("value", 0, math.MaxUint32, false)
		s.number("use action packet", 0, 1, false)
		s.tags("action usable place", true, "village", "seria room", "dont use assult")
		s.purchaseMaterial(math.MaxInt32, true)
		prefix := "\x60[change avatar]\x60 "
		action := s.get("action type")
		if !strings.HasPrefix(action, prefix) {
			continue
		}
		raw := s.numbers(strings.TrimPrefix(action, prefix))
		if len(raw) == 0 || len(raw)%4 != 0 || len(raw) > 65536 {
			continue
		}
		limits := s.numbers(s.get("stackable action limit indexes"))
		if len(distinct(limits)) != len(limits) {
			continue
		}
		valid := true
		for _, name := range []string{"action type", "item category", "stackable action limit indexes", "string data", "action usable place", "need material"} {
			i := slices.IndexFunc(s.fields, func(f giftField) bool { return f.name == name })
			j := slices.IndexFunc(s.fields, func(f giftField) bool { return f.name == "/"+name })
			if i < 0 && j < 0 && name != "action type" {
				continue
			}
			if i < 0 || j != i+1 || s.fields[i].value == "" || s.fields[j].value != "" {
				valid = false
				break
			}
		}
		var ints []int
		for i, f := range s.fields {
			if f.name == "int data" || f.name == "/int data" {
				ints = append(ints, i)
			}
		}
		if len(ints) > 0 && (len(ints) != 2 || ints[1] != ints[0]+1 || s.fields[ints[0]].name != "int data" || s.fields[ints[1]].value != "") {
			valid = false
		}
		if !valid || s.err != nil {
			continue
		}
		replacements := make(map[uint32]uint32)
		seen := make(map[uint32]bool)
		for row := range slices.Chunk(raw, 4) {
			if row[0] == 0 || row[0] > math.MaxInt32 || row[1] == 0 || row[1] > math.MaxInt32 || row[2] != 0 || row[3] != 5 || seen[row[0]] {
				valid = false
				break
			}
			seen[row[0]] = true
			if len(limits) == 0 || slices.Contains(limits, row[0]) {
				replacements[row[0]] = row[1]
			}
		}
		if !valid || len(limits) > 0 && len(limits) != len(replacements) {
			continue
		}
		template, err := src.Gift()
		if err != nil || template.Kind != 2 || template.FirstBagSlot != 65 {
			continue
		}
		b, err := json.Marshal(template)
		if err == nil {
			out[id] = storage.AvatarConversionSource{Source: b, Replacements: replacements}
		}
	}
	return out
}

func completeAvatarConversions(items ItemLookup, cases map[uint32]map[byte]uint16, sources map[uint32]storage.AvatarConversionSource) storage.AvatarConversionCatalog {
	out := storage.AvatarConversionCatalog{Sources: sources, Targets: make(map[uint32]storage.AvatarConversionTarget)}
	checked := make(map[uint32]*storage.AvatarConversionTarget)
	read := func(id uint32) *storage.AvatarConversionTarget {
		if prior, have := checked[id]; have {
			return prior
		}
		checked[id] = nil
		src, err := items.Source(id)
		if err != nil || !src.Equipment {
			return nil
		}
		p, err := src.SpecialGift(cases, 4)
		if err != nil || p.Template.Kind != 0 || p.Template.WornSlot > 7 {
			return nil
		}
		s := readItemScript(src.Text)
		grade := s.number("grade", 255, 255, true)
		clone := avatarCloneRE.MatchString(s.get("item category"))
		if s.err != nil {
			return nil
		}
		raw, err := json.Marshal(p.Template)
		if err != nil {
			return nil
		}
		result := &storage.AvatarConversionTarget{Template: raw, Grade: byte(grade), IsClone: clone}
		checked[id] = result
		return result
	}
	for _, src := range sources {
		for id, end := range src.Replacements {
			input, output := read(id), read(end)
			if input != nil && output != nil {
				out.Targets[id], out.Targets[end] = *input, *output
			}
		}
	}
	return out
}
