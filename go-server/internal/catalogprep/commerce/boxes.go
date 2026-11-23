package commerce

import (
	"encoding/json"
	"fmt"
	"math"
	"slices"
	"strings"

	"s9a1/server/internal/catalogprep/gm"
	"s9a1/server/internal/storage"
)

type LotteryPrize struct {
	Weight     uint64
	TemplateId uint32
}

type LotteryRule struct {
	Source       *ItemTemplate
	GoldCost     uint32
	ForbidScene8 bool
	Materials    []storage.CommercePayment
	Prizes       []LotteryPrize
}

type Boxes struct {
	Lottery          map[uint32]LotteryRule
	LotteryTemplates map[uint32]*ItemTemplate
	KeyedBoxes       map[uint32]storage.KeyedBoxRule
	LuckyBoxes       map[uint32]storage.LuckyBoxRule
	KeyedTemplates   map[uint32]*ItemTemplate
	LuckyTemplates   map[uint32]*ItemTemplate
}

func BuildBoxes(items *Items, roots, lotteryRoots []uint32) (*Boxes, error) {
	return buildBoxes(items, roots, lotteryRoots, true)
}

func buildBoxes(items *Items, roots, lotteryRoots []uint32, includePinned bool) (*Boxes, error) {
	b := &Boxes{Lottery: map[uint32]LotteryRule{}, LotteryTemplates: map[uint32]*ItemTemplate{},
		KeyedBoxes: map[uint32]storage.KeyedBoxRule{}, LuckyBoxes: map[uint32]storage.LuckyBoxRule{},
		KeyedTemplates: map[uint32]*ItemTemplate{}, LuckyTemplates: map[uint32]*ItemTemplate{}}
	ids := map[uint32]byte{}
	if includePinned {
		ids[490708133], ids[490708135] = 1, 1
	}
	for _, id := range roots {
		ids[id] |= 3
	}
	for _, id := range lotteryRoots {
		ids[id] |= 2
	}
	if len(ids) > 32768 {
		return nil, fmt.Errorf("lottery source preparation exceeds its bounded root snapshot")
	}
	for _, id := range sortedIDs(ids) {
		if items.stackable[int64(id)] == "" || items.equipment[int64(id)] != "" {
			continue
		}
		r, err := items.Source(id)
		if err != nil {
			return nil, err
		}
		switch cashField(r, "stackable type") {
		case "`[upgradable legacy]` 1":
			if ids[id]&2 == 0 {
				continue
			}
			t, err := r.Gift()
			if err != nil {
				continue
			}
			b.LotteryTemplates[id] = t
			if rule, err := parseLottery(items, r, t, b.LotteryTemplates); err == nil {
				b.Lottery[id] = rule
			}
		case "`[random upgradable legacy]` 1":
			if ids[id]&1 != 0 {
				if rule, err := parseKeyedBox(items, r, b.KeyedTemplates); err == nil {
					b.KeyedBoxes[id] = rule
				}
			}
		case "`[lucky box]` 1":
			if ids[id]&1 != 0 {
				if rule, err := readLuckyBox(items, r, b.LuckyTemplates); err == nil {
					b.LuckyBoxes[id] = rule
				}
			}
		}
	}
	return b, nil
}

var lotteryFields = strings.Split("name|name2|explain|flavor text|grade|attach type|rarity|weight|value|usable job|minimum level|icon|icon mark|field image|stackable type|price|move wav|int data|stack limit|expiration date|usable period|need material|lottery use cost|lottery use need item|fix upgrade|broadcast type|daily delete item|weekly delete item|action usable place|item category", "|")

