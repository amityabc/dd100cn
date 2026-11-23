package commerce

import (
	"maps"
	"math"
	"regexp"
	"strconv"
	"strings"

	"s9a1/server/internal/catalogprep/gm"
	"s9a1/server/internal/storage"
)

type craftDestruction struct {
	cube            uint32
	base            float64
	rate            int64
	rows            [8]storage.CraftDestructionRow
	exclude, pieces map[uint32]bool
	fixed           map[uint32]map[uint32]uint32
}

func loadCraftDestruction(items *Items) (craftDestruction, error) {
	var out craftDestruction
	p := craftParser{name: "destruction"}
	read := func(path string) []gm.Field {
		text, err := items.source.ReadText(path, scriptLimit)
		p.check(err == nil, "failed reading "+path)
		return p.script(text)
	}
	fields := read("etc/disjoint.etc")
	price := read("equipment/pricetable.tbl")
	pieces := read("etc/epicpieceinfo.etc")
	for _, f := range fields {
		if f.Name != "cube index" {
			continue
		}
		tokens := p.tokens(f.Value)
		if len(tokens) == 0 || tokens[0] != "`[no element]`" {
			continue
		}
		p.check(out.cube == 0 && len(tokens) == 11, "invalid no-element cube index")
		for _, v := range tokens[1:] {
			id := uint32(p.ints(v, 1, math.MaxInt32))
			p.check(out.cube == 0 || out.cube == id, "inconsistent cube index")
			out.cube = id
		}
	}
	p.check(out.cube != 0, "missing cube index")
	cube := p.array(fields, "cube creation const", 9)
	if !p.check(len(cube) == 9 && cube[0] > 0, "invalid cube factor") {
		return out, p.err
	}
	out.base = float64(cube[0])
	itemID := func(s string) uint32 { return uint32(p.ints(s, 1, math.MaxInt32)) }
	itemRows := func(tag string) [8][]uint32 {
		v := p.tokens(p.field(fields, tag, false))
		var rows [8][]uint32
		at := 0
		for i := range rows {
			if !p.check(at < len(v), "missing "+tag+" rarity row") {
				break
			}
			n := p.ints(v[at], 0, 64)
			at++
			if !p.check(n <= len(v)-at, "truncated "+tag+" rarity row") {
				break
			}
			rows[i] = []uint32{}
			for _, token := range v[at : at+n] {
				rows[i] = append(rows[i], itemID(token))
			}
			at += n
		}
		p.check(at == len(v), "trailing "+tag+" item operands")
		return rows
	}
	extra, souls := itemRows("additional result"), itemRows("additional result expand")
	values := func(tag string) (func() float64, func() bool) {
		v := p.tokens(p.field(fields, tag, false))
		at := 0
		return func() float64 {
			if !p.check(at < len(v), "truncated "+tag) {
				return 0
			}
			value := p.float(v[at])
			at++
			return float64(value)
		}, func() bool { return at == len(v) }
	}
	next, noExtraConstants := values("additional result const")
	nextSoul, noSoulConstants := values("additional result expand const")
	for i := range out.rows {
		row := storage.CraftDestructionRow{CubeMultiplier: float64(cube[i+1]), Additional: extra[i], GreatDivisor: next(), SoulDivisor: nextSoul()}
		if row.GreatDivisor != 0 {
			row.NormalDivisor, row.AdditionalGreatChance, row.AdditionalNormalChance = next(), next(), next()
		}
		p.check(row.AdditionalGreatChance <= 100 && row.AdditionalNormalChance <= 100 &&
			(len(row.Additional) == 0 || row.GreatDivisor > 0 && row.NormalDivisor > 0), "invalid additional material constants")
		if row.SoulDivisor != 0 {
			row.SoulGreatChance, row.SoulNormalChance = nextSoul(), nextSoul()
			if p.check(len(souls[i]) == 1, "missing soul item") {
				row.SoulID = souls[i][0]
			}
		} else {
			p.check(len(souls[i]) == 0, "unexpected soul item")
		}
		p.check(row.SoulGreatChance <= 100 && row.SoulNormalChance <= 100, "invalid soul material constants")
		out.rows[i] = row
	}
	p.check(noExtraConstants() && noSoulConstants(), "trailing destruction constants")
	rate := p.tokens(p.field(price, "rate", false))
	if p.check(len(rate) == 3, "sale rate must comprise 3 values") {
		out.rate = int64(p.ints(rate[0], 1, math.MaxInt32))
		p.ints(rate[1], 1, math.MaxInt32)
		p.ints(rate[2], 1, math.MaxInt32)
	}
	out.exclude, out.pieces = map[uint32]bool{}, map[uint32]bool{}
	for _, token := range p.tokens(p.field(fields, "exclude additional result expand", false)) {
		out.exclude[itemID(token)] = true
	}
	out.fixed = map[uint32]map[uint32]uint32{}
	setFixed := func(id uint32, m map[uint32]uint32) {
		if existing, found := out.fixed[id]; found {
			p.check((m == nil) == (existing == nil) && maps.Equal(m, existing), "conflicting fixed destruction output")
		}
		out.fixed[id] = m
	}
	for i := 0; i < len(fields); i++ {
		if fields[i].Name != "particular item" {
			continue
		}
		start := i + 1
		for i = start; i < len(fields) && fields[i].Name != "/particular item"; i++ {
			p.check(fields[i].Name != "particular item", "nested destruction recipe")
		}
		if !p.check(i < len(fields), "unclosed destruction recipe") {
			break
		}
		block := fields[start:i]
		ids := p.tokens(p.field(block, "input index", false))
		output := p.tokens(p.field(block, "output index", false))
		var result map[uint32]uint32
		if len(output) == 3 {
			a, aerr := strconv.ParseInt(output[0], 10, 32)
			b, berr := strconv.ParseInt(output[1], 10, 32)
			c, cerr := strconv.ParseInt(output[2], 10, 32)
			if aerr == nil && berr == nil && cerr == nil && a > 0 && b == 1000 && c > 0 {
				result = map[uint32]uint32{uint32(a): uint32(c)}
			}
		}
		for _, v := range ids {
			setFixed(itemID(v), result)
		}
	}
	events := p.tokens(p.field(fields, "event result", false))
	p.check(len(events)%3 == 0, "invalid event result row")
	for i := 0; i+2 < len(events); i += 3 {
		setFixed(itemID(events[i]), map[uint32]uint32{itemID(events[i+1]): itemID(events[i+2])})
	}
	sections := map[string]int{}
	for i, f := range pieces {
		switch f.Name {
		case "equipment piece drop info", "/equipment piece drop info", "piece list":
			_, exists := sections[f.Name]
			p.check(!exists, "ambiguous epic piece declaration")
			sections[f.Name] = i + 1
		}
	}
	start, list, finish := sections["equipment piece drop info"], sections["piece list"], sections["/equipment piece drop info"]
	if p.check(start > 0 && list > start && list+1 < finish && pieces[list].Name == "/piece list", "incomplete epic piece catalog") {
		tokens := p.tokens(pieces[list-1].Value)
		p.check(len(tokens) > 0 && len(tokens)%2 == 0, "invalid epic piece catalog")
		allPieces := map[uint32]bool{}
		for i := 0; i+1 < len(tokens); i += 2 {
			item, piece := itemID(tokens[i]), itemID(tokens[i+1])
			p.check(!out.pieces[item] && !allPieces[piece], "duplicate epic piece identity")
			out.pieces[item], allPieces[piece] = true, true
		}
	}
	return out, p.err
}

