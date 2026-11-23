package commerce

import (
	"encoding/json"
	"fmt"
	"maps"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"s9a1/server/internal/catalogprep/gm"
	"s9a1/server/internal/catalogprep/towns"
	"s9a1/server/internal/storage"
)

type Shops struct {
	TownPeers, DungeonPeers   []storage.CommercePeer
	OculusPeers               []storage.OculusMerchantPeer
	TownAreas                 []storage.CommerceArea
	TownPhases                map[uint16][]storage.CommercePhase
	TownMaps                  map[string][]uint32
	Visibility                storage.CommerceVisibility
	Products, DungeonProducts []storage.CommerceProduct
	Disassemblers, RepairNPCs []uint32
	PaymentTemplates          map[uint32]*ItemTemplate `json:"-"`
	PurchaseTemplates         map[uint32]*ItemTemplate `json:"-"`
	InputIDs                  []uint32                 `json:"-"`
	inputIDs                  map[uint32]bool
}

type shopKey struct{ npc, shop uint32 }
type productKey struct {
	npc, shop uint32
	job       byte
	id        uint32
}

var (
	npcItemShop        = regexp.MustCompile("(?i)\x60\\[item shop\\]\\x60\\s+([0-9]+)(?:\\s|$)")
	npcDisjoint        = regexp.MustCompile("(?i)\x60\\[disjoint item\\]\\x60\\s+0(?:\\s|$)")
	npcSimple          = regexp.MustCompile("(?i)\x60\\[simple shop\\]\\x60\\s+1000(?:\\s|$)")
	seriaCurrencyBlock = regexp.MustCompile("(?ims)^\\[show account common inventory item on itemshop\\].*?^\\[/show account common inventory item on itemshop\\]\\s*")
)

