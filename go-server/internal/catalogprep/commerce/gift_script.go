package commerce

import (
	"encoding/json"
	"fmt"
	"math"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"s9a1/server/internal/catalogprep/gm"
	"s9a1/server/internal/storage"
)

const giftFixedType = "`[usable cera package]` 0"

func giftSelectionType(typ string) bool {
	return typ == "`[booster selection]` 0" || typ == "`[booster selection]` 6"
}
func giftRandomType(typ string) bool {
	return typ == "`[booster]` 0" || typ == "`[booster]` 1" || typ == "`[booster random]` 0"
}

func giftUnique(fields []gm.Field, name string) (string, int, error) {
	at := -1
	for i, f := range fields {
		if f.Name != name {
			continue
		}
		if at >= 0 {
			return "", -1, fmt.Errorf("ambiguous gift source rule: %s", name)
		}
		at = i
	}
	if at == -1 {
		return "", -1, nil
	}
	return fields[at].Value, at, nil
}

func giftInt(v string) (uint32, error) {
	if v == "" || strings.ContainsAny(v, "+-") {
		return 0, fmt.Errorf("Gift integer list grammar is unsupported")
	}
	n, err := strconv.ParseUint(v, 10, 32)
	if err != nil {
		return 0, fmt.Errorf("Gift integer is outside the supported range")
	}
	return uint32(n), nil
}

func giftInts(value string, empty bool) ([]uint32, error) {
	p := strings.Fields(value)
	if len(p) == 0 && !empty {
		return nil, fmt.Errorf("Gift integer list is empty")
	}
	r := make([]uint32, len(p))
	for i, v := range p {
		for _, d := range v {
			if d < '0' || d > '9' {
				return nil, fmt.Errorf("Gift integer list grammar is unsupported")
			}
		}
		n, err := giftInt(v)
		if err != nil {
			return nil, err
		}
		r[i] = n
	}
	return r, nil
}

func parseGiftBox(template *ItemTemplate, text, typ string, resolve giftResolve, special giftSpecialResolve) (storage.GiftDefinition, error) {
	d := storage.GiftDefinition{SelectionCount: -1, RequestOpcode: 510, Categories: []storage.GiftCategory{}, RandomPools: []storage.GiftPool{}, Materials: []storage.CommercePayment{}, MaximumLevel: 255}
	var err error
	d.Template, err = json.Marshal(template)
	if err != nil {
		return d, err
	}
	fields, err := gm.ItemFields(text)
	if err != nil {
		return d, err
	}
	if giftRandomType(typ) {
		beginValue, begin, err := giftUnique(fields, "booster info")
		if err != nil {
			return d, err
		}
		endValue, end, err := giftUnique(fields, "/booster info")
		if err != nil || begin < 0 || end <= begin || beginValue != "" || endValue != "" {
			return d, fmt.Errorf("Random gift complete booster-info block is absent")
		}
		top := append(slices.Clone(fields[:begin]), fields[end+1:]...)
		if err := parseGiftSource(&d, top, typ, template.MinimumLevel, resolve); err != nil {
			return d, err
		}
		d.RandomUse, d.RequestOpcode = true, 160
		d.RandomPools, err = parseGiftPools(fields[begin+1:end], resolve, special)
		return d, err
	}

	top := make([]gm.Field, 0, len(fields))
	seen := map[[2]uint32]bool{}
	totalRewards := 0
	for i := 0; i < len(fields); i++ {
		f := fields[i]
		if f.Name != "booster select category" {
			top = append(top, f)
			continue
		}
		end := i + 1
		for end < len(fields) && fields[end].Name != "/booster select category" {
			end++
		}
		if end == len(fields) || len(d.Categories) >= 4096 {
			return d, fmt.Errorf("Gift selection category is not closed or exceeds its budget")
		}
		coord, err := giftInts(f.Value, false)
		if err != nil || len(coord) != 2 || coord[0] > 255 || coord[1] > 255 || seen[[2]uint32(coord)] {
			return d, fmt.Errorf("Gift selection category coordinates are unsupported or duplicated")
		}
		seen[[2]uint32(coord)] = true
		c, err := parseGiftCategory(fields[i+1:end], byte(coord[0]), byte(coord[1]), &totalRewards, resolve, special)
		if err != nil {
			return d, err
		}
		d.Categories = append(d.Categories, c)
		i = end
	}
	if err := parseGiftSource(&d, top, typ, template.MinimumLevel, resolve); err != nil {
		return d, err
	}
	if typ == giftFixedType {
		value, at, err := giftUnique(top, "package data")
		if err != nil || len(d.Categories) != 0 || at < 0 || at+1 >= len(top) || top[at+1].Name != "/package data" {
			return d, fmt.Errorf("Fixed gift package data is not closed")
		}
		rows, err := giftInts(value, false)
		if err != nil || len(rows)%2 != 0 || len(rows)/2 > 32768 {
			return d, fmt.Errorf("Fixed gift reward tuple width or count is invalid")
		}
		c := storage.GiftCategory{RequiredItems: []uint32{}, Rewards: []storage.GiftReward{}}
		for i := 0; i < len(rows); i += 2 {
			if rows[i] == math.MaxUint32 || rows[i+1] == 0 {
				return d, fmt.Errorf("Fixed gift reward identity or quantity is invalid")
			}
			c.Rewards = append(c.Rewards, resolve(rows[i], rows[i+1], ""))
		}
		d.Categories = append(d.Categories, c)
		return d, nil
	}
	if len(d.Categories) == 0 {
		return d, fmt.Errorf("Gift selection categories are absent")
	}
	countText, _, err := giftUnique(top, "booster selection num")
	count, errCount := giftInts(countText, false)
	if err != nil || errCount != nil || len(count) != 1 || count[0] > 255 {
		return d, fmt.Errorf("Gift selection count is unsupported")
	}
	d.SelectionCount, d.RequestOpcode = int(count[0]), 160
	shapeText, _, err := giftUnique(top, "booster category num")
	if err != nil || shapeText == "" {
		return d, err
	}
	shape, err := giftInts(shapeText, false)
	if err != nil || shape[0] > 2 || len(shape) != int(shape[0])+1 {
		return d, fmt.Errorf("Gift category dimension grammar is unsupported")
	}
	a, b := uint32(1), uint32(1)
	if len(shape) > 1 {
		a = max(1, shape[1])
	}
	if len(shape) > 2 {
		b = max(1, shape[2])
	}
	for v := range seen {
		if a > 256 || b > 256 || v[0] >= a || v[1] >= b {
			return d, fmt.Errorf("Gift category coordinates exceed their declared dimensions")
		}
	}
	return d, nil
}

