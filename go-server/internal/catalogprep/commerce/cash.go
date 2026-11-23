package commerce

import (
	"encoding/json"
	"fmt"
	"math"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"s9a1/server/internal/catalogprep/gm"
	"s9a1/server/internal/storage"
)

const cashShopPath = "etc/(r)cerashop.etc"

type cashRow struct {
	sku, item, count, gold, reserved, cera, display, code uint32
}

type cashRestriction struct{ code, sku, period, count, scope uint32 }

type cashResources struct {
	items                                                       *Items
	shop, bonusText, couponText                                 string
	restrictions                                                map[uint32][]cashRestriction
	restrictionCount                                            map[uint32]int
	points, ceraOnly, mileage, returning, nonstackable, bonused map[uint32]int
	discounts                                                   map[uint32][]uint32
	discountNumbers, restrictionNumbers, couponNumbers          map[uint32]int
	coupons                                                     map[uint32]bool
	offers                                                      map[uint32]storage.CashOffer
	templates                                                   map[uint32]*ItemTemplate
}

type CashData struct {
	Offers                             map[uint32]storage.CashOffer
	Templates                          map[uint32]*ItemTemplate
	AccountCargo                       storage.CargoCatalog
	AccountPolicies, CharacterPolicies map[uint32]PurchasePolicy
	AccountCodes                       []uint32
}

var cashLine = regexp.MustCompile("\\s*([0-9]+)\\s+([0-9]+)\\s+([0-9]+)\\s+([0-9]+)\\s+([0-9]+)\\s+([0-9]+)\\s+\x60[^\x60]*\x60\\s+([0-9]+)\\s+([0-9]+)")
var cashRecoveryUnsupported = regexp.MustCompile("(?i)action|cash|contract|package|purchase|buy|limit count|one a day|usable period|expiration|expiry")
var npcPricingMetadata = regexp.MustCompile("(?i)purchase|buy|usable from")
var npcClassicCashLimit = regexp.MustCompile("^\x60account\x60\\s+([0-9]+)$")
var npcCurrentCashLimit = regexp.MustCompile("^\x60account\x60\\s+\x60(?:monthly|accumulate)\x60\\s+([0-9]+)$")

func BuildCash(i *Items, battlePass ...BattlePassCashSource) (*CashData, error) {
	s, err := readCashResources(i)
	if err != nil {
		return nil, err
	}
	cargo, err := s.buildCapacity()
	if err != nil {
		return nil, err
	}
	if err := s.addSimpleServices(); err != nil {
		return nil, err
	}
	if err := s.addDyes(); err != nil {
		return nil, err
	}
	if err := s.addPremiums(); err != nil {
		return nil, err
	}
	if err := s.addNameTags(); err != nil {
		return nil, err
	}
	for _, category := range []string{"recoveryitem", "coin", "creature", "item", "avatar cube", "item etc", "item mod or ext", "premium", "item event", "item event second category"} {
		rows, err := s.rows(category)
		if err != nil {
			return nil, err
		}
		for _, row := range rows {
			if _, exists := s.offers[row.sku]; exists {
				continue
			}
			if err := s.prepareRow(category, row); err != nil {
				return nil, err
			}
		}
	}
	if len(battlePass) > 1 {
		return nil, fmt.Errorf("cash catalog has multiple Battle Pass definitions")
	}
	var pass *BattlePassCashSource
	if len(battlePass) != 0 {
		pass = &battlePass[0]
	}
	if err := s.addPackages(pass); err != nil {
		return nil, err
	}
	account, character, codes, err := CashPurchasePolicies(s.offers)
	if err != nil {
		return nil, err
	}
	return &CashData{Offers: s.offers, Templates: s.templates, AccountCargo: cargo, AccountPolicies: account, CharacterPolicies: character, AccountCodes: codes}, nil
}

