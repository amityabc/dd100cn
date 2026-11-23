package commerce

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"maps"
	"math"
	"path"
	"regexp"
	"slices"
	"strings"

	"s9a1/server/internal/catalogprep/fate"
	"s9a1/server/internal/catalogprep/gm"
	"s9a1/server/internal/catalogprep/pvftext"
	"s9a1/server/internal/itemdata"
	"s9a1/server/internal/storage"
)

const fateExpiry = "2030-11-11 06:00:00"
const fateMaterial uint32 = 490023498

var fateOffers = map[uint32]struct {
	cost   uint32
	period byte
	typ    string
	tags   string
}{
	490023504: {300, 0, "`[booster]` 0", "booster info|etc|hide booster info|action usable place"},
	490023506: {15, 1, "`[booster]` 0", "booster info|etc|hide booster info|action usable place"},
	490023507: {60, 2, "`[booster]` 0", "booster info|etc|hide booster info|action usable place"},
	490023509: {200, 0, "`[etc]` 1", "equipment amplify reinforcement ticket|action usable place|hide booster info"},
	490023510: {20, 2, "`[booster selection]` 0", "booster selection num|booster select category|etc|stackable|hide booster info|default select auto job and growType|booster category num"},
	490023514: {200, 0, "`[booster selection]` 0", "booster selection num|booster select category|etc|stackable|hide booster info|default select auto job and growType|booster category num|booster category name|action usable place"},
	490023515: {30, 0, "`[waste]` 0", "action type|action expiration info|action usable place"},
}

type fateCommerceSource struct {
	items    *Items
	identity map[string]string
}

func (s *fateCommerceSource) read(member string) (string, error) {
	text, hash, err := sourceTextAndHash(s.items.source, member)
	if err == nil {
		s.identity[member] = hash
	}
	return text, err
}

func (s *fateCommerceSource) indexed(category string, id uint32, expected string) (string, error) {
	index, err := s.items.source.ReadIndex(category)
	if err != nil {
		return "", err
	}
	member := strings.ToLower(strings.ReplaceAll(index[int64(id)], "\\", "/"))
	if !strings.HasPrefix(member, category+"/") || expected != "" && member != expected {
		return "", fmt.Errorf("activity %s %d maps to an unreviewed member", category, id)
	}
	s.identity[fmt.Sprintf("%s:%d", category, id)] = member
	return s.read(member)
}

func (s *fateCommerceSource) item(id uint32) (gm.ItemSource, error) {
	r, err := s.items.Source(id)
	if err != nil {
		return r, err
	}
	member := fmt.Sprintf("stackable/490023001/chn_%d.stk", id)
	if id == 10319615 {
		member = "stackable/10319001/10319615.stk"
	}
	if r.Equipment || r.Path != member {
		return r, fmt.Errorf("activity item %d maps to an unreviewed member", id)
	}
	s.identity[fmt.Sprintf("stackable:%d", id)], s.identity[member] = member, r.Hash
	return r, nil
}

func (s *fateCommerceSource) hash(prefix string) string {
	h := sha256.New()
	fmt.Fprintln(h, prefix)
	for _, k := range slices.Sorted(maps.Keys(s.identity)) {
		fmt.Fprintf(h, "%s\x00%s\n", k, s.identity[k])
	}
	return strings.ToUpper(hex.EncodeToString(h.Sum(nil)))
}

