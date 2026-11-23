package commerce

import (
	"encoding/json"
	"fmt"
	"math"
	"regexp"
	"strings"

	"s9a1/server/internal/catalogprep/gm"
	"s9a1/server/internal/storage"
)

type npcOffer struct {
	product  storage.CommerceProduct
	inputs   map[uint32]*ItemTemplate
	purchase bool
}

var (
	purchaseRule   = regexp.MustCompile("(?i)purchase|buy|currency|cash|cera|exchange|need material|trade price|coin price|price type|price unit|limit count|sell limit|one a day|usable period|expiration date")
	instanceMarker = regexp.MustCompile("(?i)^set .+ instance type$")
	boosterBlock   = regexp.MustCompile("(?ims)^\\[booster info\\][ \t]*$(.*?)^\\[/booster info\\][^\\r\\n]*")
	guildMaterial  = regexp.MustCompile("(?im)^\\[stackable type\\][ \t]*\\r?\\n[ \t]*`\\[material\\]` 0[ \t]*\\r?\\n")
	newLimits      = regexp.MustCompile("`(account|character)`\\s+`(daily|weekly|monthly|accumulate)`\\s+([1-9][0-9]*)(?:\\s|$)")
	oldLimit       = regexp.MustCompile("^`(account|character|charac)`\\s+([1-9][0-9]*)$")
)

func (i *Items) Prepared(id uint32) (*ItemTemplate, error) {
	r, err := i.Source(id)
	if err != nil {
		return nil, err
	}
	return preparedTemplate(r)
}

func preparedTemplate(r ItemSource) (*ItemTemplate, error) {
	if !r.Equipment {
		if strings.Contains(r.Text, "[teleport potion]") {
			if kind, _ := gm.ItemField(r.Text, "stackable type"); kind == "`[teleport potion]` 0" {
				t, _, err := r.GM()
				return t, err
			}
		}
		if usesGiftBeforeCommerce(r.Id, r.Text) {
			return r.Gift()
		}
	}
	return r.Ordinary()
}

func (i *Items) readNpcOffer(id uint32, dungeon bool, templateCache map[uint32]*ItemTemplate) (npcOffer, error) {
	r, err := i.Source(id)
	if err != nil {
		return npcOffer{}, err
	}
	ordinary, ordinaryErr := i.ordinaryOffer(r, dungeon)
	if ordinaryErr == nil && len(ordinary.product.Materials) > 0 {
		return ordinary, nil
	}
	if ordinaryErr == nil && (id == 490016725 || id == 490016727) {
		return ordinary, nil
	}
	full, fullErr := i.fullNpcOffer(r, templateCache)
	if fullErr != nil {
		if ordinaryErr != nil || npcLifetime(r.Text) {
			return npcOffer{}, fullErr
		}
		return ordinary, nil
	}
	if ordinaryErr != nil || npcLifetime(r.Text) {
		return full, nil
	}
	var product ItemTemplate
	if err := json.Unmarshal(full.product.Template, &product); err != nil {
		return npcOffer{}, err
	}
	if product.PvfGiftItem || product.AccountCommonInventory || product.InitialSealed || product.AccountAfterFirstTrade ||
		product.ResourceExpiration != nil || product.RelativeLifetimeSeconds != 0 || product.DailyDeletion ||
		product.WeeklyDeletionDay != nil || product.FatigueTimeMonthlyDeletion {
		return full, nil
	}
	return ordinary, nil
}

func npcLifetime(text string) bool {
	fields, _ := gm.ItemFields(text)
	for _, f := range fields {
		switch f.Name {
		case "stack in account common item inventory", "fatiguetime monthly delete", "daily delete item", "weekly delete item", "expiration date", "usable period":
			return true
		}
	}
	return false
}