func readCashResources(items *Items) (*cashResources, error) {
	shop, err := readCommerceText(items.source, cashShopPath)
	if err != nil {
		return nil, err
	}
	bonuses, err := items.source.ReadText("etc/chn_cerashop_multibonus.etc", scriptLimit)
	if err != nil {
		return nil, err
	}
	coupons, err := items.source.ReadText("etc/chn_coupon.etc", scriptLimit)
	if err != nil {
		return nil, err
	}
	s := &cashResources{
		items: items, shop: shop, bonusText: bonuses, couponText: coupons, offers: map[uint32]storage.CashOffer{}, templates: map[uint32]*ItemTemplate{},
		restrictions: map[uint32][]cashRestriction{}, restrictionCount: map[uint32]int{}, discounts: map[uint32][]uint32{}, coupons: map[uint32]bool{},
	}
	for tag, to := range map[string]*map[uint32]int{
		"buy only cera point": &s.points, "buy only cera": &s.ceraOnly, "not stackable buy": &s.nonstackable,
		"specific product mileage": &s.mileage, "return user buy restrict code": &s.returning,
		"buy restrict item": &s.restrictionNumbers, "discount list": &s.discountNumbers,
	} {
		block, err := commerceBlock(shop, tag)
		if err != nil {
			return nil, err
		}
		*to = numberSet(block)
	}
	s.bonused = numberSet(bonuses)
	block, err := commerceBlock(coupons, "coupon")
	if err != nil {
		return nil, err
	}
	s.couponNumbers = numberSet(block)
	cp, err := cashIntegers(block, 3)
	if err != nil {
		return nil, err
	}
	for at := 0; at < len(cp); at += 3 {
		s.coupons[cp[at+2]] = true
	}
	limits, err := commerceBlock(shop, "buy restrict item")
	if err != nil {
		return nil, err
	}
	p, err := cashIntegers(limits, 5)
	if err != nil {
		return nil, err
	}
	for n := 0; n < len(p); n += 5 {
		r := cashRestriction{p[n], p[n+1], p[n+2], p[n+3], p[n+4]}
		s.restrictionCount[r.code]++
		s.restrictions[r.sku] = append(s.restrictions[r.sku], r)
	}
	prices, err := commerceBlock(shop, "discount list")
	if err != nil {
		return nil, err
	}
	d, err := cashIntegers(prices, 2)
	if err != nil {
		return nil, err
	}
	for n := 0; n < len(d); n += 2 {
		s.discounts[d[n]] = append(s.discounts[d[n]], d[n+1])
	}
	return s, nil
}

func cashIntegers(text string, width int) ([]uint32, error) {
	parts := numbers(text)
	if len(parts)%width != 0 {
		return nil, fmt.Errorf("cash supplemental policy grammar is invalid")
	}
	out := make([]uint32, 0, len(parts))
	for _, v := range parts {
		n, err := unsigned(v, 32)
		if err != nil {
			return nil, err
		}
		out = append(out, uint32(n))
	}
	return out, nil
}

func numberSet(text string) map[uint32]int {
	out := map[uint32]int{}
	for _, v := range numbers(text) {
		if n, err := strconv.ParseUint(v, 10, 32); err == nil {
			out[uint32(n)]++
		}
	}
	return out
}

func (s *cashResources) rows(category string) ([]cashRow, error) {
	raw, err := commerceBlock(s.shop, category)
	if err != nil {
		return nil, err
	}
	rows := []cashRow{}
	seen := map[uint32]bool{}
	at := 0
	for _, m := range cashLine.FindAllStringSubmatchIndex(raw, -1) {
		if m[0] != at || len(rows) >= 4096 {
			return nil, fmt.Errorf("cash row grammar is invalid in %s", category)
		}
		at = m[1]
		var n [8]uint32
		for i := range n {
			x, err := unsigned(raw[m[i*2+2]:m[i*2+3]], 32)
			if err != nil {
				return nil, err
			}
			n[i] = uint32(x)
		}
		if n[0] == 0 || n[0] == math.MaxUint32 || seen[n[0]] {
			return nil, fmt.Errorf("cash commodity %d is invalid or duplicated in %s", n[0], category)
		}
		seen[n[0]] = true
		rows = append(rows, cashRow{n[0], n[1], n[2], n[3], n[4], n[5], n[6], n[7]})
	}
	if strings.TrimSpace(raw[at:]) != "" {
		return nil, fmt.Errorf("cash row grammar is invalid in %s", category)
	}
	return rows, nil
}

func (r cashRow) quote() storage.CashOffer {
	return storage.CashOffer{Commodity: r.sku, TemplateID: r.item, Count: r.count, Cera: r.cera, Gold: r.gold, AccountPeriod: 101, CharacterPeriod: 1,
		BonusItems: []storage.CommercePayment{}, OwnedItemExclusions: []uint32{}, RequiredCouponTemplates: map[uint32]json.RawMessage{}}
}

