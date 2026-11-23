package commerce

import (
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math"
	"regexp"
	"slices"
	"strings"

	"s9a1/server/internal/catalogprep/gm"
)

type BattlePassCashSource struct {
	ResourceHash                  string
	Certificate, Package, BuyCode uint32
}

var packageLine = regexp.MustCompile("\\s*([0-9]+)\\s+([0-9]+)\\s+([0-9]+)\\s+([0-9]+)\\s+([0-9]+)\\s+(\x60[^\x60]*\x60)\\s+([0-9]+)\\s+([0-9]+)\\s+(-?[0-9]+)\\s+(-?[0-9]+)\\s+([0-9]+)")

type packageQuote struct {
	cashRow
	model    uint32
	min, max string
	level    uint32
	label    string
}

func (s *cashResources) packages(category string) (map[uint32]packageQuote, error) {
	text, err := commerceBlock(s.shop, category)
	if err != nil {
		return nil, err
	}
	out := map[uint32]packageQuote{}
	at := 0
	for _, m := range packageLine.FindAllStringSubmatchIndex(text, -1) {
		if m[0] != at || len(out) >= 4096 {
			return nil, fmt.Errorf("%s package row grammar is invalid", category)
		}
		at = m[1]
		str := func(n int) string { return text[m[n*2]:m[n*2+1]] }
		var n [12]uint32
		for _, i := range []int{1, 2, 3, 4, 5, 7, 8, 11} {
			v, err := unsigned(str(i), 32)
			if err != nil {
				return nil, err
			}
			n[i] = uint32(v)
		}
		if n[1] == 0 || n[1] > math.MaxInt32 || out[n[1]].sku != 0 {
			return nil, fmt.Errorf("%s package commodity is invalid or duplicated", category)
		}
		out[n[1]] = packageQuote{cashRow{sku: n[1], item: n[2], count: 1, gold: n[3], reserved: n[4], cera: n[5], code: n[8]}, n[7], str(9), str(10), n[11], str(6)}
	}
	if strings.TrimSpace(text[at:]) != "" {
		return nil, fmt.Errorf("%s package row grammar is invalid", category)
	}
	return out, nil
}

func (p packageQuote) quoted(model uint32) bool {
	return p.ceraQuote() && p.model == model && p.code == 0 && p.min == "-1" && p.max == "-1" && p.level == 255
}

func (s *cashResources) addPackages(pass *BattlePassCashSource) error {
	rows, err := s.packages("package")
	if err != nil {
		return err
	}
	if pass != nil {
		if _, err := hex.DecodeString(pass.ResourceHash); err != nil || len(pass.ResourceHash) != 64 ||
			pass.Certificate == 0 || pass.Package == 0 || pass.BuyCode == 0 || pass.BuyCode == math.MaxUint32 {
			return fmt.Errorf("battle pass cash resource identity is invalid")
		}
		for n, id := range []uint32{pass.Certificate, pass.Package} {
			matched := []packageQuote{}
			code := pass.BuyCode
			if n == 0 {
				code++
			}
			for _, row := range rows {
				if row.item == id && row.model == 1 && row.label == "``" && row.code == code && row.ceraQuote() && row.min == "-1" && row.max == "-1" && row.level == 255 {
					matched = append(matched, row)
				}
			}
			if len(matched) != 1 {
				return fmt.Errorf("Battle Pass immediate package quote is absent or ambiguous")
			}
			src, err := s.items.Source(id)
			if err != nil || strings.Join(strings.Fields(cashField(src, "stackable type")), " ") != "`[contract]` 0" {
				return fmt.Errorf("Battle Pass source is not an immediate contract")
			}
			q := matched[0].quote()
			q.BattlePassHash, q.BattlePassDeluxe = pass.ResourceHash, n == 1
			if err := s.add(q, nil); err != nil {
				return err
			}
		}
	}
	for _, r := range rows {
		if !r.quoted(2) || s.points[r.item] != 0 || s.bonused[r.sku] != 0 || s.coupons[r.sku] || len(s.discounts[r.sku]) > 0 {
			continue
		}
		q := r.quote()
		if len(s.restrictions[r.sku]) != 0 {
			p, ok := s.restriction(r.cashRow, 101, true)
			if !ok || r.sku != 106036 || r.item != 490709749 || p.code != 310 || p.count != 1 {
				continue
			}
			q.AccountRestriction, q.AccountLimit = p.code, p.count
		}
		t, err := s.items.Gift(r.item)
		if err != nil || !t.CashPackage {
			continue
		}
		if err := s.add(q, t); err != nil {
			return err
		}
	}
	community, err := s.packages("community package")
	if err != nil {
		return err
	}
	for _, r := range community {
		if !r.quoted(0) || s.nonstackable[r.sku] != 0 || !s.ordinarySupplemental(r.cashRow, false) {
			continue
		}
		src, err := s.items.Source(r.item)
		if err != nil || src.Equipment {
			continue
		}
		fields, err := gm.ItemFields(src.Text)
		if err != nil || slices.ContainsFunc(fields, func(f gm.Field) bool { return npcPricingMetadata.MatchString(f.Name) }) {
			continue
		}
		typ := cashField(src, "stackable type")
		if typ != "`[material]` 0" && typ != "`[material]` 1" {
			continue
		}
		t, err := src.Gift()
		if err != nil || t.Kind != 2 || t.CashPackage {
			continue
		}
		if err := s.add(r.quote(), t); err != nil {
			return err
		}
	}
	return s.addCoupons(rows)
}