func parseLottery(items *Items, src ItemSource, t *ItemTemplate, templates map[uint32]*ItemTemplate) (LotteryRule, error) {
	d := LotteryRule{Source: t, Materials: []storage.CommercePayment{}, Prizes: []LotteryPrize{}}
	fields, err := gm.ItemFields(src.Text)
	if err != nil {
		return d, err
	}
	s := consumableScript{fields}
	for _, f := range fields {
		if !strings.HasPrefix(f.Name, "/") && !slices.Contains(lotteryFields, f.Name) {
			return d, fmt.Errorf("lottery source requires additional rule: %s", f.Name)
		}
	}
	for name, defaultValue := range map[string]string{"fix upgrade": "0", "broadcast type": "1", "daily delete item": "1"} {
		value, err := s.value(name)
		if err != nil || s.has("/"+name) || s.has(name) && value != defaultValue {
			return d, fmt.Errorf("lottery has nondefault or malformed %s", name)
		}
	}
	if s.has("/weekly delete item") || s.has("daily delete item") && s.has("weekly delete item") {
		return d, fmt.Errorf("lottery has ambiguous calendar")
	}
	for _, name := range []string{"need material", "item category", "action usable place"} {
		if !s.block(name, true) {
			return d, fmt.Errorf("lottery block is incomplete: %s", name)
		}
	}
	v := func(name string) string { v, _ := s.value(name); return v }
	if category := v("item category"); category != "" && category != "`event`" {
		return d, fmt.Errorf("lottery requires additional category rules")
	}
	if s.has("need material") {
		if _, err := boxMaterialPairs(v("need material")); err != nil {
			return d, err
		}
	}
	if s.has("action usable place") {
		places, err := fatigueTags(v("action usable place"), quotedPlace)
		if err != nil || !slices.Contains(places, "[village]") || !slices.Contains(places, "[seria room]") ||
			slices.ContainsFunc(places, func(p string) bool { return p != "[village]" && p != "[seria room]" && p != "[dont use assult]" }) {
			return d, fmt.Errorf("lottery has an unsupported usable place")
		}
		d.ForbidScene8 = slices.Contains(places, "[dont use assult]")
	}
	d.GoldCost, err = s.number("lottery use cost", 0, math.MaxInt32, false)
	if err != nil {
		return d, err
	}
	if s.has("lottery use need item") {
		p, err := boxMaterialPairs(v("lottery use need item"))
		if err != nil || len(p) != 2 || items.stackable[int64(p[0])] == "" || items.equipment[int64(p[0])] != "" {
			return d, fmt.Errorf("lottery use material is invalid")
		}
		t, err := items.Gift(p[0])
		if err != nil || t.Kind != 2 || t.PetConsumable {
			return d, fmt.Errorf("lottery material has no ordinary inventory owner")
		}
		templates[p[0]] = t
		d.Materials = append(d.Materials, storage.CommercePayment{TemplateID: p[0], Count: p[1]})
	}
	pool, err := giftInts(v("int data"), false)
	if err != nil || len(pool)%3 != 0 || len(pool) > 32768*3 {
		return d, fmt.Errorf("lottery triple pool is malformed")
	}
	var total uint64
	for i := 0; i < len(pool); i += 3 {
		id, w, count := pool[i], pool[i+1], pool[i+2]
		if id == 0 || id == math.MaxUint32 || count != 1 || items.equipment[int64(id)] == "" {
			return d, fmt.Errorf("lottery prize requires an ordinary equipment owner")
		}
		r := templates[id]
		if r == nil {
			r, err = items.Gift(id)
		}
		if err != nil || r == nil || r.Kind == 2 {
			return d, fmt.Errorf("lottery prize has no complete ordinary equipment template")
		}
		templates[id] = r
		d.Prizes = append(d.Prizes, LotteryPrize{uint64(w), id})
		total += uint64(w)
	}
	if total == 0 {
		return d, fmt.Errorf("lottery prize pool has no positive weight")
	}
	return d, nil
}

func boxMaterialPairs(text string) ([]uint32, error) {
	p, err := giftInts(text, false)
	if err != nil || len(p)%2 != 0 || slices.ContainsFunc(p, func(v uint32) bool { return v == 0 || v > math.MaxInt32 }) {
		return nil, fmt.Errorf("box material pair is invalid")
	}
	seen := map[uint32]bool{}
	for i := 0; i < len(p); i += 2 {
		if seen[p[i]] {
			return nil, fmt.Errorf("box material pair repeats an identity")
		}
		seen[p[i]] = true
	}
	return p, nil
}

var keyedFields = strings.Split("name|name2|explain|flavor text|grade|rarity|weight|value|usable job|attach type|minimum level|icon|icon mark|field image|stackable type|move wav|stack limit|action type|lucky item list|RANDOMBOX|int data|sealing removal item|action usable place|usable event|need material|expiration date|usable period|daily delete item|weekly delete item", "|")