func (r cashRow) ceraQuote() bool {
	return r.item > 0 && r.item < math.MaxUint32 && r.count > 0 && r.count <= math.MaxUint16 && r.gold == 0 && r.reserved == 0 && r.cera > 0 && r.cera <= math.MaxInt32
}

func (s *cashResources) add(quote storage.CashOffer, t *ItemTemplate) error {
	if _, exists := s.offers[quote.Commodity]; exists {
		return fmt.Errorf("cash commodity %d appears twice", quote.Commodity)
	}
	if t != nil {
		raw, err := json.Marshal(t)
		if err != nil {
			return err
		}
		quote.Template = raw
		s.templates[t.TemplateId] = t
	}
	s.offers[quote.Commodity] = quote
	return nil
}

func (s *cashResources) restriction(r cashRow, period byte, account bool) (cashRestriction, bool) {
	var none cashRestriction
	rows := s.restrictions[r.sku]
	if len(rows) != 1 {
		return none, false
	}
	p := rows[0]
	if p.code == 0 || p.code > math.MaxInt32 || p.period != uint32(period) || p.count == 0 || p.count > math.MaxUint8 || s.restrictionCount[p.code] != 1 ||
		p.scope > 1 || (p.scope == 1) != account {
		return none, false
	}
	return p, true
}

func (s *cashResources) ordinarySupplemental(r cashRow, discount bool) bool {
	if s.points[r.item] != 0 || s.bonused[r.sku] != 0 || s.coupons[r.sku] || len(s.restrictions[r.sku]) != 0 {
		return false
	}
	rows := s.discounts[r.sku]
	return len(rows) == 0 || discount && len(rows) == 1 && rows[0] >= r.cera && rows[0] <= math.MaxInt32
}

func (s *cashResources) prepareRow(section string, r cashRow) error {
	if section == "item etc" {
		if handled, err := s.extras(r); handled || err != nil {
			return err
		}
	}
	switch section {
	case "recoveryitem", "coin", "creature":
		if r.ceraQuote() && r.display == 0 && r.code == 0 && s.ordinarySupplemental(r, section == "coin") {
			src, err := s.items.Source(r.item)
			if err == nil {
				roles, _ := gm.ItemFields(src.Text)
				typ, _ := gm.ItemField(src.Text, "stackable type")
				switch section {
				case "recoveryitem":
					if !src.Equipment && typ == "`[waste]` 0" && cashField(src, "usable job") == "`[all]`" &&
						!slices.ContainsFunc(roles, func(f gm.Field) bool { return cashRecoveryUnsupported.MatchString(f.Name) }) {
						if t, err := src.Ordinary(); err == nil {
							return s.add(r.quote(), t)
						}
					}
				case "coin":
					if r.item == 1 && !src.Equipment && typ == "`[etc]` 0" && cashField(src, "usable job") == "`[all]`" && cashField(src, "attach type") == "`[trade delete]`" {
						q := r.quote()
						q.RevivalCoins = true
						return s.add(q, nil)
					}
				case "creature":
					if src.Equipment && r.count == 1 && cashField(src, "equipment type") == "`[creature]` 0" && cashField(src, "sub type") == "1" && cashField(src, "attach type") == "`[sealing trade]`" {
						t, err := src.SpecialGM(nil)
						if err != nil {
							return nil
						}
						t.Template.InitialSealed = false
						raw, err := json.Marshal(t.Template)
						if err != nil {
							return err
						}
						q := r.quote()
						q.PetTemplate = raw
						return s.add(q, nil)
					}
				}
			}
		}
		if section == "coin" {
			return nil
		}
		if section == "recoveryitem" {
			return s.fatigueOffer(r)
		}
	case "item event", "item event second category":
		return s.periodicOffer(section, r)
	}
	return s.ordinaryCashOffer(section, r)
}

func cashField(r ItemSource, name string) string {
	value, _ := gm.ItemField(r.Text, name)
	return value
}

