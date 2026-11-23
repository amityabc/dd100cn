package commerce

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"regexp"
	"slices"
	"strconv"
	"strings"
)

const premiumSeconds uint32 = 7 * 86400

type premiumSource struct {
	id, item, price uint32
	typ             byte
	hash            string
}
type characterServiceSource struct {
	premiumSource
	slot            uint32
	tag, parameters string
}

var accountPremiums = []premiumSource{
	{105770, 490706833, 2000000, 22, "521E033C0762AA35A47977526DCF4C9AD3B113D138E42AB4F273543D98AE4EA5"},
	{105771, 490706834, 4000000, 27, "E2D83C4E3C2B5A16F33205074F8B3DB9B5A23F7630407F59889BCE4A8DA0C071"},
	{103061, 10000389, 1500000, 97, "EB9766C0B0AFFFC814BCD2AC94FEE56E3B2C7DC3F09859F764C6867356437DDF"},
}

var characterServices = []characterServiceSource{
	{premiumSource{100539, 2681931, 200, 101, ""}, 1, "lottery_onemore", ""},
	{premiumSource{100540, 2681932, 160, 102, ""}, 2, "quest_helper", "364 100 70 30 2681922 2681923 2681924 2681925"},
	{premiumSource{100541, 2681933, 200, 103, "398B331838EC18FD4E85A9412EA316C2EA1782CACD500D4B7C09AFF4DDAF066D"}, 3, "buff_service", "364 10 5 5 0 5 5 0 5 5 0 5 5 0 1 5 0 1 5 0 1 5 0"},
	{premiumSource{100537, 2681929, 160, 104, "1B416763E39EB77386A9ADB798C33A4482CB829FD0C22240DED7EA0246F6CF60"}, 4, "recovery_hp_mp", "364 1"},
	{premiumSource{100538, 2681930, 160, 105, ""}, 5, "free_stamina", "364 10"},
	{premiumSource{100817, 2682205, 200, 106, "6786AD5DFE4D7FB5CA8062BC7E64F6233615443D5E793654AA1199B8D05C28B8"}, 6, "auto_repair", "364 6"},
	{premiumSource{100882, 2682206, 160, 107, "7C4130D4C946A3D83E43AB8AAC8A74A6D66B6B28CC3C69B29122999184F5D17E"}, 7, "lottery_fastbreak", "364 60"},
}
var premiumHeader = regexp.MustCompile("(?s)\\[type\\]\\s*([0-9]+)\\s*")
var termItem = regexp.MustCompile("(?s)\\[item\\]\\s*([0-9]+)\\s*\\[term\\]\\s*7\\s*\\[/term\\]\\s*\\[/item\\]")

func premiumBlock(text string, typ byte) (string, error) {
	id := strconv.Itoa(int(typ))
	var found string
	for _, at := range premiumHeader.FindAllStringSubmatchIndex(text, -1) {
		if text[at[2]:at[3]] != id {
			continue
		}
		rest := text[at[1]:]
		if next := premiumHeader.FindStringIndex(rest); next != nil {
			rest = rest[:next[0]]
		}
		if found != "" {
			return "", fmt.Errorf("premium type %d is duplicated", typ)
		}
		found = rest
	}
	if found == "" {
		return "", fmt.Errorf("premium type %d is absent", typ)
	}
	return found, nil
}

func (s *cashResources) premiumSupplemental(id, item uint32, character bool) bool {
	if s.restrictionNumbers[id] != 0 || s.discountNumbers[id] != 0 || s.returning[id] != 0 ||
		s.points[item] != 0 || s.ceraOnly[item] != 0 || s.nonstackable[item] != 0 || s.nonstackable[id] != 0 ||
		s.bonused[id] != 0 || s.couponNumbers[id] != 0 {
		return false
	}
	return character || s.mileage[id] == 0
}

func (s *cashResources) addPremiums() error {
	rows, err := s.rows("premium")
	if err != nil {
		return err
	}
	premiumText, err := s.items.source.ReadText("etc/premiumlist_new.etc", scriptLimit)
	if err != nil {
		return err
	}
	for _, p := range accountPremiums {
		index := slices.IndexFunc(rows, func(r cashRow) bool {
			return r.sku == p.id && r.item == p.item && r.count == 1 && r.gold == p.price && r.reserved == 0 && r.cera == 0 && r.display == 0 && r.code == 0
		})
		if index < 0 {
			continue
		}
		if !s.premiumSupplemental(p.id, p.item, false) {
			return fmt.Errorf("premium SKU %d has unimplemented supplemental purchase policy", p.id)
		}
		src, err := s.items.Source(p.item)
		if err != nil || src.Hash != p.hash {
			return fmt.Errorf("premium SKU %d differs from the pinned PVF item", p.id)
		}
		sub, err := premiumBlock(premiumText, p.typ)
		if err != nil {
			return err
		}
		found := false
		for _, m := range termItem.FindAllStringSubmatch(sub, -1) {
			if m[1] == strconv.FormatUint(uint64(p.item), 10) {
				found = true
			}
		}
		if !found {
			return fmt.Errorf("premium entitlement %d is missing its seven-day source", p.id)
		}
		switch p.typ {
		case 22:
			slots, err := commerceBlock(premiumText, "overequip list")
			const expected = "2 `[weapon]` `[support weapon]` 6 `[coat]` `[shoulder]` `[pants]` `[shoes]` `[waist]` `[title name]` 3 `[amulet]` `[wrist]` `[ring]` 9 `[hat avatar]` `[hair avatar]` `[face avatar]` `[coat avatar]` `[pants avatar]` `[shoes avatar]` `[breast avatar]` `[waist avatar]` `[skin avatar]` 3 `[support]` `[magic stone]` `[earring]`"
			values, vErr := commerceSection(sub, "over equipinfo")
			if err != nil || vErr != nil || strings.Join(strings.Fields(slots), " ") != expected || values != "5 1 10 2 10 3 10 4 10 5 10" {
				return fmt.Errorf("Conqueror equipment categories differ from the verified wearable slots")
			}
		case 27:
			level, err := commerceSection(sub, "over skill")
			if err != nil || level != "5" {
				return fmt.Errorf("SkillMaster premium effect differs from the verified levels")
			}
		case 97:
			if err := s.crystalEffects(); err != nil {
				return err
			}
		}
		q := rows[index].quote()
		q.PremiumType, q.PremiumSeconds = p.typ, premiumSeconds
		if err := s.add(q, nil); err != nil {
			return err
		}
	}
	return s.addCharacterServices()
}