func BuildFateCommerce(items *Items, event *fate.Catalog) (*storage.FateExchangeCatalog, storage.FateBoxesCatalog, []uint32, error) {
	var boxes storage.FateBoxesCatalog
	if event == nil {
		return nil, boxes, nil, nil
	}
	if event.EventId != 4171 || event.Town.EntryNpc != 400001285 || event.Town.CampNpc != 400001286 ||
		event.Town.Seria.Town != 38 || event.Town.Seria.Area != 1 || event.Town.Camp.Town != 135 || event.Town.Camp.Area != 0 || len(event.Rewards) != 5 {
		return nil, boxes, nil, fmt.Errorf("Fate exchange requires event4171 and its complete native town route")
	}
	s := &fateCommerceSource{items: items, identity: map[string]string{}}
	shops := []storage.FateExchangeShop{{NPC: 400001285, Shop: 400000197, Town: 38, Area: 1}, {NPC: 400001286, Shop: 400000199, Town: 135, Area: 0}}
	for i, shop := range shops {
		if err := s.shop(i, shop); err != nil {
			return nil, boxes, nil, err
		}
	}
	m, err := s.item(fateMaterial)
	if err != nil {
		return nil, boxes, nil, err
	}
	material, err := fateItem(m, "`[material]` 0", "`[account]`", fateExpiry, "stack in account common item inventory", true, false)
	if err != nil || !giftHasField(m.Text, "stack in account common item inventory") || cashField(m, "stack in account common item inventory") != "" {
		return nil, boxes, nil, fmt.Errorf("Fate merchant common material differs")
	}
	materialRaw, err := json.Marshal(material)
	if err != nil {
		return nil, boxes, nil, err
	}
	ids := map[uint32]bool{fateMaterial: true}
	boxes.Stages = map[uint32]storage.FateBoxRule{}
	boxes.StageHash, boxes.StageReactivated = event.ResourceHash, true
	for _, reward := range event.Rewards {
		var t ItemTemplate
		if json.Unmarshal(reward.Template, &t) != nil || t.TemplateId == 0 || t.Kind != 2 || t.FateCollection || t.ResourceExpiration != nil {
			return nil, boxes, nil, fmt.Errorf("Fate stage source template is not intact")
		}
		r, err := items.Source(t.TemplateId)
		if err != nil || r.Hash != t.ResourceHash {
			return nil, boxes, nil, fmt.Errorf("Fate stage source definition changed")
		}
		count, hide, err := fateGiftPool(r, fateMaterial, 1000, 0)
		if err != nil {
			return nil, boxes, nil, err
		}
		boxes.Stages[t.TemplateId] = storage.FateBoxRule{Source: reward.Template, Reward: materialRaw, RewardCount: count, HideBoosterInfo: hide, RewardAttachType: 5}
		ids[t.TemplateId] = true
	}
	if len(boxes.Stages) != 5 {
		return nil, boxes, nil, fmt.Errorf("Fate stage sources repeat")
	}
	offers := map[uint32]storage.FateExchangeOffer{}
	for _, id := range sortedIDs(fateOffers) {
		policy := fateOffers[id]
		r, err := s.item(id)
		if err != nil {
			return nil, boxes, nil, err
		}
		t, err := fateItem(r, policy.typ, "`[account]`", fateExpiry, policy.tags+"|purchase limit|need material", false, false)
		if err != nil {
			return nil, boxes, nil, err
		}
		cost, err := commerceBlock(r.Text, "need material")
		if err != nil || strings.Join(strings.Fields(cost), " ") != fmt.Sprintf("%d %d", fateMaterial, policy.cost) {
			return nil, boxes, nil, fmt.Errorf("Fate product %d material cost differs", id)
		}
		limit, err := commerceBlock(r.Text, "purchase limit")
		if err != nil || strings.Join(strings.Fields(limit), " ") != "`account` `"+[]string{"accumulate", "daily", "weekly"}[policy.period]+"` 1" {
			return nil, boxes, nil, fmt.Errorf("Fate product %d purchase limit differs", id)
		}
		raw, err := json.Marshal(t)
		if err != nil {
			return nil, boxes, nil, err
		}
		offers[id] = storage.FateExchangeOffer{Product: raw, Material: materialRaw, Cost: policy.cost, Period: policy.period, SourceExpiration: fateExpiry}
		ids[id] = true
	}
	exchange := &storage.FateExchangeCatalog{Shops: shops, Offers: offers, ResourceHash: s.hash("FateExchangeAcquisition:v1:ArchivedEventReactivated"), ArchivedEventReactivated: true}
	s.identity = map[string]string{}
	boxes.Exchange, err = s.exchangeBoxes(offers, ids)
	if err != nil {
		return nil, boxes, nil, err
	}
	boxes.ExchangeHash = s.hash("FateRewardBoxUse:v1:ArchivedEventReactivated:ordinary-zero-core-detail:no-instance-expiration:no-additional-use-cost:child-actions-unavailable")
	boxes.ExchangeReactivated = true
	return exchange, boxes, sortedIDs(ids), nil
}