func parseGiftCategory(fields []gm.Field, index, subindex byte, total *int, resolve giftResolve, special giftSpecialResolve) (storage.GiftCategory, error) {
	c := storage.GiftCategory{Index: index, SubIndex: subindex, RequiredItems: []uint32{}, Rewards: []storage.GiftReward{}}
	reject := func(reason string) {
		if c.UnavailableReason == nil {
			c.UnavailableReason = &reason
		}
	}
	upgrades := map[string]string{}
	for i, f := range fields {
		if strings.HasPrefix(f.Name, "/") {
			if f.Value != "" {
				reject("Gift category has content after a closing field")
			}
			continue
		}
		switch f.Name {
		case "default select":
			n, err := giftInts(f.Value, false)
			if err != nil {
				reject(err.Error())
			} else if n[0] != uint32(len(n)-1) || !distinctGiftIDs(n[1:], true) {
				reject("Gift default-selection identities are invalid")
			} else {
				c.RequiredItems = append(c.RequiredItems, n[1:]...)
			}
			continue
		case "booster equipment upgrade", "random probability", "broadcast upgrade":
			if _, seen := upgrades[f.Name]; seen {
				reject("Repeated gift equipment upgrade field")
			} else {
				upgrades[f.Name] = f.Value
			}
			if f.Name == "random probability" && (i+1 >= len(fields) || fields[i+1].Name != "/random probability") {
				reject("Gift upgrade probability table is not closed")
			}
			continue
		case "etc", "equipment", "stackable", "creature", "avatar":
		default:
			reject("Gift category field is unsupported: " + f.Name)
			continue
		}
		if i+1 >= len(fields) || fields[i+1].Name != "/"+f.Name {
			reject("Gift category reward field is not closed: " + f.Name)
			continue
		}
		values, err := giftInts(f.Value, true)
		if err != nil {
			reject(err.Error())
			continue
		}
		width := 2
		if f.Name == "avatar" {
			width = 4
		}
		if len(values)%width != 0 {
			reject("Gift category reward tuple width is unsupported")
			continue
		}
		for j := 0; j < len(values); j += width {
			id, count := values[j], values[j+1]
			if id == math.MaxUint32 || id == 0 && width == 4 || count == 0 {
				reject("Gift category reward identity or quantity is invalid")
				continue
			}
			*total++
			if *total > 32768 {
				return c, fmt.Errorf("Gift has too many rewards")
			}
			if width == 4 {
				c.Rewards = append(c.Rewards, special(id, count, values[j+2], int64(values[j+3])))
			} else {
				c.Rewards = append(c.Rewards, resolve(id, count, ""))
			}
		}
	}
	if len(upgrades) > 0 {
		if err := applyGiftUpgrades(c.Rewards, upgrades); err != nil {
			reject("Gift equipment upgrade is invalid: " + err.Error())
		}
	}
	if len(c.Rewards) == 0 {
		reject("Gift selection category has no supported reward syntax")
	}
	seen := map[uint32]bool{}
	for _, r := range c.Rewards {
		if seen[r.TemplateID] {
			reject("Gift category repeats a reward identity that the native selection cannot distinguish")
		}
		seen[r.TemplateID] = true
	}
	for _, id := range c.RequiredItems {
		if !seen[id] {
			reject("Gift default selection is absent from its reward category")
		}
	}
	return c, nil
}

