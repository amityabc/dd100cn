package commerce

import (
	"errors"
	"fmt"
	"maps"
	"math"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"s9a1/server/internal/catalogprep/gm"
	"s9a1/server/internal/storage"
)

type AvatarOffer struct {
	Template  gm.SpecialTemplate
	Cera      uint32
	Abilities map[byte]uint16
	Model     byte
}
type AvatarBoxProduct struct {
	Template       gm.SpecialTemplate
	Abilities      map[byte]uint16
	Model          uint32
	ResourcePeriod int64
	AttachType     byte
}
type avatarCategory struct {
	Index, Color byte
	Products     []AvatarBoxProduct
}
type AvatarBox struct {
	Source         *ItemTemplate
	Categories     []avatarCategory
	SelectionCount int
}
type AvatarProducts struct {
	AvatarOffers map[uint32]AvatarOffer
	AvatarBoxes  map[uint32]AvatarBox
	AvatarClones map[uint32]gm.AvatarClone
}

var avatarSkuRow = regexp.MustCompile(`\s*(\d+)\s+(\d+)\s+(-?\d+)\s+(-?\d+)\s+(-?\d+)\s+(-?\d+)`)
var avatarCategoryBlock = regexp.MustCompile(`(?ims)^\[booster select category\]\s*(.*?)^\[/booster select category\]`)
var avatarCategoryRows = regexp.MustCompile(`(?s)^\s*(\d+)\s+(\d+)\s+\[avatar\]\s*(.*?)\[/avatar\]\s*$`)

func BuildAvatarProducts(items *Items, roots []uint32, gifts *Gifts) (*AvatarProducts, error) {
	s, err := readCashResources(items)
	if err != nil {
		return nil, err
	}
	abilities, err := items.source.ReadText("skill/abilitydatas.dat", scriptLimit)
	if err != nil {
		return nil, err
	}
	cases, err := gm.AbilityCases(abilities)
	if err != nil {
		return nil, err
	}
	out := &AvatarProducts{map[uint32]AvatarOffer{}, map[uint32]AvatarBox{}, map[uint32]gm.AvatarClone{}}
	avatar, err := commerceBlock(s.shop, "avatar")
	if err != nil {
		return nil, err
	}
	at := 0
	seen := map[uint32]bool{}
	offers := map[[2]uint32]*AvatarOffer{}
	cloneIDs := map[uint32]bool{}
	for _, m := range avatarSkuRow.FindAllStringSubmatchIndex(avatar, -1) {
		if m[0] != at {
			return nil, errors.New("invalid avatar SKU row")
		}
		at = m[1]
		col := func(i int) string { return avatar[m[2*i]:m[2*i+1]] }
		code, e := strconv.ParseUint(col(1), 10, 32)
		if e != nil || code == 0 || code == math.MaxUint32 || seen[uint32(code)] {
			return nil, errors.New("invalid or repeated avatar SKU")
		}
		sku := uint32(code)
		seen[sku] = true
		if col(3) != "1" && col(3) != "2" && col(3) != "3" || col(4) != "0" && col(4) != "1" || col(5) != "0" || col(6) != "-1" {
			continue
		}
		n, e := strconv.ParseUint(col(2), 10, 32)
		if e != nil {
			return nil, e
		}
		id := uint32(n)
		if len(s.restrictions[sku]) != 0 || len(s.discounts[sku]) != 0 || s.coupons[sku] || s.bonused[sku] > 0 || s.points[id] > 0 {
			continue
		}
		model := byte(col(3)[0] - '0')
		key := [2]uint32{id, uint32(model)}
		o, ok := offers[key]
		if !ok {
			if items.equipment[int64(id)] != "" {
				src, e := items.Source(id)
				if e != nil {
					return nil, e
				}
				t, cera, ability, e := src.CashAvatar(model, cases)
				if e == nil {
					o = &AvatarOffer{t, cera, ability, model}
				}
			}
			offers[key] = o
		}
		if o != nil {
			out.AvatarOffers[sku] = *o
			if o.Template.WornSlot <= 7 {
				cloneIDs[id] = true
			}
		}
	}
	if strings.TrimSpace(avatar[at:]) != "" {
		return nil, errors.New("incomplete avatar SKU table")
	}
	return addAvatarGifts(out, items, append(slices.Clone(roots), 490709620), gifts, cases, cloneIDs)
}

func ownedAvatarGifts(items *Items, roots []uint32, gifts *Gifts) (*AvatarProducts, error) {
	abilities, err := items.source.ReadText("skill/abilitydatas.dat", scriptLimit)
	if err != nil {
		return nil, err
	}
	cases, err := gm.AbilityCases(abilities)
	if err != nil {
		return nil, err
	}
	out := &AvatarProducts{AvatarBoxes: map[uint32]AvatarBox{}, AvatarClones: map[uint32]gm.AvatarClone{}}
	return addAvatarGifts(out, items, roots, gifts, cases, map[uint32]bool{})
}

