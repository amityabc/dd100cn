package commerce

import (
	"encoding/json"
	"fmt"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"s9a1/server/internal/storage"
)

var craftEventTickets = []uint32{
	8233, 8234, 8235, 8236, 8237, 8324,
	8308, 8309, 8310, 8311, 8312, 8313, 8314, 8315, 8316, 8317, 8318, 8319, 8320, 8321, 8322, 8323,
	8440, 8441, 8442, 8443, 8444, 8445, 8446, 8447, 8448, 8449, 8450, 8451, 8452, 8453, 8454, 8455,
	10008086, 10008087, 10008088, 10008089, 10008090, 10008091, 10008092, 10008093, 10008094,
	10008095, 10008096, 10008097, 10008098, 10008099, 10008100, 10008101, 10008102, 10008103,
	10008104, 10008105, 10008106, 10008107, 8238, 490017081, 490017082, 490017083,
}

var (
	craftEquipType    = regexp.MustCompile("^`(\\[[^\\]]+\\])`(?:\\s+[0-9]+)?$")
	craftSwapDisabled = regexp.MustCompile(`(?im)^\[(?:upgrade impossible|cannot upgrade|no upgrade|separate upgrade impossible)\]`)
	craftNames        = regexp.MustCompile("`([^`]+)`")
	craftEquipSlots   = map[string]int{
		"[weapon]": 12, "[title name]": 13, "[coat]": 14, "[shoulder]": 15, "[pants]": 16, "[shoes]": 17,
		"[waist]": 18, "[amulet]": 19, "[wrist]": 20, "[ring]": 21, "[support]": 22, "[magic stone]": 23, "[earring]": 25,
	}
)

func readCraftEquipment(src ItemSource) (storage.CraftEquipment, bool) {
	p := craftParser{name: "equipment"}
	fields := p.script(src.Text)
	if p.err != nil || craftSwapDisabled.MatchString(src.Text) {
		return storage.CraftEquipment{}, false
	}
	typeMatch := craftEquipType.FindStringSubmatch(strings.TrimSpace(p.field(fields, "equipment type", true)))
	if typeMatch == nil {
		return storage.CraftEquipment{}, false
	}
	kind := strings.ToLower(typeMatch[1])
	slot, exists := craftEquipSlots[kind]
	if !exists {
		return storage.CraftEquipment{}, false
	}
	integer := func(tag string, min, max int) int {
		value := strings.TrimSpace(p.field(fields, tag, false))
		if strings.ContainsAny(value, "+-") {
			p.check(false, "invalid unsigned equipment field "+tag)
		}
		return p.ints(value, min, max)
	}
	result := storage.CraftEquipment{TemplateID: src.Id, ResourceHash: src.Hash, EquipmentType: kind, TargetEquipmentSlot: slot,
		MinimumLevel: integer("minimum level", 1, 100), Grade: integer("grade", 0, 255), Rarity: integer("rarity", 0, 7)}
	block, err := commerceBlock(src.Text, "impossible contents")
	if err != nil {
		return storage.CraftEquipment{}, false
	}
	impossible := map[string]bool{}
	for _, match := range craftNames.FindAllStringSubmatch(block, -1) {
		impossible[strings.ToLower(match[1])] = true
	}
	groupText := strings.TrimSpace(p.field(fields, "remodel group", true))
	group := 0
	if groupText != "" {
		group = integer("remodel group", 1, 255)
	}
	result.CanReinforce = group == 0 && !impossible["upgrade"]
	result.CanAmplify = group == 0 && !impossible["amplify upgrade"]
	result.CanForge = group == 0 && !impossible["separate upgrade"]
	result.CanRemodel = group > 0 && !impossible["remodel"]
	if result.CanRemodel {
		result.RemodelGroup = group
	}
	return result, p.err == nil
}

type craftQuotes struct {
	normal, amplify craftUpgrade
	forge           craftForge
	remodel         craftRemodel
}