func BuildShops(items *Items, town *towns.Catalog) (*Shops, error) {
	if town == nil {
		return nil, fmt.Errorf("commerce NPC directory requires parsed town maps")
	}
	r := &Shops{
		TownPeers: []storage.CommercePeer{}, DungeonPeers: []storage.CommercePeer{}, OculusPeers: []storage.OculusMerchantPeer{},
		TownAreas: []storage.CommerceArea{}, TownPhases: make(map[uint16][]storage.CommercePhase), TownMaps: make(map[string][]uint32),
		Products: []storage.CommerceProduct{}, DungeonProducts: []storage.CommerceProduct{}, inputIDs: map[uint32]bool{},
		PaymentTemplates: map[uint32]*ItemTemplate{}, PurchaseTemplates: map[uint32]*ItemTemplate{},
		Disassemblers: []uint32{}, RepairNPCs: []uint32{},
	}
	mapsByPath := map[string][]towns.NPC{}
	for _, m := range town.Maps {
		mapsByPath[strings.ToLower(m.Path)] = m.Npcs
	}
	npcs := map[uint32]bool{}
	for _, a := range town.CommerceAreas {
		row := storage.CommerceArea{Town: a.Town, Area: a.Area, Path: a.Path, Phases: emptySlice(a.Phases)}
		row.Permission.Level, row.Permission.Blocked = a.Permission.Level, a.Permission.Blocked
		row.Permission.All, row.Permission.Any, row.Permission.Except, row.Permission.Started = emptySlice(a.Permission.All), emptySlice(a.Permission.Any), emptySlice(a.Permission.Except), emptySlice(a.Permission.Started)
		r.TownAreas = append(r.TownAreas, row)
		for _, p := range append([]string{a.Path}, a.Phases...) {
			path := strings.ToLower(p)
			if _, exists := r.TownMaps[path]; exists {
				continue
			}
			placements, defined := mapsByPath[path]
			// Commerce also accepts the maps retained solely for its native phase directory.
			if !defined {
				text, err := items.source.ReadText(p, scriptLimit)
				if err != nil {
					continue
				}
				placements = towns.NPCs(text)
			}
			if a.Town == 38 && a.Area == 1 && path == seriaRoom {
				placements, _ = seriaNPCs(items.source)
			}
			set := map[uint32]bool{}
			for _, npc := range placements {
				set[npc.Id] = true
				npcs[npc.Id] = true
			}
			r.TownMaps[path] = sortedIDs(set)
		}
	}
	for townID, rules := range town.Phases {
		row := make([]storage.CommercePhase, 0, len(rules))
		for _, p := range rules {
			row = append(row, storage.CommercePhase{Kind: p.Kind, Id: p.Id, Index: p.Index})
		}
		r.TownPhases[townID] = row
	}
	npcIndex, err := items.source.ReadIndex("npc")
	if err != nil {
		return nil, err
	}
	shops, err := items.source.ReadIndex("itemshop")
	if err != nil {
		return nil, err
	}
	texts := map[uint32]string{}
	for _, id := range sortedIDs(npcs) {
		if path := npcIndex[int64(id)]; path != "" {
			if text, err := items.source.ReadText(path, scriptLimit); err == nil {
				texts[id] = text
			}
		}
	}
	productCache := map[uint32]npcOffer{}
	templateCache := map[uint32]*ItemTemplate{}
	peers := map[shopKey]map[uint32]bool{}
	repaired := map[uint32]bool{}
	townProducts := map[productKey]storage.CommerceProduct{}
	dungeonProducts := map[productKey]storage.CommerceProduct{}
	sourceScripts := map[uint32]string{}
	seriaPrepared := false
	qualifiedNPCs := sortedIDs(npcs)
	for _, id := range qualifiedNPCs {
		text := texts[id]
		if text == "" {
			continue
		}
		role, err := commerceBlock(text, "role")
		if err != nil {
			continue
		}
		slots := npcItemShop.FindAllStringSubmatch(role, -1)
		disjoint := npcDisjoint.MatchString(role)
		if len(slots) == 0 && !disjoint && !hasNativeCraftRole(role) {
			continue
		}
		sourceScripts[id] = text
		if disjoint {
			r.Disassemblers = append(r.Disassemblers, id)
		}
		for _, slot := range slots {
			n, err := strconv.ParseUint(slot[1], 10, 32)
			if err != nil || n == 0 || shops[int64(n)] == "" {
				continue
			}
			shop := uint32(n)
			text, err := items.source.ReadText(shops[int64(n)], scriptLimit)
			if err != nil {
				continue
			}
			regional := false
			if id == 2 && shop == 15 {
				if g, e := items.source.ReadText("itemshop/(r)equipmentshop7.shp", scriptLimit); e == nil {
					text, regional = g, true
				}
			}
			ownerRaw, err := commerceSection(text, "NPC")
			if err != nil {
				continue
			}
			owner64, err := strconv.ParseUint(ownerRaw, 10, 32)
			if err != nil || owner64 == 0 || npcIndex[int64(owner64)] == "" {
				continue
			}
			owner := uint32(owner64)
			if _, err := items.source.ReadText(npcIndex[int64(owner)], scriptLimit); err != nil {
				continue
			}
			k := shopKey{owner, shop}
			if peers[k] == nil {
				peers[k] = make(map[uint32]bool)
			}
			peers[k][id], repaired[id] = true, true
			if regional && !seriaPrepared {
				if offer, err := items.seriaOffer(text); err == nil && offer.product.TemplateID != 0 {
					seriaPrepared = true
					for job := range 16 {
						r.put(townProducts, owner, shop, byte(job), offer)
					}
				}
			}
			vector, err := shopProducts(text)
			if err != nil {
				continue
			}
			for job, ids := range vector {
				for _, template := range ids {
					if seriaPrepared && owner == 2 && shop == 15 && template == 490709686 {
						continue
					}
					offer, seen := productCache[template]
					if !seen {
						offer, _ = items.readNpcOffer(template, false, templateCache)
						productCache[template] = offer
					}
					if offer.product.TemplateID != 0 {
						r.put(townProducts, owner, shop, byte(job), offer)
					}
				}
			}
		}
	}
	for _, key := range sortedShopKeys(peers) {
		r.TownPeers = append(r.TownPeers, storage.CommercePeer{NPC: key.npc, Shop: key.shop, Placements: sortedIDs(peers[key])})
	}
	r.RepairNPCs = sortedIDs(repaired)
	r.addDungeonShops(items, npcIndex, shops, dungeonProducts)
	r.Products = sortedProducts(townProducts)
	r.DungeonProducts = sortedProducts(dungeonProducts)
	r.InputIDs = sortedIDs(r.inputIDs)
	r.Visibility, err = buildVisibility(items.source, sourceScripts)
	if err != nil {
		return nil, err
	}
	return r, nil
}

