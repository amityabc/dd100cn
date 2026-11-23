package dungeons

import (
	"regexp"
	"slices"
	"strconv"
	"strings"

	"s9a1/server/internal/catalogprep/pvftext"
)

type clearRewardExtras struct {
	SpecificGoldcardGroups       []clearGoldcardGroup
	DungeonIndexRewardRates      []clearIndexRate
	PrivilegeServiceRewardRates  []clearPrivilegeRate
	SpecialDungeonClearRewards   []clearSpecialDungeon
	AdditionalNativeSections     []clearNativeSection
	ClearRewardGoldRates         []clearGoldRate
	SpecialClearRewardItemGroups []clearSpecialGroup
	CardSlotRewardTables         []clearSlotTable
	GoldCardBlankItems           []clearBlankItem
	PcroomCardBlankItems         []clearBlankItem
}
type clearRewardItem struct{ Item, Weight, Quantity uint32 }
type clearGoldcardGroup struct {
	Group        uint32
	DungeonTypes []uint32
	Items        []clearRewardItem
}
type clearIndexRate struct {
	DungeonType, DungeonIndex, Variant uint32
	Items                              []clearRewardItem
}
type clearPrivilegeRate struct {
	DungeonIndex, Variant uint32
	Item                  clearRewardItem
}
type clearSpecialDungeon struct {
	DungeonType    uint32
	MailMessages   []string
	Entries        []clearSpecialEntry
	DungeonIndexes []uint32
}
type clearSpecialEntry struct {
	DungeonIndex                 uint32
	ClearRewards, SpecialRewards []clearRewardItem
	ItemList                     clearSpecialItemList
}
type clearSpecialItemList struct {
	Maximum, DrawCount uint32
	Items              []weightedDrop
}
type clearNativeSection struct{ Name, Body string }
type clearGoldRate struct{ DungeonIndex, Rate uint32 }
type clearSpecialGroup struct {
	DungeonIndexes []uint32
	Items          []clearSpecialItem
}
type clearSpecialItem struct{ Item, Variant, Weight, Quantity uint32 }
type clearSlotTable struct {
	Name    string
	Rewards []clearSlotReward
}
type clearSlotReward struct {
	Slot                   string
	Item, Weight, Quantity uint32
}
type clearBlankItem struct{ Weight, Item, Quantity uint32 }

var clearSupplementTags = []string{
	"gold card blank item", "pcroom card blank item", "chronicle set reward rate", "static table", "clear reward gold", "special clear reward item",
	"cardslot specific reward table",
}

func (p *input) clearRewardExtras(text string) clearRewardExtras {
	var r clearRewardExtras
	r.SpecificGoldcardGroups = p.clearGoldcard(p.field(text, "specific goldcard group", false))
	r.DungeonIndexRewardRates = p.clearIndexed(p.field(text, "dungeon_index specific reward rate", false))
	r.PrivilegeServiceRewardRates = p.clearPrivilege(p.field(text, "dungeon index privilege service reward rate", false))
	r.SpecialDungeonClearRewards = p.specialDungeons(p.field(text, "special dungeon clear reward", false))
	r.AdditionalNativeSections = []clearNativeSection{}
	for _, tag := range clearSupplementTags {
		if tagPresent(text, tag) {
			r.AdditionalNativeSections = append(r.AdditionalNativeSections, clearNativeSection{tag, p.field(text, tag, false)})
		}
	}
	r.ClearRewardGoldRates = p.clearGoldRates(p.field(text, "clear reward gold", false))
	r.SpecialClearRewardItemGroups = p.clearSpecialGroups(p.field(text, "special clear reward item", false))
	r.CardSlotRewardTables = p.clearSlotTables(p.field(text, "cardslot specific reward table", false))
	r.GoldCardBlankItems = p.clearBlank(p.field(text, "gold card blank item", false), "gold card blank item")
	r.PcroomCardBlankItems = p.clearBlank(p.field(text, "pcroom card blank item", false), "pcroom card blank item")
	return r
}

func (p *input) clearGoldcard(body string) []clearGoldcardGroup {
	out := []clearGoldcardGroup{}
	seen := map[uint32]bool{}
	for _, group := range pvftext.Blocks(body, "reward group") {
		header, _, _ := strings.Cut(group, "[")
		g := p.towerUnsigned(header, 1, "goldcard group")
		if !p.require(len(g) == 1 && g[0] > 0 && !seen[g[0]], "clear reward goldcard group identity invalid") {
			return out
		}
		seen[g[0]] = true
		types := p.towerUnsigned(p.field(group, "reward dungeon type list", true), 256, "reward dungeon type list")
		values := p.towerUnsigned(p.field(group, "reward item list", true), 768, "reward item list")
		out = append(out, clearGoldcardGroup{g[0], types, p.clearRewardItems(values)})
	}
	return out
}

