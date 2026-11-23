package commerce

import (
	"errors"
	"io/fs"
	"math"
	"strings"

	"s9a1/server/internal/storage"
)

type craftBook struct{ action, option, count, max int }
type craftBooks struct {
	items   map[uint32]craftBook
	chance  []struct{ Value, Weight int }
	minimum int
}

func loadCraftBooks(items *Items) (craftBooks, error) {
	const path = "etc/amplifyitem.etc"
	out := craftBooks{items: map[uint32]craftBook{}, minimum: 55}
	text, err := items.source.ReadText(path, scriptLimit)
	if errors.Is(err, fs.ErrNotExist) {
		return out, nil
	}
	if err != nil {
		return out, err
	}
	p := craftParser{name: path}
	tokens := func(tag string) []string {
		v, e := commerceSection(text, tag)
		p.check(e == nil, "duplicate "+tag)
		return p.tokens(v)
	}
	options := map[string]int{}
	name := func(s string) string {
		if !p.check(len(s) >= 2 && s[0] == '`' && s[len(s)-1] == '`', "option must be quoted") {
			return s
		}
		return s[1 : len(s)-1]
	}
	option := func(s string, max int) int {
		o, exists := options[name(s)]
		p.check(exists && o >= 1 && o <= max, "unknown option")
		return o
	}
	mapping := tokens("option mapping table")
	p.check(len(mapping) >= 2 && len(mapping)%2 == 0, "invalid amplification option mapping")
	for i := 0; i+1 < len(mapping); i += 2 {
		key := name(mapping[i])
		_, exists := options[key]
		p.check(!exists, "duplicate option")
		options[key] = p.ints(mapping[i+1], 0, 6)
	}
	data := tokens("option data")
	p.check(len(data) > 0 && len(data)%3 == 0, "invalid amplification option weights")
	for i := 0; i+2 < len(data); i += 3 {
		out.chance = append(out.chance, struct{ Value, Weight int }{option(data[i], 4), p.ints(data[i+1], 1, math.MaxInt32)})
	}
	level := tokens("equip level const")
	if p.check(len(level) == 1, "invalid amplification equipment level constant") {
		out.minimum = p.ints(level[0], 1, 100)
	}
	conflicts := map[uint32]bool{}
	for action, tag := range []string{"invest option", "reinvest option", "random invest upgrade option"} {
		stride := 3
		if action == 1 {
			stride = 4
		}
		rows := tokens(tag)
		p.check(len(rows) > 0 && len(rows)%stride == 0, "incomplete amplification registration")
		for i := 0; i+stride <= len(rows); i += stride {
			id := uint32(p.ints(rows[i+1], 1, math.MaxInt32))
			r := craftBook{action: action, option: option(rows[i], 6), count: p.ints(rows[i+2], 1, 1000)}
			if action == 1 {
				r.max = p.ints(rows[i+3], 0, 255)
			}
			if prior, dup := out.items[id]; dup && prior != r {
				conflicts[id] = true
			}
			out.items[id] = r
		}
	}
	for id := range conflicts {
		delete(out.items, id)
	}
	return out, p.err
}

func (b craftBooks) ticket(id uint32, text string) (storage.CraftTicketRule, bool) {
	p := craftParser{name: "ticket"}
	fields := p.script(text)
	vector := []string{"equipment reinforcement ticket", "equipment amplify reinforcement ticket", "amplification random value"}
	out := storage.CraftTicketRule{TemplateID: id, ChancePercent: 100, BookAction: 2, BookOption: 5, BookMaterialCount: 1,
		BookMaximumUpgradeLevel: 99, BookMinimumLevel: 55, RandomValues: []struct{ Value, Weight int }{}, BookTypeWeights: []struct{ Value, Weight int }{}}
	var tokens []string
	for kind, tag := range vector {
		value := strings.TrimSpace(p.field(fields, tag, true))
		if value == "" {
			continue
		}
		if !p.check(tokens == nil, "multiple ticket actions") {
			return out, false
		}
		out.Kind = byte(kind)
		tokens = p.tokens(value)
	}
	registration, registered := b.items[id]
	switch {
	case tokens == nil:
		if !registered || registration.action == 2 ||
			!strings.Contains(strings.ToLower(p.field(fields, "stackable type", true)), "[etc]") {
			return out, false
		}
		out.Kind = 2
	case out.Kind != 2:
		if !p.check(len(tokens) >= 3 && len(tokens) <= 4 && tokens[2] == "`fixed`", "ticket effect is not fixed") {
			return out, false
		}
		out.Level = byte(p.ints(tokens[0], 1, 31))
		out.ChancePercent = p.ints(tokens[1], 1, 100)
	case out.Kind == 2:
		if !p.check(len(tokens) >= 2 && len(tokens) <= 64 && len(tokens)%2 == 0, "invalid amplification random values") {
			return out, false
		}
		for i := 0; i < len(tokens); i += 2 {
			out.RandomValues = append(out.RandomValues, struct{ Value, Weight int }{p.ints(tokens[i], 1, 31), p.ints(tokens[i+1], 1, 100000)})
		}
	}
	if out.Kind == 2 && registered {
		out.BookAction, out.BookOption, out.BookMaterialCount = registration.action, registration.option, registration.count
		out.BookMaximumUpgradeLevel, out.BookMinimumLevel = registration.max, b.minimum
		switch {
		case registration.option == 6:
			out.BookTypeWeights = b.chance
		case registration.option >= 1 && registration.option <= 4:
			out.BookTypeWeights = []struct{ Value, Weight int }{{registration.option, 1}}
		}
	}
	return out, p.err == nil
}