func hasNativeCraftRole(role string) bool {
	for _, kind := range []string{"reinforce", "amplify", "refine", "remodel", "scorn"} {
		if strings.Contains(role, "`["+kind+"]`") {
			return true
		}
	}
	return false
}

func emptySlice[T any](value []T) []T {
	if value == nil {
		return []T{}
	}
	return value
}

func sortedIDs[T any](m map[uint32]T) []uint32 {
	out := slices.Collect(maps.Keys(m))
	slices.Sort(out)
	return emptySlice(out)
}

func sortedShopKeys[T any](m map[shopKey]T) []shopKey {
	out := slices.Collect(maps.Keys(m))
	slices.SortFunc(out, func(a, b shopKey) int {
		if a.npc != b.npc {
			if a.npc < b.npc {
				return -1
			}
			return 1
		}
		if a.shop < b.shop {
			return -1
		}
		if a.shop > b.shop {
			return 1
		}
		return 0
	})
	return out
}

func sortedProducts(m map[productKey]storage.CommerceProduct) []storage.CommerceProduct {
	out := slices.Collect(maps.Values(m))
	slices.SortFunc(out, func(a, b storage.CommerceProduct) int {
		for _, pair := range [][2]uint32{{a.NPC, b.NPC}, {a.Shop, b.Shop}, {uint32(a.Job), uint32(b.Job)}, {a.TemplateID, b.TemplateID}} {
			if pair[0] < pair[1] {
				return -1
			}
			if pair[0] > pair[1] {
				return 1
			}
		}
		return 0
	})
	return emptySlice(out)
}

func (s *Shops) put(out map[productKey]storage.CommerceProduct, npc, shop uint32, job byte, offer npcOffer) {
	row := offer.product
	row.NPC, row.Shop, row.Job = npc, shop, job
	if len(row.Template) != 0 {
		out[productKey{npc, shop, job, row.TemplateID}] = row
	}
	s.inputIDs[row.TemplateID] = true
	for id, t := range offer.inputs {
		s.inputIDs[id] = true
		if offer.purchase && s.PurchaseTemplates[id] == nil {
			s.PurchaseTemplates[id] = t
		}
		if offer.purchase && id != row.TemplateID && s.PaymentTemplates[id] == nil {
			s.PaymentTemplates[id] = t
		}
	}
	for _, p := range row.Materials {
		s.inputIDs[p.TemplateID] = true
	}
}

func (i *Items) seriaOffer(text string) (npcOffer, error) {
	const productID, materialID = 490709686, 490709560
	for tag, want := range map[string]string{"NPC": "2", "type": "`[etc shop]`", "show account common inventory item on itemshop": "490709560"} {
		v, err := gm.ItemField(text, tag)
		if err != nil || v != want {
			return npcOffer{}, fmt.Errorf("Seria regional shop binding or common currency differs")
		}
	}
	visible, err := shopProducts(seriaCurrencyBlock.ReplaceAllString(text, ""))
	if err != nil || !slices.Contains(visible[0], uint32(productID)) {
		return npcOffer{}, err
	}
	src, err := i.Source(productID)
	if err != nil {
		return npcOffer{}, err
	}
	fields, err := gm.ItemFields(src.Text)
	if err != nil {
		return npcOffer{}, err
	}
	known := []string{"name", "explain", "flavor text", "grade", "rarity", "usable job", "attach type", "minimum level", "icon", "icon mark", "stackable type",
		"move wav", "expiration date", "stack limit", "booster info", "etc", "hide booster info", "purchase limit", "need material"}
	for _, f := range fields {
		if !slices.Contains(known, strings.TrimPrefix(f.Name, "/")) {
			return npcOffer{}, fmt.Errorf("Seria gift has unknown acquisition metadata")
		}
	}
	for tag, want := range map[string]string{"purchase limit": "`account` `daily` 1", "need material": "490709560 32", "stackable type": "`[booster]` 0",
		"attach type": "`[account]`", "expiration date": "`2030-10-14 06:00:00`"} {
		v, err := gm.ItemField(src.Text, tag)
		if err != nil || v != want {
			return npcOffer{}, fmt.Errorf("Seria gift price, quota or acquisition rules differ")
		}
	}
	p, err := src.Gift()
	if err != nil {
		return npcOffer{}, err
	}
	m, err := i.Gift(materialID)
	if err != nil || !m.AccountCommonInventory || !m.FatigueTimeMonthlyDeletion {
		return npcOffer{}, fmt.Errorf("Seria coin requires its monthly account-common source")
	}
	raw, err := json.Marshal(p)
	if err != nil {
		return npcOffer{}, err
	}
	return npcOffer{product: storage.CommerceProduct{TemplateID: productID, Template: raw, MinimumLevel: p.MinimumLevel,
		Materials: []storage.CommercePayment{{TemplateID: materialID, Count: 32}}, Limits: []storage.CommerceLimit{{Account: true, Period: 1, Count: 1}}},
		inputs: map[uint32]*ItemTemplate{productID: p, materialID: m}, purchase: true}, nil
}