func loadCraftSource[T any](items *Items, path string, parse func(string, string) (T, error)) (T, error) {
	text, hash, err := sourceTextAndHash(items.source, path)
	if err != nil {
		var empty T
		return empty, err
	}
	out, err := parse(text, hash)
	if err != nil {
		return out, fmt.Errorf("%s: %w", path, err)
	}
	return out, nil
}

func newCraftQuotes(items *Items) (craftQuotes, error) {
	var rules craftQuotes
	var err error
	rules.normal, err = loadCraftSource(items, "etc/upgrade.etc", func(text, hash string) (craftUpgrade, error) {
		return parseCraftUpgrade(text, "normal", hash, false)
	})
	if err != nil {
		return rules, err
	}
	rules.amplify, err = loadCraftSource(items, "etc/amplifyupgrade.etc", func(text, hash string) (craftUpgrade, error) {
		return parseCraftUpgrade(text, "amplify", hash, true)
	})
	if err != nil {
		return rules, err
	}
	rules.forge, err = loadCraftSource(items, "etc/upgrade_separate.etc", parseCraftForge)
	if err != nil {
		return rules, err
	}
	rules.remodel, err = loadCraftSource(items, "etc/upgrade_remodel.etc", parseCraftRemodel)
	return rules, err
}

func (r craftQuotes) stages(e storage.CraftEquipment) storage.CraftStages {
	result := storage.CraftStages{Reinforce: make([]*storage.CraftQuote, 31), Amplify: make([]*storage.CraftQuote, 31),
		Forge: make([]*storage.CraftQuote, 8), Remodel: []*storage.CraftQuote{}}
	if e.RemodelGroup != 0 {
		result.Remodel = make([]*storage.CraftQuote, 31)
	}
	for i := range result.Reinforce {
		result.Reinforce[i] = r.normal.quote(e, i, nil)
		result.Amplify[i] = r.amplify.quote(e, i, &r.normal)
	}
	for i := range result.Forge {
		result.Forge[i] = r.forge.quote(e, i)
	}
	for i := range result.Remodel {
		result.Remodel[i] = r.remodel.quote(e, i)
	}
	return result
}

func (r craftQuotes) materialIDs() map[uint32]bool {
	ids := map[uint32]bool{}
	for _, rules := range []craftUpgrade{r.normal, r.amplify} {
		ids[rules.bonusID] = true
		for _, row := range rules.rows {
			ids[row.material.id] = true
		}
	}
	for _, rules := range []map[int]craftMaterial{r.forge.grades, r.forge.levels, r.remodel.breaks} {
		for _, m := range rules {
			ids[m.id] = true
		}
	}
	for _, rows := range r.remodel.groups {
		for _, row := range rows {
			ids[row.material.id] = true
		}
	}
	delete(ids, 0)
	return ids
}

