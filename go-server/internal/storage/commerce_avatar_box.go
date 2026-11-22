package storage

import (
	"encoding/json"
	"slices"
	"time"
)

type AvatarGiftBox struct {
	Source         json.RawMessage
	SelectionCount int
	Categories     []struct {
		Index, Color byte
		Products     []struct {
			GiftSpecial
			AttachType byte
		}
	}
}

func (x *CommerceTx) openAvatarGift(i int, q GiftRequest, d AvatarGiftBox, now time.Time) error {
	if q.Opcode != 160 || q.Random || d.SelectionCount < 0 || d.SelectionCount > 255 ||
		!sameCommerceTemplate(d.Source, x.Inventory.Items[i].Template) {
		return ErrInvalid
	}
	at := -1
	for n, cat := range d.Categories {
		if cat.Index == q.Category && cat.Color == q.Subcategory {
			if at >= 0 {
				return ErrInvalid
			}
			at = n
		}
	}
	if at < 0 {
		return ErrInvalid
	}
	products := d.Categories[at].Products
	wanted := d.SelectionCount
	if wanted == 0 {
		wanted = len(products)
	}
	var choice *GiftChoice
	for _, c := range q.Choices {
		if len(c.Items) == wanted {
			if choice != nil {
				return ErrInvalid
			}
			x := c
			choice = &x
		}
	}
	if choice == nil || wanted == 0 || wanted > 255 {
		return ErrInvalid
	}
	for id := range choice.Abilities {
		if !slices.Contains(choice.Items, id) {
			return ErrInvalid
		}
	}
	type product struct {
		gift            GiftSpecial
		id              uint32
		ability, attach byte
	}
	byID := map[uint32]int{}
	for n, p := range products {
		var t specialPurchaseTemplate
		if json.Unmarshal(p.Template, &t) != nil || t.Kind != 0 || t.TemplateID == 0 || p.ResourcePeriod != 0 && t.RelativeLifetimeSeconds == 0 {
			return ErrInvalid
		}
		if _, dup := byID[t.TemplateID]; dup {
			return ErrInvalid
		}
		byID[t.TemplateID] = n
	}
	selected := make([]product, 0, wanted)
	seen := map[uint32]bool{}
	for n, id := range choice.Items {
		pos, ok := byID[id]
		if !ok || seen[id] || d.SelectionCount == 0 && pos != n {
			return ErrInvalid
		}
		seen[id] = true
		p := products[pos]
		ability := choice.Abilities[id]
		var t specialPurchaseTemplate
		if json.Unmarshal(p.Template, &t) != nil || ability == 255 || p.Abilities[ability]&t.UsableJobs == 0 {
			return ErrInvalid
		}
		selected = append(selected, product{p.GiftSpecial, id, ability, p.AttachType})
	}
	if err := x.debitAsset(i, 1); err != nil {
		return err
	}
	for _, p := range selected {
		if err := x.grantSpecial(p.gift.Template, uint16(p.ability), "", now, false); err != nil {
			return err
		}
		x.gift.Avatar = true
		x.gift.Rewards = append(x.gift.Rewards, GiftResultRow{p.id, 1, p.attach})
	}
	return nil
}