func (s *cashResources) addCoupons(packages map[uint32]packageQuote) error {
	block, err := commerceBlock(s.couponText, "coupon")
	if err != nil {
		return err
	}
	mapping, err := cashIntegers(block, 3)
	if err != nil {
		return err
	}
	for _, family := range []struct {
		group, original, destination uint32
		ids                          []uint32
	}{
		{79, 106034, 108749, []uint32{490709160, 490709555}}, {80, 106034, 108750, []uint32{490709587}}, {26, 102036, 101662, []uint32{2683473}},
	} {
		if _, exists := s.offers[family.destination]; exists {
			continue
		}
		original, exists := s.offers[family.original]
		if !exists {
			continue
		}
		matched := 0
		for at := 0; at < len(mapping); at += 3 {
			r := mapping[at : at+3]
			if r[2] != family.destination && (r[0] != family.group || r[1] != family.original) {
				continue
			}
			if !slices.Equal(r, []uint32{family.group, family.original, family.destination}) {
				matched = -1
				break
			}
			matched++
		}
		if matched != 1 {
			continue
		}
		var quote cashRow
		if family.group == 26 {
			rows, err := s.rows("item mod or ext")
			if err != nil {
				return err
			}
			idx := slices.IndexFunc(rows, func(r cashRow) bool { return r.sku == family.destination })
			if idx < 0 {
				continue
			}
			quote = rows[idx]
			if !quote.ceraQuote() || quote.item != 2660834 || quote.count != 5 || quote.display != 0 || quote.code != 0 ||
				uint64(quote.cera)*2 != uint64(original.Cera) {
				continue
			}
		} else {
			item := packages[family.destination]
			ref := packages[family.original]
			if !item.quoted(2) || !ref.quoted(2) || item.item != ref.item || item.cera >= ref.cera {
				continue
			}
			quote = item.cashRow
		}
		check := func(sku uint32) bool {
			return s.bonused[sku] == 0 && s.points[quote.item] == 0 && s.nonstackable[sku] == 0 && s.nonstackable[quote.item] == 0 &&
				s.mileage[sku] == 0 && s.returning[sku] == 0 && len(s.restrictions[sku]) == 0 && len(s.discounts[sku]) == 0
		}
		if !check(family.original) || !check(family.destination) {
			continue
		}
		t := s.templates[quote.item]
		if t == nil || (family.group != 26) != t.CashPackage {
			continue
		}
		q := quote.quote()
		valid := true
		for _, id := range family.ids {
			src, err := s.items.Source(id)
			if err != nil {
				valid = false
				break
			}
			c, err := couponTemplate(src, family.group)
			if err != nil {
				valid = false
				break
			}
			value, err := json.Marshal(c)
			if err != nil {
				return err
			}
			q.RequiredCouponTemplates[id] = value
			s.templates[id] = c
		}
		if valid {
			if err := s.add(q, t); err != nil {
				return err
			}
		}
	}
	return nil
}