func (s *cashResources) ordinaryCashOffer(section string, r cashRow) error {
	capsule := section == "creature" && r.item == 2683241 && r.code == 70052 && r.display == 0 && r.count == 1 && s.mileage[r.sku] == 0 && s.returning[r.sku] == 0 && s.returning[r.code] == 0
	if !r.ceraQuote() || r.item == 2660239 || r.display > 2 || r.code != 0 && !capsule || !s.ordinarySupplemental(r, true) {
		return nil
	}
	src, err := s.items.Source(r.item)
	if err != nil || src.Equipment {
		return nil
	}
	fields, err := gm.ItemFields(src.Text)
	if err != nil {
		return nil
	}
	typ := cashField(src, "stackable type")
	action := cashField(src, "action type")
	avatar := typ == "`[avatar gift]` 0" && cashField(src, "avatar gift type") != "" || typ == "`[etc]` 0" && cashField(src, "avatar select ability change") != ""
	actionSource := action == "`[open creature skin slot]`" && typ == "`[etc]` 1" || strings.HasPrefix(action, "`[change avatar]` ") && typ == "`[etc]` 0"
	if actionSource && (s.nonstackable[r.sku] > 0 || s.nonstackable[r.item] > 0 || s.mileage[r.sku] > 0 || s.returning[r.sku] > 0) {
		return nil
	}
	if (section == "item mod or ext" || section == "premium") && !avatar && !actionSource {
		return nil
	}
	valid := avatar || actionSource
	switch section {
	case "creature":
		valid = typ == "`[feed]` 1" || typ == "`[creature]` 1" || capsule && typ == "`[etc]` 0"
	default:
		valid = valid || slices.Contains([]string{"`[etc]` 0", "`[material]` 0", "`[material]` 1", "`[waste]` 0", "`[town and dungeon]` 0", "`[equipment effect]` 0"}, typ)
	}
	if !valid || !cashAcquisition(fields) {
		return nil
	}
	if section == "item etc" && r.item != 490704493 && r.item != 490709218 && !avatar && !actionSource {
		if typ != "`[material]` 0" || !slices.ContainsFunc(fields, func(f gm.Field) bool { return f.Name == "purchase limit" || f.Name == "daily purchase limit" }) {
			return nil
		}
	}
	t, err := src.Gift()
	if err != nil || t.Kind != 2 || t.CashPackage {
		return nil
	}
	q := r.quote()
	if capsule {
		q.AccountRestriction, q.AccountLimit, q.AccountPeriod = r.code, 1, 0
	}
	return s.add(q, t)
}

func cashAcquisition(fields []gm.Field) bool {
	limit, limitEnd, mat, matEnd := -1, -1, -1, -1
	for i, f := range fields {
		switch f.Name {
		case "daily purchase limit", "purchase limit":
			if limit >= 0 {
				return false
			}
			limit = i
		case "/purchase limit":
			if limitEnd >= 0 {
				return false
			}
			limitEnd = i
		case "need material":
			if mat >= 0 {
				return false
			}
			mat = i
		case "/need material":
			if matEnd >= 0 {
				return false
			}
			matEnd = i
		default:
			if npcPricingMetadata.MatchString(f.Name) {
				return false
			}
		}
	}
	if limit < 0 {
		return limitEnd < 0
	}
	regex := npcCurrentCashLimit
	if fields[limit].Name == "daily purchase limit" {
		regex = npcClassicCashLimit
	}
	m := regex.FindStringSubmatch(fields[limit].Value)
	if m == nil {
		return false
	}
	count, err := unsigned(m[1], 31)
	if err != nil || count == 0 {
		return false
	}
	if limitEnd >= 0 && (fields[limit].Name != "purchase limit" || limitEnd != limit+1 || fields[limitEnd].Value != "") {
		return false
	}
	if mat < 0 || matEnd != mat+1 || fields[matEnd].Value != "" {
		return false
	}
	parts := strings.Fields(fields[mat].Value)
	if len(parts) < 2 || len(parts)%2 != 0 {
		return false
	}
	seen := map[uint64]bool{}
	for at := 0; at < len(parts); at += 2 {
		id, e := unsigned(parts[at], 31)
		n, ne := unsigned(parts[at+1], 31)
		if e != nil || ne != nil || n == 0 || id == 0 || seen[id] {
			return false
		}
		seen[id] = true
	}
	return true
}