func (s *cashResources) crystalEffects() error {
	raw, err := s.items.source.ReadText("etc/premiumserviceeffect.etc", scriptLimit)
	if err != nil {
		return err
	}
	block, err := premiumBlock(raw, 97)
	if err != nil {
		return err
	}
	body, err := commerceBlock(block, "add selectAble equipment list")
	if err != nil || strings.Join(numbers(body), " ") != "3033 100300084 3034 100300087 3035 100300086 3036 100300085 3037 100300088 3262 100300089" {
		return fmt.Errorf("crystal contract selectable effect mapping differs from the verified client resource")
	}
	native := map[uint32]string{
		100300084: "5BC00386C533B2B98F13AFD3CF366DDBB27B61E98DE2BB6EE7B1DBB94AFF3815",
		100300087: "5EE585EB7FB1F24AC76339634A7764EDCDF421FD6579E19C5904F3545D653F15",
		100300086: "F0C294F8F7F1D90557554B5D3ADE0C17DA700354AB6F6310A854740E1B503FDF",
		100300085: "A2913C0A6D698771CECEB47984B6468BF05F07DEA6A6ACD665BA07EA89878B20",
		100300088: "CC2337880B4E8C96362A8BBEA36EF1BDB583AB49687615F6795934A53C726459",
		100300089: "BDAEC34B0735AAFFCC886AA35B442C657F909324A3FAE2F6797BF6EE5DC0C9A2",
	}
	for id, hash := range native {
		r, err := s.items.Source(id)
		if err != nil || !r.Equipment || r.Hash != hash {
			return fmt.Errorf("crystal contract effect source %d changed", id)
		}
	}
	return nil
}

func (s *cashResources) addCharacterServices() error {
	part, err := commerceBlock(s.shop, "selectable character premium")
	if err != nil {
		return err
	}
	params, err := s.items.source.ReadText("etc/chn_characpremium.etc", scriptLimit)
	if err != nil {
		return err
	}
	mileage, err := commerceBlock(s.shop, "specific product mileage")
	if err != nil {
		return err
	}
	fees, err := cashIntegers(mileage, 2)
	if err != nil {
		return err
	}
	for _, p := range characterServices {
		prefix := fmt.Sprintf("(?:^|[^0-9])%d\\s+%d\\s+%d\\s+7\\s+0\\s+%d\\s+\x60[^\x60]*\x60\\s+0\\s+-1(?:\\s|$)", p.id, p.item, p.slot, p.price)
		found := regexp.MustCompile(prefix).FindAllString(part, -1)
		if len(found) == 0 {
			continue
		}
		if len(found) != 1 || !s.premiumSupplemental(p.id, p.item, true) {
			return fmt.Errorf("character service %d has invalid source quote or supplemental policy", p.id)
		}
		r, err := s.items.Source(p.item)
		if err != nil || p.hash != "" && r.Hash != p.hash {
			return fmt.Errorf("character service %d differs from its pinned source", p.id)
		}
		raw, err := commerceBlock(params, p.tag)
		if err != nil {
			return err
		}
		parsed := strings.Join(numbers(raw), " ")
		if p.slot == 1 {
			d := sha256.Sum256([]byte(parsed))
			if strings.ToUpper(hex.EncodeToString(d[:])) != "A005C76F1B8CCA39036D68CE7E7CC0FA014F059898721A90DB7376744F489525" {
				return fmt.Errorf("character service %d native parameters differ", p.id)
			}
		} else if parsed != p.parameters {
			return fmt.Errorf("character service %d native parameters differ", p.id)
		}
		var charged []uint32
		for n := 0; n < len(fees); n += 2 {
			if fees[n] == p.id {
				charged = append(charged, fees[n+1])
			}
		}
		if !slices.Equal(charged, []uint32{2}) {
			return fmt.Errorf("character service %d mileage source changed", p.id)
		}
		q := cashRow{sku: p.id, item: p.item, count: 1, cera: p.price}.quote()
		q.PremiumType, q.PremiumSeconds = p.typ, premiumSeconds
		if err := s.add(q, nil); err != nil {
			return err
		}
	}
	return nil
}