func (s *Shops) addDungeonShops(i *Items, npcs, shops map[int64]string, out map[productKey]storage.CommerceProduct) {
	read := func(npc, shop uint32) (string, string, bool) {
		if npcs[int64(npc)] == "" || shops[int64(shop)] == "" {
			return "", "", false
		}
		n, e := i.source.ReadText(npcs[int64(npc)], scriptLimit)
		q, se := i.source.ReadText(shops[int64(shop)], scriptLimit)
		return n, q, e == nil && se == nil
	}
	if npc, text, ok := read(1000, 1000); ok {
		role, _ := commerceBlock(npc, "role")
		visible, _ := commerceSection(npc, "visible on dungeon clear")
		owner, _ := commerceSection(text, "NPC")
		vector, err := shopProducts(text)
		if visible == "1" && owner == "1000" && npcSimple.MatchString(role) && err == nil {
			s.DungeonPeers = append(s.DungeonPeers, storage.CommercePeer{NPC: 1000, Shop: 1000})
			purchases := map[uint32]npcOffer{}
			templates := map[uint32]*ItemTemplate{}
			for j, rows := range vector {
				for _, id := range rows {
					q, ready := purchases[id]
					if !ready {
						q, _ = i.readNpcOffer(id, true, templates)
						purchases[id] = q
					}
					if q.product.TemplateID != 0 {
						s.put(out, 1000, 1000, byte(j), q)
					}
				}
			}
		}
	}
	for _, shop := range []struct {
		npc, shop       uint32
		items, dungeons []uint32
	}{
		{100000277, 100000075, []uint32{10312870, 10313330, 10313331, 10313949}, []uint32{100000007, 100002669}},
		{400001123, 400000105, []uint32{490015880, 490015881, 490015882, 490015883}, []uint32{400000389}},
	} {
		node, text, ok := read(shop.npc, shop.shop)
		if !ok {
			continue
		}
		role, _ := commerceBlock(node, "role")
		click, _ := commerceSection(node, "enable click in dungeon")
		owner, _ := commerceSection(text, "NPC")
		typ, _ := commerceSection(text, "type")
		roles, _ := gm.ItemFields(node)
		fields, err := gm.ItemFields(text)
		shape := []string{"npc", "type", "sell info", "item list", "/item list", "/sell info"}
		if strings.Join(strings.Fields(role), " ") != fmt.Sprintf("`[item shop]` %d", shop.shop) || len(matchingFields(roles, "/role")) != 1 ||
			click != "1" || owner != strconv.FormatUint(uint64(shop.npc), 10) || typ != "`[weapon shop]`" || err != nil ||
			len(fields) < 6 || len(fields) > 7 || len(fields) == 7 && !strings.EqualFold(fields[6].Name, "message") {
			continue
		}
		valid := true
		for j, f := range fields[:6] {
			if !strings.EqualFold(f.Name, shape[j]) || (j == 2 || j == 4 || j == 5) && f.Value != "" {
				valid = false
			}
		}
		vector, err := shopProducts(text)
		if !valid || err != nil {
			continue
		}
		for _, d := range shop.dungeons {
			s.OculusPeers = append(s.OculusPeers, storage.OculusMerchantPeer{Dungeon: d, NPC: shop.npc, Shop: shop.shop})
		}
		for _, id := range shop.items {
			r, err := i.Source(id)
			if err != nil || r.Equipment {
				continue
			}
			offer, err := i.ordinaryOffer(r, true)
			if err != nil {
				continue
			}
			for j, ids := range vector {
				if slices.Contains(ids, id) {
					s.put(out, shop.npc, shop.shop, byte(j), offer)
				}
			}
		}
	}
}