func parseKeyedBox(items *Items, src ItemSource, templates map[uint32]*ItemTemplate) (storage.KeyedBoxRule, error) {
	var d storage.KeyedBoxRule
	fields, err := gm.ItemFields(src.Text)
	if err != nil {
		return d, err
	}
	s := consumableScript{fields}
	seen := map[string]bool{}
	for _, f := range fields {
		if seen[f.Name] || !slices.Contains(keyedFields, strings.TrimLeft(f.Name, "/")) {
			return d, fmt.Errorf("keyed box field unsupported or repeated: %s", f.Name)
		}
		seen[f.Name] = true
	}
	v := func(name string) string { v, _ := s.value(name); return v }
	if v("action type") != "`[randombox]` 1" || v("action usable place") != "`[village]` `[seria room]`" {
		return d, fmt.Errorf("keyed box use policy differs")
	}
	for _, name := range []string{"action type", "action usable place", "int data", "sealing removal item", "need material"} {
		if !s.block(name, true) || name != "need material" && v(name) == "" {
			return d, fmt.Errorf("keyed box block incomplete: %s", name)
		}
	}
	const random = "RANDOMBOX"
	pattern := []string{random, "int data", "/int data", "sealing removal item", "/sealing removal item", "/" + random}
	at := slices.IndexFunc(fields, func(f gm.Field) bool { return f.Name == random })
	if at < 0 || at+len(pattern) > len(fields) || v(random) != "" || v("/"+random) != "" {
		return d, fmt.Errorf("keyed box requires a complete fixed reward/material block")
	}
	for i, name := range pattern {
		if fields[at+i].Name != name {
			return d, fmt.Errorf("keyed box requires a complete fixed reward/material block")
		}
	}
	activity, err := s.number("usable event", 0, math.MaxUint16, false)
	if err != nil || activity == 0 {
		return d, fmt.Errorf("keyed box activity is incomplete")
	}
	p, err := giftInts(v("int data"), false)
	if err != nil || len(p) != 7 || p[0] != p[3] || p[1] != 1 || p[2] != 0 || p[3] == 0 || p[3] == math.MaxUint32 || p[4] == 0 || p[5] != 1 || p[6] != 0 {
		return d, fmt.Errorf("keyed box requires one fixed result")
	}
	id, reward := src.Id, p[3]
	m, err := giftInts(v("sealing removal item"), false)
	if err != nil || len(m) != 5 || m[0] != 2 || m[1] == 0 || m[1] == math.MaxUint32 || m[1] == id || m[1] == reward || m[2] != 1 || m[3] != id || m[4] != 1 || reward == id {
		return d, fmt.Errorf("keyed box requires one key and one source")
	}
	if s.has("need material") {
		price, err := giftInts(v("need material"), false)
		if err != nil || len(price)%2 != 0 || slices.ContainsFunc(price, func(n uint32) bool { return n == 0 || n > math.MaxInt32 }) {
			return d, fmt.Errorf("keyed box acquisition price is malformed")
		}
	}
	kt, err := items.Source(m[1])
	if err != nil || kt.Equipment || cashField(kt, "stackable type") != "`[material]` 1" {
		return d, fmt.Errorf("keyed box key is not the native material")
	}
	source, e1 := src.Gift()
	key, e2 := kt.Gift()
	r, e3 := items.Gift(reward)
	if e1 != nil || e2 != nil || e3 != nil || source.Kind != 2 || key.Kind != 2 || r.Kind != 2 || source.FirstBagSlot != 65 || source.LastBagSlot != 120 ||
		r.FirstBagSlot != 65 || r.LastBagSlot != 120 || source.PetConsumable || key.PetConsumable || r.PetConsumable || key.AccountCommonInventory || r.AccountCommonInventory {
		return d, fmt.Errorf("keyed box acquisition requires three complete ordinary definitions")
	}
	for _, t := range []*ItemTemplate{source, key, r} {
		templates[t.TemplateId] = t
	}
	d.Source, e1 = json.Marshal(source)
	d.Key, e2 = json.Marshal(key)
	d.Reward, e3 = json.Marshal(r)
	d.UsableEvent = uint16(activity)
	if e1 != nil || e2 != nil || e3 != nil {
		return d, fmt.Errorf("keyed box acquisition JSON is invalid")
	}
	return d, nil
}