func distinctGiftIDs(values []uint32, allowZero bool) bool {
	seen := map[uint32]bool{}
	for _, v := range values {
		if seen[v] || v == math.MaxUint32 || !allowZero && v == 0 {
			return false
		}
		seen[v] = true
	}
	return true
}

func applyGiftUpgrades(rs []storage.GiftReward, fields map[string]string) error {
	selector, exists := fields["booster equipment upgrade"]
	if !exists {
		return fmt.Errorf("Missing upgrade selector")
	}
	n, err := giftInts(selector, false)
	if err != nil || len(n) != 1 {
		return fmt.Errorf("Invalid upgrade selector")
	}
	rows := []storage.GiftUpgrade{}
	table, exists := fields["random probability"]
	if n[0] != 255 {
		if n[0] > 31 || exists {
			return fmt.Errorf("Invalid fixed upgrade")
		}
		rows = append(rows, storage.GiftUpgrade{Level: byte(n[0]), Weight: 1})
	} else {
		if !exists {
			return fmt.Errorf("Missing random upgrade weights")
		}
		v, err := giftInts(table, false)
		if err != nil || len(v)%2 != 0 || len(v) > 64 {
			return fmt.Errorf("Invalid upgrade probability shape")
		}
		positive, seen := false, map[uint32]bool{}
		for i := 0; i < len(v); i += 2 {
			if v[i] > 31 || seen[v[i]] {
				return fmt.Errorf("Invalid or repeated upgrade level")
			}
			positive, seen[v[i]] = positive || v[i+1] > 0, true
			rows = append(rows, storage.GiftUpgrade{Level: byte(v[i]), Weight: v[i+1]})
		}
		if !positive {
			return fmt.Errorf("Upgrade probability has no positive weight")
		}
	}
	var broadcast *byte
	if v, ok := fields["broadcast upgrade"]; ok {
		n, err := giftInts(v, false)
		if err != nil || len(n) != 1 || n[0] > 31 {
			return fmt.Errorf("Gift upgrade broadcast threshold is outside the native range")
		}
		b := byte(n[0])
		broadcast = &b
	}
	for i, r := range rs {
		var t ItemTemplate
		if json.Unmarshal(r.Template, &t) != nil || !t.PvfGiftItem || t.Kind == 2 {
			return fmt.Errorf("Gift upgrade requires a complete ordinary equipment reward")
		}
		rs[i].Upgrades, rs[i].BroadcastUpgrade = rows, broadcast
	}
	return nil
}