var craftPriceNumber = regexp.MustCompile(`-?[0-9]+(?:\.[0-9]+)?`)

func craftItemValue(p *craftParser, fields []gm.Field) float64 {
	values := map[string]string{}
	skills := false
	for _, f := range fields {
		switch f.Name {
		case "all skill item container":
			p.check(!skills, "nested equipment skill container")
			skills = true
		case "/all skill item container":
			p.check(skills, "unmatched equipment skill container")
			skills = false
		case "value", "price":
			if skills {
				continue
			}
			_, dup := values[f.Name]
			p.check(!dup, "duplicate item "+f.Name)
			values[f.Name] = f.Value
		}
	}
	p.check(!skills, "unterminated equipment skill container")
	for _, field := range []string{"value", "price"} {
		raw := craftPriceNumber.FindString(values[field])
		if raw == "" {
			continue
		}
		n, err := strconv.ParseFloat(raw, 64)
		p.check(err == nil, "invalid item value")
		return n
	}
	return -1
}

func (r craftDestruction) prepare(e storage.CraftEquipment, text string) (storage.CraftDestruction, bool) {
	out := storage.CraftDestruction{CubeID: r.cube, Row: r.rows[e.Rarity]}
	if !craftValidEquipment(e) {
		return out, false
	}
	p := craftParser{name: "equipment destruction"}
	fields := p.script(text)
	impossible := p.tokens(p.field(fields, "impossible contents", true))
	prohibited := p.field(fields, "attach type", true) == "`[trade delete]`"
	for _, v := range impossible {
		if !p.check(len(v) >= 3 && v[0] == '`' && v[len(v)-1] == '`', "invalid destruction exclusion list") {
			return out, false
		}
		prohibited = prohibited || strings.EqualFold(strings.Trim(v, "`[] "), "disjoint")
	}
	if p.err != nil {
		return out, false
	}
	if prohibited {
		out.SuppressSoul, out.Fixed = true, map[uint32]uint32{}
		return out, true
	}
	if fixed, exists := r.fixed[e.TemplateID]; exists {
		out.SuppressSoul, out.Fixed = true, fixed
		return out, fixed != nil
	}
	if r.pieces[e.TemplateID] {
		return out, false
	}
	price := craftItemValue(&p, fields)
	if p.err != nil || price < 0 || price > math.MaxInt32 || price != math.Trunc(price) {
		return out, false
	}
	sale := int64(price) * r.rate / 1000
	if sale > math.MaxInt32 {
		return out, false
	}
	cubes := math.Floor(float64(max(1, sale)) * out.Row.CubeMultiplier / r.base)
	if math.IsInf(cubes, 0) || math.IsNaN(cubes) || cubes > math.MaxInt32 {
		return out, false
	}
	out.CubeCount = max(1, uint32(cubes))
	out.SuppressSoul = r.exclude[e.TemplateID]
	return out, true
}

func addCraftDestructionMaterials(ids map[uint32]bool, row storage.CraftDestruction) {
	if row.Fixed != nil {
		for id := range row.Fixed {
			ids[id] = true
		}
		return
	}
	ids[row.CubeID] = true
	for _, id := range row.Row.Additional {
		ids[id] = true
	}
	if !row.SuppressSoul && row.Row.SoulID != 0 && row.Row.SoulNormalChance > 0 {
		ids[row.Row.SoulID] = true
	}
}