func couponTemplate(src ItemSource, group uint32) (*ItemTemplate, error) {
	fields, err := gm.ItemFields(src.Text)
	if err != nil {
		return nil, err
	}
	value := map[string]string{}
	allowed := []string{"name", "explain", "flavor text", "grade", "rarity", "usable job", "/usable job", "attach type", "minimum level", "icon",
		"stackable type", "move wav", "expiration date", "stack limit"}
	if group == 26 {
		allowed = append(allowed, "impossible contents", "/impossible contents")
	}
	invalid := fmt.Errorf("coupon %d resource metadata is incomplete", src.Id)
	for _, f := range fields {
		if !slices.Contains(allowed, f.Name) {
			return nil, invalid
		}
		if _, exists := value[f.Name]; exists {
			return nil, invalid
		}
		value[f.Name] = f.Value
	}
	attach, rarity := "`[account]`", "2"
	if group == 80 {
		rarity = "3"
	}
	if group == 26 {
		attach, rarity = "`[trade]`", "1"
		if _, ok := value["/impossible contents"]; !ok || value["impossible contents"] != "`gift`" || value["expiration date"] != "" {
			return nil, invalid
		}
	} else if value["expiration date"] == "" {
		return nil, invalid
	}
	if value["stackable type"] != fmt.Sprintf("`[coupon]` %d 0", group) || value["attach type"] != attach || value["rarity"] != rarity ||
		value["usable job"] != "`[all]`" || value["minimum level"] != "1" || value["grade"] != "1" {
		return nil, invalid
	}
	t, err := src.Gift()
	if err != nil || t.Kind != 2 || !t.PvfGiftItem || t.CashPackage || (group == 26) != (t.ResourceExpiration == nil) ||
		t.RelativeLifetimeSeconds != 0 || t.DailyDeletion || t.WeeklyDeletionDay != nil {
		return nil, invalid
	}
	return t, nil
}