const fateCommonFields = "name|explain|flavor text|grade|rarity|usable job|attach type|minimum level|icon|icon mark|stackable type|move wav|expiration date|item category|stack limit|weight|field image"

func fateItem(src gm.ItemSource, typ, attachment, expiry, extra string, common, verified bool) (*ItemTemplate, error) {
	fields, err := gm.ItemFields(src.Text)
	if err != nil {
		return nil, err
	}
	allowed := strings.Split(fateCommonFields+"|"+extra, "|")
	for _, f := range fields {
		if !slices.ContainsFunc(allowed, func(name string) bool { return strings.EqualFold(name, strings.TrimLeft(f.Name, "/")) }) {
			return nil, fmt.Errorf("Fate item %d has unsupported field %s", src.Id, f.Name)
		}
	}
	s := consumableScript{fields}
	if cashField(src, "stackable type") != typ || cashField(src, "attach type") != attachment || cashField(src, "usable job") != "`[all]`" ||
		cashField(src, "minimum level") != "1" || cashField(src, "expiration date") != expiry && cashField(src, "expiration date") != "`"+expiry+"`" ||
		expiry == "" && s.has("expiration date") || !s.block("usable job", true) {
		return nil, fmt.Errorf("Fate item %d acquisition facts differ", src.Id)
	}
	stack, err := s.number("stack limit", math.MaxUint32, math.MaxUint32, false)
	if err != nil || stack == 0 {
		return nil, fmt.Errorf("Fate item %d stack limit is invalid", src.Id)
	}
	lo, hi := uint16(65), uint16(120)
	if common || src.Id == 10319615 {
		lo, hi = 121, 176
	}
	if common {
		stack = min(stack, math.MaxInt32)
	}
	return &ItemTemplate{TemplateId: src.Id, Kind: 2, FirstBagSlot: lo, LastBagSlot: hi, WornSlot: math.MaxUint16, StackLimit: stack, MinimumLevel: 1,
		UsableJobs: math.MaxUint16, ResourceHash: src.Hash, AccountCommonInventory: common,
		WeaponQualification: gm.WeaponQualification{Verified: verified, Jobs: []itemdata.WeaponJob{}, Valid: true}}, nil
}

func fateGiftPool(r gm.ItemSource, reward, weight, expected uint32) (uint32, int, error) {
	booster, err := commerceBlock(r.Text, "booster info")
	if err != nil || booster == "" {
		return 0, 0, fmt.Errorf("Fate box %d has no booster block", r.Id)
	}
	parts, err := commerceBlock(booster, "etc")
	roll, rollErr := giftInts(parts, false)
	if err != nil || rollErr != nil || len(roll) != 4 || roll[0] != 1 || roll[1] != reward || roll[2] != weight || roll[3] == 0 ||
		expected != 0 && roll[3] != expected || cashField(r, "hide booster info") != "1" {
		return 0, 0, fmt.Errorf("Fate box %d reward tuple differs", r.Id)
	}
	fields, err := gm.ItemFields(booster)
	if err != nil || len(fields) != 2 || fields[0].Name != "etc" || fields[1].Name != "/etc" || fields[1].Value != "" {
		return 0, 0, fmt.Errorf("Fate box %d contains an unsupported pool", r.Id)
	}
	return roll[3], 1, nil
}