func (p *input) clearRewardItems(values []uint32) []clearRewardItem {
	out := []clearRewardItem{}
	if !p.require(len(values) > 0 && len(values)%3 == 0, "clear reward item rows incomplete") {
		return out
	}
	for i := 0; i < len(values); i += 3 {
		p.require(values[i] != 0 && values[i+1] != 0 && values[i+2] != 0, "clear reward item row invalid")
		out = append(out, clearRewardItem{values[i], values[i+1], values[i+2]})
	}
	return out
}

func (p *input) clearIndexed(body string) []clearIndexRate {
	out := []clearIndexRate{}
	for _, block := range pvftext.Blocks(body, "dungeon type") {
		v, err := pvftext.Integers(block)
		if !p.require(err == nil && len(v) >= 6 && (len(v)-1)%5 == 0 && v[0] > 0, "clear-reward dungeon index row malformed") {
			return out
		}
		for i := 1; i < len(v); i += 5 {
			id, variant, item, weight, qty := v[i], v[i+1], v[i+2], v[i+3], v[i+4]
			p.require(id > 0 && variant >= 0 && item > 0 && weight > 0 && qty > 0, "clear-reward dungeon index item invalid")
			out = append(out, clearIndexRate{uint32(v[0]), uint32(id), uint32(variant), []clearRewardItem{{uint32(item), uint32(weight), uint32(qty)}}})
		}
	}
	return out
}

func (p *input) clearPrivilege(body string) []clearPrivilegeRate {
	out := []clearPrivilegeRate{}
	seen := map[[2]int]bool{}
	for _, block := range pvftext.Blocks(body, "dungeon type") {
		v, err := pvftext.Integers(block)
		if !p.require(err == nil && len(v) == 5 && v[0] > 0 && v[1] >= 0 && v[2] > 0 && v[3] > 0 && v[4] > 0,
			"clear reward privilege-service row malformed") {
			return out
		}
		key := [2]int{v[0], v[1]}
		p.require(!seen[key], "clear reward privilege-service row repeated")
		seen[key] = true
		out = append(out, clearPrivilegeRate{uint32(v[0]), uint32(v[1]), clearRewardItem{uint32(v[2]), uint32(v[3]), uint32(v[4])}})
	}
	return out
}

func (p *input) specialDungeons(body string) []clearSpecialDungeon {
	out := []clearSpecialDungeon{}
	seen := map[uint32]bool{}
	for _, block := range pvftext.Blocks(body, "dungeon type") {
		head, _, _ := strings.Cut(block, "[")
		id := p.towerUnsigned(head, 1, "special dungeon type")
		if !p.require(len(id) == 1 && id[0] > 0 && !seen[id[0]], "clear reward special dungeon type malformed") {
			return out
		}
		seen[id[0]] = true
		d := clearSpecialDungeon{DungeonType: id[0], MailMessages: []string{}, Entries: []clearSpecialEntry{}, DungeonIndexes: []uint32{}}
		mail := quoted.FindAllStringSubmatch(p.field(block, "mail message", true), -1)
		p.require(len(mail) > 0, "clear reward special mail message absent")
		for _, row := range mail {
			p.require(row[1] != "", "clear reward special mail message empty")
			d.MailMessages = append(d.MailMessages, row[1])
		}
		for _, line := range pvftext.Blocks(block, "dungeon index") {
			head, _, _ := strings.Cut(line, "[")
			index := p.towerUnsigned(head, 1, "special dungeon index")
			if !p.require(len(index) == 1 && !slices.Contains(d.DungeonIndexes, index[0]), "clear reward special dungeon index repeated or invalid") {
				return out
			}
			entry := clearSpecialEntry{DungeonIndex: index[0]}
			entry.ClearRewards = p.clearRewardItems(p.towerUnsigned(p.field(line, "clear reward", true), budget, "clear reward"))
			entry.SpecialRewards = p.clearRewardItems(p.towerUnsigned(p.field(line, "special clear reward", true), budget, "special clear reward"))
			pool := p.towerUnsigned(p.field(line, "special clear reward item list", true), budget, "special clear reward item list")
			if !p.require(len(pool) >= 4 && len(pool)%2 == 0 && pool[0] > 0 && pool[1] > 0, "clear reward special item list incomplete") {
				return out
			}
			entry.ItemList = clearSpecialItemList{Maximum: pool[0], DrawCount: pool[1], Items: []weightedDrop{}}
			for i := 2; i < len(pool); i += 2 {
				p.require(pool[i] > 0 && pool[i+1] > 0, "clear reward special item list entry invalid")
				entry.ItemList.Items = append(entry.ItemList.Items, weightedDrop{Item: pool[i], Weight: pool[i+1]})
			}
			d.DungeonIndexes = append(d.DungeonIndexes, index[0])
			d.Entries = append(d.Entries, entry)
		}
		p.require(len(d.Entries) > 0, "clear reward special dungeon has no index")
		out = append(out, d)
	}
	return out
}

