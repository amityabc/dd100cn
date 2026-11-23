package commerce

import (
	"fmt"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"s9a1/server/internal/catalogprep/gm"
)

var (
	shopTags          = regexp.MustCompile("(?im)^[ \t]*\\[([^/\\]\\r\\n]+)\\][ \t\\r]*$")
	sellBlock         = regexp.MustCompile("(?ims)^\\[sell info\\]\\s*(.*?)^\\[/sell info\\]")
	tabBlocks         = regexp.MustCompile("(?ims)^\\[tab\\]\\s*(.*?)^\\[/tab\\]")
	categoryBlocks    = regexp.MustCompile("(?ims)^\\[category entry\\]\\s*(.*?)^\\[/category entry\\]")
	itemLists         = regexp.MustCompile("(?ims)^\\[item list\\]\\s*(.*?)^\\[/item list\\]")
	categorySelectors = regexp.MustCompile("(?im)^\\[use category\\][ \t]*\\r?\\n([^\\r\\n]*)")
	shopFields        = []string{"npc", "type", "sell info", "use category", "tab", "category entry", "id", "item list", "message",
		"show inventory item on itemshop", "show account common inventory item on itemshop", "hide tab list"}
)

type shopPage struct {
	job   int
	items []uint32
}

func shopProducts(text string) ([16][]uint32, error) {
	var out [16][]uint32
	for _, match := range shopTags.FindAllStringSubmatch(text, -1) {
		if !slices.Contains(shopFields, strings.ToLower(match[1])) {
			return out, fmt.Errorf("shop contains unsupported pricing, currency, limit or activation rules")
		}
	}
	typ, err := commerceSection(text, "type")
	if err != nil {
		return out, err
	}
	switch typ {
	case "`[weapon shop]`", "`[equipment shop]`", "`[etc shop]`":
	default:
		return out, fmt.Errorf("shop is not an ordinary gold shop")
	}
	sell := sellBlock.FindAllStringSubmatch(text, 2)
	if len(sell) != 1 {
		return out, fmt.Errorf("shop requires one complete sell-info block")
	}
	fields, err := gm.ItemFields(text)
	if err != nil {
		return out, err
	}
	var stack []string
	var start, end int
	for _, f := range fields {
		name := strings.ToLower(f.Name)
		switch name {
		case "sell info":
			start++
			stack = append(stack, name)
		case "tab":
			stack = append(stack, name)
		case "/sell info", "/tab":
			if len(stack) == 0 || stack[len(stack)-1] != name[1:] {
				return out, fmt.Errorf("shop list scopes are unmatched or crossed")
			}
			if name == "/sell info" {
				end++
			}
			stack = stack[:len(stack)-1]
		}
	}
	if len(stack) != 0 || start != 1 || end != 1 {
		return out, fmt.Errorf("shop list scope is incomplete")
	}
	for _, tag := range []string{"show inventory item on itemshop", "show account common inventory item on itemshop"} {
		s, err := commerceSection(text, tag)
		if err != nil {
			return out, err
		}
		for _, v := range strings.Fields(s) {
			n, e := unsigned(v, 32)
			if e != nil || n == 0 {
				return out, fmt.Errorf("shop displayed currency list contains an invalid identity")
			}
		}
	}
	hiddenText, err := commerceSection(text, "hide tab list")
	if err != nil {
		return out, err
	}
	hides := map[uint64]bool{}
	for _, v := range strings.Fields(hiddenText) {
		n, e := unsigned(v, 32)
		if e != nil {
			return out, e
		}
		hides[n] = true
	}
	block := sell[0][1]
	tabs := tabBlocks.FindAllStringSubmatch(block, -1)
	for index := range hides {
		if index >= uint64(len(tabs)) {
			return out, fmt.Errorf("hidden tab index has no source tab")
		}
	}
	outside := tabBlocks.ReplaceAllString(block, "")
	inherited, err := shopSelector(outside, "")
	if err != nil {
		return out, err
	}
	pages, err := shopPages(outside, inherited)
	if err != nil {
		return out, err
	}
	for n, tab := range tabs {
		if hides[uint64(n)] {
			continue
		}
		p, err := shopPages(tab[1], inherited)
		if err != nil {
			return out, err
		}
		pages = append(pages, p...)
	}
	for job := range out {
		set := map[uint32]bool{}
		for _, p := range pages {
			if p.job != job && p.job != -1 {
				continue
			}
			for _, id := range p.items {
				if !set[id] {
					set[id] = true
					out[job] = append(out[job], id)
				}
			}
		}
	}
	return out, nil
}

func shopSelector(text, inherited string) (string, error) {
	selectors := map[string]bool{}
	for _, match := range categorySelectors.FindAllStringSubmatch(text, -1) {
		selectors[strings.TrimSpace(match[1])] = true
	}
	if len(selectors) > 1 {
		return "", fmt.Errorf("shop contains conflicting category selectors")
	}
	for s := range selectors {
		inherited = s
	}
	if inherited != "" && inherited != "`basic job`" {
		return "", fmt.Errorf("unsupported NPC shop category selector")
	}
	return inherited, nil
}

func shopPages(text, inherited string) ([]shopPage, error) {
	var out []shopPage
	flat := categoryBlocks.ReplaceAllString(text, "")
	selector, err := shopSelector(flat, inherited)
	if err != nil {
		return nil, err
	}
	entries := categoryBlocks.FindAllStringSubmatch(text, -1)
	if len(entries) != 0 && selector != "`basic job`" {
		return nil, fmt.Errorf("NPC shop categories have no supported selector")
	}
	lists := func(body string, job int) error {
		var p shopPage
		p.job = job
		for _, list := range itemLists.FindAllStringSubmatch(body, -1) {
			for _, token := range strings.Fields(list[1]) {
				if token == "-1" || token == "-2" {
					continue
				}
				n, err := unsigned(token, 32)
				if err != nil || n == 0 {
					return fmt.Errorf("shop item list is not a plain template-ID whitelist")
				}
				p.items = append(p.items, uint32(n))
			}
		}
		if len(p.items) != 0 {
			out = append(out, p)
		}
		return nil
	}
	for _, e := range entries {
		field, err := commerceSection(e[1], "id")
		if err != nil {
			return nil, err
		}
		id, err := strconv.ParseUint(field, 10, 32)
		if err != nil {
			return nil, err
		}
		if id >= 16 {
			continue
		}
		if err := lists(e[1], int(id)); err != nil {
			return nil, err
		}
	}
	if err := lists(flat, -1); err != nil {
		return nil, err
	}
	return out, nil
}