func (s *cashResources) extras(r cashRow) (bool, error) {
	if r.sku == 101307 {
		p, ok := s.restriction(r, 0, false)
		if !ok || p.code != 34 || p.count != 1 || r.item != 51 || r.count != 1 || r.cera != 3800 || !r.ceraQuote() ||
			r.display != 0 || r.code != 0 || s.points[r.item] != 0 || s.bonused[r.sku] != 0 || s.nonstackable[r.sku] != 0 || s.nonstackable[r.item] != 0 ||
			s.mileage[r.sku] != 0 || s.returning[r.sku] != 0 || s.coupons[r.sku] || len(s.discounts[r.sku]) != 0 {
			return true, nil
		}
		src, err := s.items.Source(r.item)
		if err != nil || !validOwl(src) {
			return true, nil
		}
		t, err := src.Gift()
		if err != nil || t == nil || t.Kind != 2 || !t.PvfGiftItem || t.CashPackage || t.FirstBagSlot != 65 || t.LastBagSlot != 120 || t.StackLimit != math.MaxUint32 ||
			t.ResourceExpiration != nil || t.RelativeLifetimeSeconds != 0 || t.DailyDeletion || t.WeeklyDeletionDay != nil {
			return true, nil
		}
		q := r.quote()
		q.CharacterRestriction, q.CharacterDailyLimit, q.CharacterPeriod = 34, 1, 0
		q.OwnedItemExclusions = []uint32{51, 2660234}
		return true, s.add(q, t)
	}
	if r.sku >= 105872 && r.sku <= 105875 {
		count := []uint32{1, 10, 50, 100}[r.sku-105872]
		if r.item != 490708126 || r.count != count || r.cera != count*500 || !r.ceraQuote() || r.display != 104 || r.code != 0 ||
			s.points[r.item] != 0 || len(s.restrictions[r.sku]) != 0 || len(s.discounts[r.sku]) != 0 {
			return true, fmt.Errorf("Disguise product has unsupported currency quantity price or auxiliary fields")
		}
		bonuses, err := readCashBonuses(s.bonusText, r.sku, count)
		if err != nil {
			return true, err
		}
		for _, gift := range bonuses {
			t, err := s.items.Gift(gift.TemplateID)
			if err != nil {
				return true, err
			}
			s.templates[t.TemplateId] = t
		}
		source, err := s.items.Gift(r.item)
		if err != nil {
			return true, err
		}
		q := r.quote()
		q.BonusItems = bonuses
		return true, s.add(q, source)
	}
	if r.cera == 0 && r.gold > 0 && r.gold <= math.MaxInt32 && r.item > 0 && r.item <= math.MaxInt32 && r.count > 0 && r.count <= math.MaxUint16 &&
		r.reserved == 0 && r.display == 104 && r.code == 0 && s.ordinarySupplemental(r, false) && s.ceraOnly[r.item] == 0 &&
		s.nonstackable[r.sku] == 0 && s.nonstackable[r.item] == 0 && s.mileage[r.sku] == 0 && s.returning[r.sku] == 0 && s.couponNumbers[r.sku] == 0 {
		src, err := s.items.Source(r.item)
		if err != nil || src.Equipment {
			return true, nil
		}
		fields, err := gm.ItemFields(src.Text)
		if err != nil {
			return true, nil
		}
		allowed := []string{"name", "name2", "explain", "flavor text", "grade", "rarity", "usable job", "/usable job", "attach type", "minimum level",
			"icon", "stackable type", "move wav", "stack limit", "price", "value", "weight", "item group name", "need material", "/need material",
			"impossible contents", "/impossible contents", "expiration date", "usable period"}
		if !cashAcquisition(fields) || slices.ContainsFunc(fields, func(f gm.Field) bool { return !slices.Contains(allowed, f.Name) }) {
			return true, nil
		}
		typ := cashField(src, "stackable type")
		if typ != "`[material]` 0" && typ != "`[material]` 1" {
			return true, nil
		}
		t, err := src.Gift()
		if err != nil || t.Kind != 2 || t.CashPackage {
			return true, nil
		}
		return true, s.add(r.quote(), t)
	}
	return false, nil
}

func validOwl(src ItemSource) bool {
	fields, err := gm.ItemFields(src.Text)
	names := []string{"name", "explain", "grade", "rarity", "usable job", "/usable job", "attach type", "minimum level", "icon", "field image",
		"stackable type", "cool time", "cooltime group", "move wav", "item category", "/item category", "impossible contents", "/impossible contents"}
	if err != nil || len(fields) != len(names) {
		return false
	}
	m := map[string]string{}
	for i, f := range fields {
		if !slices.Contains(names, f.Name) {
			return false
		}
		if _, dup := m[f.Name]; dup {
			return false
		}
		m[f.Name] = f.Value
		if f.Name == "usable job" || f.Name == "item category" || f.Name == "impossible contents" {
			if i+1 >= len(fields) || fields[i+1].Name != "/"+f.Name || fields[i+1].Value != "" {
				return false
			}
		}
	}
	return m["stackable type"] == "`[unlimited waste]` 0" && m["attach type"] == "`[trade]`" && m["minimum level"] == "1" &&
		m["usable job"] == "`[all]`" && m["grade"] == "1" && m["rarity"] == "2" && m["cool time"] == "3000" && m["cooltime group"] == "70" &&
		m["item category"] == "" && m["impossible contents"] == "`gift`"
}