func (p *input) clearGoldRates(body string) []clearGoldRate {
	out := []clearGoldRate{}
	if strings.TrimSpace(body) == "" {
		return out
	}
	rows := p.towerUnsigned(body, 512, "clear reward gold")
	if !p.require(len(rows)%2 == 0, "clear reward gold rows incomplete") {
		return out
	}
	seen := map[uint32]bool{}
	for i := 0; i < len(rows); i += 2 {
		id, rate := rows[i], rows[i+1]
		p.require(id > 0 && rate <= 1000000 && !seen[id], "clear reward gold row invalid or repeated")
		seen[id] = true
		out = append(out, clearGoldRate{id, rate})
	}
	return out
}

func (p *input) clearSpecialGroups(body string) []clearSpecialGroup {
	out := []clearSpecialGroup{}
	for _, group := range pvftext.Blocks(body, "group") {
		index := p.towerUnsigned(p.field(group, "dungeon", true), 256, "special clear reward dungeons")
		seen := map[uint32]bool{}
		for _, id := range index {
			p.require(id != 0 && !seen[id], "special clear reward dungeon list invalid")
			seen[id] = true
		}
		values := p.towerUnsigned(p.field(group, "reward item", true), 1024, "special clear reward item")
		if !p.require(len(values) > 0 && len(values)%4 == 0, "special clear reward item rows incomplete") {
			return out
		}
		g := clearSpecialGroup{DungeonIndexes: index, Items: []clearSpecialItem{}}
		for i := 0; i < len(values); i += 4 {
			v := values[i : i+4]
			p.require(v[0] > 0 && v[2] > 0 && v[3] > 0, "special clear reward item row invalid")
			g.Items = append(g.Items, clearSpecialItem{v[0], v[1], v[2], v[3]})
		}
		out = append(out, g)
	}
	return out
}

var (
	clearTableHeader = regexp.MustCompile("(?s)^\\s*`([^`]+)`(.*)$")
	clearSlotRow     = regexp.MustCompile("\\s*`([^`]+)`\\s+(\\d+)\\s+(\\d+)\\s+(\\d+)")
)

func (p *input) clearSlotTables(body string) []clearSlotTable {
	out := []clearSlotTable{}
	seen := map[string]bool{}
	for _, block := range pvftext.Blocks(body, "table") {
		m := clearTableHeader.FindStringSubmatch(block)
		if !p.require(m != nil && !seen[m[1]], "clear reward card-slot table invalid or repeated") {
			return out
		}
		seen[m[1]] = true
		table := clearSlotTable{Name: m[1], Rewards: []clearSlotReward{}}
		body := m[2]
		rows := clearSlotRow.FindAllStringSubmatchIndex(body, -1)
		p.completeRows(body, rows, "clear reward card-slot table incomplete")
		p.require(len(rows) > 0, "clear reward card-slot table empty")
		for _, row := range rows {
			v := matchFields(body, row)
			numbers := make([]uint32, 3)
			for i, token := range v[2:5] {
				u, err := strconv.ParseUint(token, 10, 32)
				p.require(err == nil, "clear reward card-slot value outside width")
				numbers[i] = uint32(u)
			}
			p.require(numbers[1] > 0 && numbers[2] > 0, "clear reward card-slot weight or quantity invalid")
			table.Rewards = append(table.Rewards, clearSlotReward{v[1], numbers[0], numbers[1], numbers[2]})
		}
		out = append(out, table)
	}
	return out
}

func (p *input) clearBlank(body, label string) []clearBlankItem {
	out := []clearBlankItem{}
	if strings.TrimSpace(body) == "" {
		return out
	}
	rows := p.towerUnsigned(body, 768, label)
	if !p.require(len(rows)%3 == 0, "clear reward "+label+" block incomplete") {
		return out
	}
	for i := 0; i < len(rows); i += 3 {
		v := rows[i : i+3]
		p.require(slices.Min(v) > 0, "clear reward "+label+" row invalid")
		out = append(out, clearBlankItem{v[0], v[1], v[2]})
	}
	return out
}