var fateTokens = regexp.MustCompile("`[^`]*`|[^\\s]+")
var fateAreaHeader = regexp.MustCompile("^\\s*([0-9]+)\\s+`([^`]+\\.map)`")
var fateMapNpc = regexp.MustCompile("\\s*([0-9]+)\\s+`\\[(?:left|right)\\]`\\s+-?[0-9]+\\s+-?[0-9]+\\s+-?[0-9]+")

func (s *fateCommerceSource) shop(mode int, d storage.FateExchangeShop) error {
	npcNames := []string{"npc/400001285_overis_decisionsuntodestiny2_seria.npc", "npc/400001286_overis_decisionsuntodestiny2_town.npc"}
	shopNames := []string{"itemshop/chn_decisionsuntodestiny2_00.shp", "itemshop/chn_decisionsuntodestiny2_01.shp"}
	roles := []string{"`[enter town]` 400001286 `[item shop]` 400000197", "`[exit town]` 0 `[item shop]` 400000199 `[decisions unto destiny storybook open]` 0"}
	npc, err := s.indexed("npc", d.NPC, npcNames[mode])
	if err != nil {
		return err
	}
	role, err := commerceBlock(npc, "role")
	event, eventErr := commerceSection(npc, "event id")
	open, openErr := commerceSection(npc, "open visible event")
	if err != nil || eventErr != nil || openErr != nil || open != "4171" || mode == 0 && event != "4171" ||
		!slices.Equal(fateTokens.FindAllString(role, -1), fateTokens.FindAllString(roles[mode], -1)) {
		return fmt.Errorf("Fate merchant %d has a different event role", d.NPC)
	}
	shop, err := s.indexed("itemshop", d.Shop, shopNames[mode])
	if err != nil {
		return err
	}
	shopFields, err := gm.ItemFields(shop)
	if err != nil {
		return err
	}
	for _, f := range shopFields {
		if !slices.Contains([]string{"npc", "type", "sell info", "tab", "item list", "message", "show account common inventory item on itemshop"}, strings.ToLower(strings.TrimLeft(f.Name, "/"))) {
			return fmt.Errorf("Fate shop %d has unsupported field %s", d.Shop, f.Name)
		}
	}
	bound, bindErr := commerceSection(shop, "NPC")
	typ, typErr := commerceSection(shop, "type")
	if bindErr != nil || typErr != nil || bound != fmt.Sprint(d.NPC) || typ != "`[etc shop]`" {
		return fmt.Errorf("Fate shop %d binding or type differs", d.Shop)
	}
	merchantGoods := pvftext.Blocks(shop, "sell info")
	if len(merchantGoods) != 1 {
		return fmt.Errorf("Fate shop %d lacks its complete sell block", d.Shop)
	}
	tabs := pvftext.Blocks(merchantGoods[0], "tab")
	if len(tabs) != 1 {
		return fmt.Errorf("Fate shop %d lacks its unique product tab", d.Shop)
	}
	listed, err := commerceBlock(tabs[0], "item list")
	p, listErr := giftInts(listed, false)
	slices.Sort(p)
	common, commonErr := commerceBlock(shop, "show account common inventory item on itemshop")
	if err != nil || listErr != nil || commonErr != nil || common != fmt.Sprint(fateMaterial) || !slices.Equal(p, sortedIDs(fateOffers)) {
		return fmt.Errorf("Fate shop %d product or common material list differs", d.Shop)
	}
	town, err := s.indexed("town", uint32(d.Town), "")
	if err != nil {
		return err
	}
	var member string
	for _, a := range pvftext.Blocks(town, "area") {
		m := fateAreaHeader.FindStringSubmatch(a)
		if m == nil || m[1] != fmt.Sprint(d.Area) {
			continue
		}
		if member != "" {
			return fmt.Errorf("Fate merchant town area is ambiguous")
		}
		member = fateMapPath(m[2])
	}
	if member == "" {
		return fmt.Errorf("Fate merchant town area has no map")
	}
	n, ok, err := s.mapNPCs(member, d.NPC, false)
	if err != nil || !ok || n != 1 {
		return fmt.Errorf("Fate merchant %d placement differs: %v", d.NPC, err)
	}
	return nil
}