func (i *Items) ordinaryOffer(r ItemSource, dungeon bool) (npcOffer, error) {
	id := r.Id
	dpl := id == 490016725 || id == 490016727
	if dpl && r.Equipment {
		return npcOffer{}, fmt.Errorf("Seria DPL products require their original stackable resource")
	}
	for _, tag := range shopTags.FindAllStringSubmatch(r.Text, -1) {
		key := strings.ToLower(tag[1])
		if dungeon && key == "need material" || dpl && key == "expiration date" {
			continue
		}
		if purchaseRule.MatchString(key) {
			return npcOffer{}, fmt.Errorf("special-currency or limited item purchase is not supported")
		}
	}
	price, err := commerceSection(r.Text, "price")
	if err != nil {
		return npcOffer{}, err
	}
	if dpl {
		expiry, err := gm.ItemField(r.Text, "expiration date")
		if err != nil || price != "1" || expiry != "`2030-11-30 06:00:00`" {
			return npcOffer{}, fmt.Errorf("Seria DPL purchase requires its one-gold price and frozen 2030 expiration")
		}
	}
	materials := []storage.CommercePayment{}
	if dungeon {
		cost, err := commerceSection(r.Text, "need material")
		if err != nil {
			return npcOffer{}, err
		}
		if cost != "" {
			if r.Equipment || id != 2600655 || strings.Join(strings.Fields(cost), " ") != "3037 20" {
				return npcOffer{}, fmt.Errorf("dungeon material product has no matched native cost evidence")
			}
			materials = append(materials, storage.CommercePayment{TemplateID: 3037, Count: 20})
		}
	}
	n, err := unsigned(price, 32)
	if price == "" && len(materials) > 0 {
		n, err = 0, nil
	}
	if err != nil || n == 0 && len(materials) == 0 {
		return npcOffer{}, fmt.Errorf("item has no supported positive ordinary gold price")
	}
	product, err := preparedTemplate(r)
	if err != nil || product == nil {
		// Legacy serialization can still select a gift-qualified root.
		product, err = r.Gift()
	}
	var frozen json.RawMessage
	if product != nil {
		frozen, err = json.Marshal(product)
		if err != nil {
			return npcOffer{}, err
		}
	}
	return npcOffer{product: storage.CommerceProduct{TemplateID: id, Equipment: r.Equipment, Gold: uint32(n),
		Template: frozen, Materials: materials, Limits: []storage.CommerceLimit{}}}, nil
}

func (i *Items) fullNpcOffer(r ItemSource, cache map[uint32]*ItemTemplate) (npcOffer, error) {
	fields, err := gm.ItemFields(r.Text)
	if err != nil {
		return npcOffer{}, err
	}
	for _, f := range fields {
		if strings.HasPrefix(f.Name, "/") {
			continue
		}
		switch f.Name {
		case "price", "need material", "purchase limit", "daily purchase limit", "reset daily purchase item", "reset monthly purchase item", "purchase minimum level", "expiration date", "usable period", "use custom buy confirm msg":
			continue
		}
		if purchaseRule.MatchString(f.Name) && !instanceMarker.MatchString(f.Name) {
			if f.Name == "cera" && boosterBlock.MatchString(r.Text) && !purchaseRuleOutsideBooster(r.Text, f.Name) {
				continue
			}
			return npcOffer{}, fmt.Errorf("NPC product requires unsupported acquisition rule: %s", f.Name)
		}
	}
	fieldSet := make(map[string][]string)
	for _, f := range fields {
		fieldSet[f.Name] = append(fieldSet[f.Name], f.Value)
	}
	read := func(name string) (string, error) {
		v := fieldSet[name]
		if len(v) > 1 {
			return "", fmt.Errorf("ambiguous NPC item field %s", name)
		}
		if len(v) == 0 {
			return "", nil
		}
		return v[0], nil
	}
	mats, err := read("need material")
	if err != nil {
		return npcOffer{}, err
	}
	tokens := strings.Fields(mats)
	if len(tokens) == 0 && len(fieldSet["need material"]) != 0 || len(tokens) > 4 || len(tokens)%2 != 0 {
		return npcOffer{}, fmt.Errorf("NPC product needs one or two positive static material/count pairs")
	}
	inputs := map[uint32]*ItemTemplate{}
	costs := []storage.CommercePayment{}
	for n := 0; n < len(tokens); n += 2 {
		mat, err := unsigned(tokens[n], 32)
		number, ne := unsigned(tokens[n+1], 32)
		if err != nil || ne != nil || mat == 0 || number == 0 || uint32(mat) == r.Id || inputs[uint32(mat)] != nil ||
			mat == 1 || mat == 2 || mat == 10002606 || i.equipment[int64(mat)] != "" {
			return npcOffer{}, fmt.Errorf("NPC material requires a separate wallet or equipment consumption owner")
		}
		t, err := i.npcItem(uint32(mat), false, cache)
		if err != nil || t.Kind != 2 {
			return npcOffer{}, fmt.Errorf("NPC payment template %d is not supported: %w", mat, err)
		}
		inputs[uint32(mat)] = t
		costs = append(costs, storage.CommercePayment{TemplateID: uint32(mat), Count: uint32(number)})
	}
	price, err := read("price")
	if err != nil {
		return npcOffer{}, err
	}
	var gold uint64
	if len(fieldSet["price"]) != 0 {
		gold, err = unsigned(price, 32)
		if err != nil {
			return npcOffer{}, err
		}
	}
	if gold == 0 && len(costs) == 0 {
		return npcOffer{}, fmt.Errorf("NPC product has no positive static gold or material cost")
	}
	minimum, err := read("purchase minimum level")
	if err != nil {
		return npcOffer{}, err
	}
	var level uint64
	if minimum != "" {
		level, err = unsigned(minimum, 8)
		if err != nil {
			return npcOffer{}, err
		}
	}
	limits, err := npcPurchaseLimits(fieldSet)
	if err != nil {
		return npcOffer{}, err
	}
	product, err := i.npcItem(r.Id, true, cache)
	if err != nil {
		return npcOffer{}, err
	}
	inputs[r.Id] = product
	raw, err := json.Marshal(product)
	if err != nil {
		return npcOffer{}, err
	}
	return npcOffer{product: storage.CommerceProduct{TemplateID: r.Id, Equipment: r.Equipment, Gold: uint32(gold), MinimumLevel: byte(level),
		Template: raw, Materials: costs, Limits: limits}, inputs: inputs, purchase: true}, nil
}