func addAvatarGifts(out *AvatarProducts, items *Items, roots []uint32, gifts *Gifts, cases map[uint32]map[byte]uint16, cloneIDs map[uint32]bool) (*AvatarProducts, error) {
	ids := map[uint32]bool{}
	for _, id := range roots {
		ids[id] = true
	}
	for id := range gifts.Templates {
		ids[id] = true
	}
	if len(ids) > 32768 {
		return nil, errors.New("too many avatar gift sources")
	}
	special := map[[2]uint32]*AvatarBoxProduct{}
	for _, id := range slices.Sorted(maps.Keys(ids)) {
		if items.stackable[int64(id)] == "" {
			continue
		}
		src, err := items.Source(id)
		if err != nil {
			return nil, err
		}
		if !strings.Contains(src.Text, "[booster select category]") {
			continue
		}
		box, err := readAvatarBox(items, src, cases, special)
		if err != nil {
			continue
		}
		out.AvatarBoxes[id] = box
		for _, cat := range box.Categories {
			for _, p := range cat.Products {
				if p.Template.WornSlot <= 7 {
					cloneIDs[p.Template.TemplateId] = true
				}
			}
		}
	}
	for _, id := range gifts.SpecialIDs {
		cloneIDs[id] = true
	}
	for _, id := range slices.Sorted(maps.Keys(cloneIDs)) {
		src, err := items.Source(id)
		if err != nil {
			return nil, err
		}
		if c, ok := src.Clone(); ok {
			out.AvatarClones[id] = c
		}
	}
	return out, nil
}
func readAvatarBox(items *Items, src ItemSource, cases map[uint32]map[byte]uint16, cache map[[2]uint32]*AvatarBoxProduct) (AvatarBox, error) {
	var box AvatarBox
	t, err := src.Gift()
	if err != nil {
		return box, err
	}
	typ := cashField(src, "stackable type")
	reward := func(id, count uint32, reason string) storage.GiftReward {
		return storage.GiftReward{TemplateID: id, Count: count, UnavailableReason: &reason}
	}
	special := func(id, count, model uint32, period int64) storage.GiftReward {
		return reward(id, count, "avatar selection")
	}
	base, err := parseGiftBox(t, src.Text, typ, reward, special)
	if err != nil || base.SelectionCount < 0 || len(base.Materials) != 0 || base.UsableFrom != nil || base.Usage != nil || base.MaximumLevel != 255 || base.SetEpicExchangeInstanceType || base.SetDisjointInstanceType || base.SetBanTransportStone {
		return box, errors.New("unsupported avatar gift box policy")
	}
	box.Source = t
	box.SelectionCount = base.SelectionCount
	box.Categories = []avatarCategory{}
	seen := map[[2]byte]bool{}
	for _, m := range avatarCategoryBlock.FindAllStringSubmatch(src.Text, -1) {
		g := avatarCategoryRows.FindStringSubmatch(m[1])
		if g == nil {
			return box, errors.New("unsupported avatar category")
		}
		index, e1 := strconv.ParseUint(g[1], 10, 8)
		color, e2 := strconv.ParseUint(g[2], 10, 8)
		k := [2]byte{byte(index), byte(color)}
		if e1 != nil || e2 != nil || seen[k] {
			return box, errors.New("invalid avatar category")
		}
		seen[k] = true
		v, err := giftInts(g[3], false)
		if err != nil || len(v) < 4 || len(v) > 1020 || len(v)%4 != 0 {
			return box, errors.New("invalid avatar box rewards")
		}
		c := avatarCategory{Index: k[0], Color: k[1]}
		parts := map[uint32]bool{}
		slots := map[uint16]bool{}
		for row := range slices.Chunk(v, 4) {
			id, model := row[0], row[2]
			if parts[id] || row[1] != 1 || model != 3 && model != 4 || row[3] != 0 {
				return box, errors.New("invalid avatar part")
			}
			parts[id] = true
			key := [2]uint32{id, model}
			p, ok := cache[key]
			if !ok {
				src, e := items.Source(id)
				if e == nil && src.Equipment {
					sp, e := src.SpecialGift(cases, model)
					if e == nil && sp.Template.Kind == 0 {
						p = &AvatarBoxProduct{sp.Template, sp.Abilities, sp.Model, sp.ResourcePeriod, sp.AttachType}
					}
				}
				cache[key] = p
			}
			if p == nil || box.SelectionCount == 0 && slots[p.Template.WornSlot] {
				return box, fmt.Errorf("unavailable or duplicate avatar part %d", id)
			}
			slots[p.Template.WornSlot] = true
			c.Products = append(c.Products, *p)
		}
		if box.SelectionCount > len(c.Products) {
			return box, errors.New("too few avatar box choices")
		}
		box.Categories = append(box.Categories, c)
	}
	if len(box.Categories) == 0 || len(box.Categories) != len(base.Categories) {
		return box, errors.New("incomplete avatar box categories")
	}
	return box, nil
}