func fateMapPath(value string) string {
	v := strings.ToLower(strings.ReplaceAll(value, "\\", "/"))
	if strings.HasPrefix(v, "map/") || strings.HasPrefix(v, "commonmap/") || strings.HasPrefix(v, "live/") {
		return v
	}
	return "map/" + v
}

func (s *fateCommerceSource) mapNPCs(member string, id uint32, imported bool) (int, bool, error) {
	text, err := s.read(member)
	if err != nil && imported && errors.Is(err, fs.ErrNotExist) && !strings.HasPrefix(path.Base(member), "(r)") {
		text, err = s.read(path.Join(path.Dir(member), "(r)"+path.Base(member)))
	}
	if err != nil {
		return 0, false, err
	}
	fields, err := gm.ItemFields(text)
	if err != nil {
		return 0, false, err
	}
	positions := map[string]bool{}
	for _, f := range fields {
		if strings.HasPrefix(f.Name, "/") {
			positions[strings.ToLower(f.Name[1:])] = true
		}
	}
	var stack []string
	for _, f := range fields {
		n := strings.ToLower(f.Name)
		if strings.HasPrefix(n, "/") {
			if len(stack) == 0 || stack[len(stack)-1] != n[1:] {
				return 0, false, fmt.Errorf("Fate town map scope is unbalanced")
			}
			stack = stack[:len(stack)-1]
			continue
		}
		if len(stack) != 0 && (n == "npc" || n == "import script") {
			return 0, false, fmt.Errorf("Fate merchant NPC or import has a conditional scope")
		}
		if positions[n] {
			stack = append(stack, n)
		}
	}
	if len(stack) != 0 {
		return 0, false, fmt.Errorf("Fate town map scope is incomplete")
	}
	npcs, found := 0, false
	if dependency, err := commerceSection(text, "import script"); err != nil {
		return 0, false, err
	} else if dependency != "" {
		if imported || len(dependency) < 3 || dependency[0] != '`' || dependency[len(dependency)-1] != '`' {
			return 0, false, fmt.Errorf("Fate town map import is unqualified")
		}
		p := fateMapPath(dependency[1 : len(dependency)-1])
		if p == member {
			return 0, false, fmt.Errorf("Fate town map has a cyclic import")
		}
		npcs, found, err = s.mapNPCs(p, id, true)
		if err != nil {
			return 0, false, err
		}
	}
	for _, block := range pvftext.Blocks(text, "NPC") {
		npcs, found = 0, true
		previous := 0
		for _, row := range fateMapNpc.FindAllStringSubmatchIndex(block, -1) {
			if row[0] != previous {
				return 0, false, fmt.Errorf("Fate merchant placement tuple has unsupported syntax")
			}
			previous = row[1]
			if block[row[2]:row[3]] == fmt.Sprint(id) {
				npcs++
			}
		}
		if strings.TrimSpace(block[previous:]) != "" {
			return 0, false, fmt.Errorf("Fate merchant map placement has an unsupported condition")
		}
	}
	return npcs, found, nil
}