var giftSourceFields = strings.FieldsFunc(`name|name2|explain|flavor text|grade|rarity|usable job|attach type|minimum level|maximum level|icon|icon mark|field image|stackable type|item group name|move wav|weight|price|value|stack limit|expiration date|booster category name|default select auto job and growType|default select auto growType|hide booster info|item category|impossible contents|npc gift disallowance|color tag explain|confirm to use|open confirm window on use|additional effect ani|item drop effect|item info to show together|recommend|recommand mark booster|suitable job|action usable place|need material|usable datetime|usable period|purchase limit|daily purchase limit|reset daily purchase item|reset monthly purchase item|restrict buy level cashshop|npc buy on special weekdays|sell restriction servers|bidding start price|avatar package preview info|avatar booster preview|trade limit|trade limit max|daily delete item|weekly delete item|fatiguetime monthly delete|set disjoint instance type|set ban transport stone|cool time|cooltime group|cooltime maintenance|total usable count|total weekly usable count|total weekly usable count reset day|total accumulated usable count|total accumulated usable count reward`, func(r rune) bool { return r == '|' })
var giftTime = regexp.MustCompile("^`[0-9]{4}-[0-9]{2}-[0-9]{2} [0-9]{2}:[0-9]{2}:[0-9]{2}`$")

func parseGiftSource(d *storage.GiftDefinition, fields []gm.Field, typ string, minLevel byte, resolve giftResolve) error {
	for _, f := range fields {
		if strings.HasPrefix(f.Name, "/") {
			if f.Value != "" {
				return fmt.Errorf("Gift source has content after a closing field")
			}
			continue
		}
		if slices.Contains(giftSourceFields, f.Name) || f.Name == "package data" && typ == giftFixedType || giftSelectionType(typ) &&
			(f.Name == "booster category num" || f.Name == "booster selection num" || f.Name == "set epic exchange instance type") {
			continue
		}
		return fmt.Errorf("Gift use requires an unsupported source rule: %s", f.Name)
	}
	for k, flag := range map[string]*bool{"set disjoint instance type": &d.SetDisjointInstanceType, "set ban transport stone": &d.SetBanTransportStone, "set epic exchange instance type": &d.SetEpicExchangeInstanceType} {
		v, at, err := giftUnique(fields, k)
		if err != nil || v != "" {
			return fmt.Errorf("Gift instance restriction must be one empty flag: %s", k)
		}
		*flag = at >= 0
	}
	place, _, err := giftUnique(fields, "action usable place")
	if err != nil {
		return err
	}
	if place != "" {
		foundVillage := false
		for _, p := range quotedPlace.FindAllStringSubmatch(place, -1) {
			if p[1] != "[village]" && p[1] != "[seria room]" && p[1] != "[dont use assult]" {
				return fmt.Errorf("Gift usable-place restriction is unsupported")
			}
			foundVillage = foundVillage || p[1] == "[village]"
		}
		if !foundVillage || strings.TrimSpace(quotedPlace.ReplaceAllString(place, "")) != "" {
			return fmt.Errorf("Gift usable-place restriction is unsupported")
		}
	}
	for _, key := range []string{"value", "maximum level"} {
		v, at, err := giftUnique(fields, key)
		if err != nil {
			return err
		}
		if at == -1 {
			continue
		}
		n, err := giftInt(v)
		if err != nil || key == "maximum level" && (n > 255 || n < uint32(minLevel)) {
			return fmt.Errorf("Gift source %s is invalid", key)
		}
		if key == "maximum level" {
			d.MaximumLevel = byte(n)
		}
	}
	if v, at, err := giftUnique(fields, "need material"); err != nil {
		return err
	} else if at >= 0 {
		n, err := giftInts(v, false)
		if err != nil || len(n)%2 != 0 || len(n)/2 > 255 || at+1 >= len(fields) || fields[at+1].Name != "/need material" {
			return fmt.Errorf("Gift material debit tuple width or count is invalid")
		}
		ids := make([]uint32, 0, len(n)/2)
		for i := 0; i < len(n); i += 2 {
			if n[i+1] == 0 {
				return fmt.Errorf("Gift material debit identity or quantity is invalid")
			}
			ids = append(ids, n[i])
		}
		if !distinctGiftIDs(ids, false) {
			return fmt.Errorf("Gift material debit list repeats or has an invalid identity")
		}
	}
	if v, _, err := giftUnique(fields, "usable datetime"); err != nil {
		return err
	} else if v != "" {
		t, err := time.ParseInLocation("2006-01-02 15:04:05", strings.Trim(v, "`"), time.FixedZone("UTC+8", 8*3600))
		if err != nil || !giftTime.MatchString(v) || t.Unix() <= 0 || t.Unix() > math.MaxInt32 {
			return fmt.Errorf("Gift usable-from absolute time is malformed")
		}
		sec := t.Unix()
		d.UsableFrom = &sec
	}
	d.Usage, err = parseGiftUsage(fields, resolve)
	return err
}