func purchaseRuleOutsideBooster(text, name string) bool {
	outside := boosterBlock.ReplaceAllString(text, "")
	fields, _ := gm.ItemFields(outside)
	for _, f := range fields {
		if f.Name == name {
			return true
		}
	}
	return false
}

func (i *Items) npcItem(id uint32, product bool, cache map[uint32]*ItemTemplate) (*ItemTemplate, error) {
	if product && id == 490011648 {
		return nil, fmt.Errorf("guild coin acquisition requires its guild-cap owner")
	}
	if ready := cache[id]; ready != nil {
		return ready, nil
	}
	src, err := i.Source(id)
	if err != nil {
		return nil, err
	}
	if id == 490011648 {
		fields, err := gm.ItemFields(src.Text)
		if err != nil {
			return nil, err
		}
		var types []string
		for _, row := range fields {
			if row.Name == "stackable type" {
				types = append(types, row.Value)
			}
		}
		if len(types) != 2 || types[0] != "`[material]` 0" || types[1] != "`[quest]` 0" {
			return nil, fmt.Errorf("guild coin requires its confirmed payment-only classification")
		}
		src.Text = guildMaterial.ReplaceAllString(src.Text, "")
	}
	mode := gm.GiftDefault
	if product {
		mode = gm.GiftShop
	}
	t, err := src.GiftFor(mode)
	if err != nil {
		return nil, fmt.Errorf("NPC item %d has an incomplete acquisition template: %w", id, err)
	}
	cache[id] = t
	return t, nil
}

func npcPurchaseLimits(fields map[string][]string) ([]storage.CommerceLimit, error) {
	out := []storage.CommerceLimit{}
	keys := []string{"purchase limit", "daily purchase limit", "reset daily purchase item", "reset monthly purchase item"}
	for _, key := range keys {
		if len(fields[key]) > 1 {
			return nil, fmt.Errorf("ambiguous NPC purchase limit field %s", key)
		}
	}
	value := func(key string) string {
		if v := fields[key]; len(v) > 0 {
			return v[0]
		}
		return ""
	}
	primary, legacy := value(keys[0]), value(keys[1])
	daily, monthly := len(fields[keys[2]]) != 0, len(fields[keys[3]]) != 0
	if primary != "" {
		if legacy != "" || daily || monthly {
			return nil, fmt.Errorf("NPC product mixes old and new purchase limits")
		}
		rows := newLimits.FindAllStringSubmatch(primary, -1)
		left := strings.TrimSpace(newLimits.ReplaceAllString(primary, ""))
		if len(rows) == 0 || len(rows) > 2 || left != "" || len(rows) == 2 && rows[0][1] == rows[1][1] {
			return nil, fmt.Errorf("NPC purchase limit scope, period or count is unsupported or repeated")
		}
		for _, r := range rows {
			n, err := unsigned(r[3], 32)
			if err != nil || n == 0 || n > math.MaxUint32 {
				return nil, fmt.Errorf("NPC purchase limit size is invalid")
			}
			period := map[string]byte{"daily": 1, "weekly": 101, "monthly": 102}[r[2]]
			out = append(out, storage.CommerceLimit{Account: r[1] == "account", Period: period, Count: uint32(n)})
		}
		return out, nil
	}
	if legacy == "" {
		if daily || monthly || len(fields[keys[0]]) > 0 || len(fields[keys[1]]) > 0 {
			return nil, fmt.Errorf("NPC reset marker or empty purchase limit has no valid counter definition")
		}
		return out, nil
	}
	if daily && monthly || value(keys[2]) != "" || value(keys[3]) != "" {
		return nil, fmt.Errorf("NPC legacy purchase limit has invalid reset periods")
	}
	m := oldLimit.FindStringSubmatch(legacy)
	if m == nil {
		return nil, fmt.Errorf("NPC legacy purchase limit scope or count is unsupported")
	}
	n, err := unsigned(m[2], 32)
	if err != nil {
		return nil, err
	}
	var period byte
	if daily {
		period = 1
	}
	if monthly {
		period = 102
	}
	return append(out, storage.CommerceLimit{Account: m[1] == "account", Period: period, Count: uint32(n)}), nil
}