func (s *fateCommerceSource) exchangeBoxes(offers map[uint32]storage.FateExchangeOffer, ids map[uint32]bool) (map[uint32]storage.FateBoxRule, error) {
	out := map[uint32]storage.FateBoxRule{}
	rows := []struct {
		source, child, weight, count uint32
		typ, attach, expiry, tags    string
		attachType                   byte
	}{
		{490023504, 490023505, 1000, 1, "`[etc]` 0", "`[account]`", fateExpiry, "string data|action type|except input item|action usable place", 5},
		{490023506, 10319615, 1000, 50, "`[material]` 0", "`[trade]`", "", "need material", 1},
		{490023507, 490023508, 10000, 1, "`[waste]` 0", "`[trade]`", fateExpiry, "price|action type|transformation mythology info|transformation input|transformation output|additional effect ani|except input item|action usable place", 1},
	}
	for _, row := range rows {
		src, err := s.item(row.source)
		if err != nil {
			return nil, err
		}
		count, hide, err := fateGiftPool(src, row.child, row.weight, row.count)
		if err != nil {
			return nil, err
		}
		child, err := s.item(row.child)
		if err != nil {
			return nil, err
		}
		t, err := fateItem(child, row.typ, row.attach, row.expiry, row.tags, false, true)
		if err != nil {
			return nil, err
		}
		if err := validateFateChild(child); err != nil {
			return nil, err
		}
		var parent ItemTemplate
		if err := json.Unmarshal(offers[row.source].Product, &parent); err != nil {
			return nil, err
		}
		raw, err := json.Marshal(t)
		if err != nil {
			return nil, err
		}
		out[row.source] = storage.FateBoxRule{Source: offers[row.source].Product, Reward: raw, RewardCount: count, HideBoosterInfo: hide, RewardAttachType: row.attachType}
		ids[row.child] = true
		s.identity[fmt.Sprintf("use:%d", row.source)] = fmt.Sprintf("draw=1;reward=%d;weight=%d;count=%d;hide=%d;attach=%d;source-stack=%d;reward-stack=%d;reward-bag=%d..%d;kind=stackable;durability=0;min=1;jobs=65535;qualification=none",
			row.child, row.weight, row.count, hide, row.attachType, parent.StackLimit, t.StackLimit, t.FirstBagSlot, t.LastBagSlot)
	}
	return out, nil
}

func validateFateChild(r gm.ItemSource) error {
	value := func(n string) string { return strings.Join(fateTokens.FindAllString(cashField(r, n), -1), " ") }
	switch r.Id {
	case 490023505:
		if value("action type") != "`[change to max ability]`" || !giftHasField(r.Text, "except input item") || value("except input item") != "" ||
			value("action usable place") != "`[village]` `[seria room]` `[dont use assult]`" || len(fateTokens.FindAllString(value("string data"), -1)) != 1 || len(strings.Trim(value("string data"), "`")) == 0 {
			return fmt.Errorf("Fate child item %d action or use places differ", r.Id)
		}
	case 490023508:
		if value("price") != "0" || value("action type") != "`[transformation mythology]` 283" || !giftHasField(r.Text, "except input item") || value("except input item") != "" ||
			value("action usable place") != "`[seria room]` `[village]`" || value("additional effect ani") != "`equipment/character/Effect/100Lv_mythology.ani`" {
			return fmt.Errorf("Fate child item %d action or use places differ", r.Id)
		}
		block, err := commerceBlock(r.Text, "transformation mythology info")
		inputs, outputs := pvftext.Blocks(block, "transformation input"), pvftext.Blocks(block, "transformation output")
		if err != nil || len(inputs) != 6 || len(outputs) != 6 {
			return fmt.Errorf("Fate child item %d transformation rows are incomplete", r.Id)
		}
		for i, text := range inputs {
			in, err := giftInts(text, false)
			out, other := giftInts(outputs[i], false)
			if err != nil || other != nil || len(in) > 64 || len(out)%2 != 0 || len(out) > 128 || !distinctGiftIDs(in, false) {
				return fmt.Errorf("Fate child transformation tuple is invalid")
			}
			ids := []uint32{}
			for j := 0; j < len(out); j += 2 {
				if out[j+1] != 100 {
					return fmt.Errorf("Fate child transformation weight differs")
				}
				ids = append(ids, out[j])
			}
			if !distinctGiftIDs(ids, false) {
				return fmt.Errorf("Fate child transformation identities repeat")
			}
		}
	case 10319615:
		if value("need material") != "490023247 1" {
			return fmt.Errorf("Fate child material acquisition differs")
		}
	}
	return nil
}