func (s *cashResources) fatigueOffer(r cashRow) error {
	p, ok := s.restriction(r, 1, false)
	if !ok || p.count != 1 || !r.ceraQuote() || r.count != 1 || r.display != 1 || r.code != 0 || s.ceraOnly[r.item] == 0 || s.points[r.item] != 0 || s.bonused[r.sku] != 0 ||
		len(s.discounts[r.sku]) != 0 || s.coupons[r.sku] {
		return nil
	}
	src, err := s.items.Source(r.item)
	if err != nil || src.Equipment || !strings.HasPrefix(cashField(src, "action type"), "`[add fatigue]` ") {
		return nil
	}
	t, err := src.Gift()
	if err != nil || !t.PvfGiftItem || t.StackLimit != 1 || !t.DailyDeletion || t.ResourceExpiration != nil || t.RelativeLifetimeSeconds != 0 || t.WeeklyDeletionDay != nil {
		return nil
	}
	q := r.quote()
	q.CharacterRestriction, q.CharacterDailyLimit = p.code, p.count
	return s.add(q, t)
}

func (s *cashResources) periodicOffer(section string, r cashRow) error {
	period := byte(101)
	if section == "item event" {
		period = 102
	}
	p, ok := s.restriction(r, period, true)
	if !ok || r.item == 0 || r.item > math.MaxInt32 || r.count == 0 || r.count > math.MaxUint16 || r.gold != 0 || r.reserved != 0 ||
		r.display > 1 || r.code != 0 || len(s.discounts[r.sku]) != 0 || s.coupons[r.sku] {
		return nil
	}
	free := r.cera == 0 && period == 101
	if r.cera > math.MaxInt32 || free && (s.points[r.item] != 1 || s.ceraOnly[r.item] != 0) || !free && s.points[r.item] != 0 {
		return nil
	}
	disguise := r.sku == 105947 && r.item == 490709085 && r.count == 1 && r.cera == 22500 && r.display == 1 && p.code == 221 && p.count == 1
	if r.sku == 105947 && !disguise || s.bonused[r.sku] != 0 && !disguise {
		return nil
	}
	if (free || period == 102) && (s.nonstackable[r.sku] != 0 || s.nonstackable[r.item] != 0 || s.mileage[r.sku] != 0 || s.returning[r.sku] != 0) {
		return nil
	}
	src, err := s.items.Source(r.item)
	if err != nil || src.Equipment {
		return nil
	}
	fields, err := gm.ItemFields(src.Text)
	if err != nil {
		return nil
	}
	typ := cashField(src, "stackable type")
	if period == 102 && !cashAcquisition(fields) || period == 101 && typ != "`[booster]` 0" {
		return nil
	}
	if free && !slices.ContainsFunc(fields, func(f gm.Field) bool { return f.Name == "booster info" }) {
		return nil
	}
	t, err := src.Gift()
	if err != nil || t.Kind != 2 || !t.PvfGiftItem || t.CashPackage {
		return nil
	}
	q := r.quote()
	q.AccountRestriction, q.AccountLimit, q.AccountPeriod, q.NativeFreePointOnly = p.code, p.count, period, free
	if disguise {
		bonuses, err := readCashBonuses(s.bonusText, r.sku, 50)
		if err != nil {
			return nil
		}
		q.BonusItems = bonuses
		for _, b := range bonuses {
			t, err := s.items.Gift(b.TemplateID)
			if err != nil {
				return nil
			}
			s.templates[t.TemplateId] = t
		}
	}
	return s.add(q, t)
}

var cashBonusBlock = regexp.MustCompile("(?ims)^\\[bonus item\\][ \t]*\\n(.*?)^\\[/bonus item\\][ \t]*$")

func readCashBonuses(text string, sku, count uint32) ([]storage.CommercePayment, error) {
	block, err := commerceBlock(text, "one plus one bonus item info ipg")
	if err != nil {
		return nil, err
	}
	key := strconv.FormatUint(uint64(sku), 10)
	value := strconv.FormatUint(uint64(count), 10)
	var out []storage.CommercePayment
	for _, m := range cashBonusBlock.FindAllStringSubmatch(block, -1) {
		row := strings.Fields(m[1])
		if len(row) == 0 || row[0] != key {
			continue
		}
		if out != nil || !slices.Equal(row, []string{key, "490708133", value, "490708134", value}) {
			return nil, fmt.Errorf("cash disguise purchase must preserve both complete bonus grants")
		}
		out = []storage.CommercePayment{{TemplateID: 490708133, Count: count}, {TemplateID: 490708134, Count: count}}
	}
	if out == nil {
		return nil, fmt.Errorf("cash disguise bonus row is absent")
	}
	return out, nil
}