// BuildCraft freezes the upgrade quotes and item and NPC prerequisites that the
// gameplay and storage layers can use without further resource reads.
func BuildCraft(items *Items, roots, preparedIDs []uint32, shops *Shops) (storage.CraftCatalog, map[uint32]int64, error) {
	out := storage.CraftCatalog{
		Definitions: map[uint32]storage.CraftEntry{}, Quotes: []storage.CraftStages{},
		Tickets: map[uint32]storage.CraftTicketRule{}, Enchants: map[uint32]storage.CraftBead{},
		Destruction: map[uint32]storage.CraftDestruction{}, Templates: map[uint32]json.RawMessage{},
	}
	rules, err := newCraftQuotes(items)
	if err != nil {
		return out, nil, err
	}
	breaks, err := loadCraftDestruction(items)
	if err != nil {
		return out, nil, err
	}
	books, err := loadCraftBooks(items)
	if err != nil {
		return out, nil, err
	}
	baseIDs := make(map[uint32]bool, len(roots)+len(preparedIDs)+len(craftEventTickets))
	for _, ids := range [][]uint32{roots, preparedIDs, craftEventTickets} {
		for _, id := range ids {
			baseIDs[id] = true
		}
	}
	allIDs := slices.Concat(sortedIDs(baseIDs), sortedIDs(books.items))
	slices.Sort(allIDs)
	materialIDs := rules.materialIDs()
	groups := map[[5]int]int{}
	for _, id := range slices.Compact(allIDs) {
		if path := items.equipment[int64(id)]; baseIDs[id] && path != "" {
			src, err := items.Source(id)
			if err != nil {
				return out, nil, err
			}
			if e, ok := readCraftEquipment(src); ok {
				key := e.QuoteGroup()
				index, found := groups[key]
				if !found {
					index = len(out.Quotes)
					groups[key] = index
					out.Quotes = append(out.Quotes, rules.stages(e))
				}
				out.Definitions[id] = storage.CraftEntry{Equipment: e, Quote: index}
				if row, ok := breaks.prepare(e, src.Text); ok {
					out.Destruction[id] = row
					addCraftDestructionMaterials(materialIDs, row)
				}
			}
		}
		if path := items.stackable[int64(id)]; path != "" {
			text, err := items.source.ReadText(path, scriptLimit)
			if err != nil {
				return out, nil, err
			}
			if ticket, ok := books.ticket(id, text); ok {
				_, hash, err := sourceTextAndHash(items.source, path)
				if err != nil {
					return out, nil, err
				}
				ticket.ResourceHash = hash
				out.Tickets[id] = ticket
			}
			if baseIDs[id] {
				if bead, ok := readCraftBead(items, id, path, text); ok {
					out.Enchants[id] = bead
					materialIDs[id] = true
				}
			}
		}
	}
	if shops != nil {
		out.NPCs, err = craftNPCs(items, shops)
		if err != nil {
			return out, nil, err
		}
	}
	weights := map[uint32]int64{}
	for _, id := range sortedIDs(materialIDs) {
		if weight, err := items.Weight(id); err == nil {
			weights[id] = weight
		}
		if template, err := items.Prepared(id); err == nil && template != nil {
			data, err := json.Marshal(template)
			if err != nil {
				return out, nil, err
			}
			out.Templates[id] = data
		}
	}
	return out, weights, nil
}

var craftGuildRole = regexp.MustCompile("(?i)`\\[guild npc [^\\]]+\\]`")

func craftNPCs(items *Items, shops *Shops) (map[storage.CraftMethod][]uint32, error) {
	roles := map[storage.CraftMethod]*regexp.Regexp{}
	for method, tag := range []string{"open upgrade item", "amplify item", "genuine damage upgrade"} {
		roles[storage.CraftMethod(method)] = regexp.MustCompile("(?i)`\\[" + regexp.QuoteMeta(tag) + "\\]`\\s+0(?:\\s|$)")
	}
	places := map[uint32]bool{}
	for _, ids := range shops.TownMaps {
		for _, id := range ids {
			places[id] = true
		}
	}
	index, err := items.source.ReadIndex("npc")
	if err != nil {
		return nil, err
	}
	out := map[storage.CraftMethod][]uint32{}
	for _, id := range sortedIDs(places) {
		path := index[int64(id)]
		if path == "" {
			continue
		}
		text, err := items.source.ReadText(path, scriptLimit)
		if err != nil {
			continue
		}
		role, err := commerceBlock(text, "role")
		if err != nil || craftGuildRole.MatchString(role) {
			continue
		}
		for method, pattern := range roles {
			if pattern.MatchString(role) {
				out[method] = append(out[method], id)
			}
		}
	}
	return out, nil
}

func craftUnsigned(s string, max uint64) (uint64, bool) {
	for _, r := range s {
		if r < '0' || r > '9' {
			return 0, false
		}
	}
	v, err := strconv.ParseUint(s, 10, 32)
	return v, err == nil && v <= max
}
