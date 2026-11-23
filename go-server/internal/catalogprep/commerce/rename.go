package commerce

import (
	"fmt"
	"regexp"
	"slices"
	"strings"

	"s9a1/server/internal/catalogprep/gm"
)

const (
	renameBagSource      = uint32(2660698)
	renamePurchaseSource = uint32(2682290)
)

var renameShopRow = regexp.MustCompile("(?:^|[^0-9])100902\\s+2682290\\s+1\\s+0\\s+0\\s+8800\\s+`[^`]*`\\s+0\\s+0(?:\\s|$)")

func (i *Items) Rename() (map[uint32]*ItemTemplate, error) {
	out := make(map[uint32]*ItemTemplate, 2)
	for _, row := range []struct {
		id                     uint32
		path, hash, typ, level string
	}{
		{renameBagSource, "stackable/cash/charcter_rename_card.stk", "1FD7416608FE7E5242838096205BE043B040EAF85EDDD4BC245C301315B7E2D0", "`[waste]` 0", "1"},
		{renamePurchaseSource, "stackable/cash/chn_character_rename.stk", "0D01AF1A093C16D2834C339F8B864AB2357DFD5F6D569EB9C4A56A6161E6300F", "`[etc]` 0", "30"},
	} {
		r, err := i.Source(row.id)
		if err != nil {
			return nil, err
		}
		if r.Path != row.path || r.Hash != row.hash || r.Equipment {
			return nil, fmt.Errorf("character rename source %d differs from the matched client resource", row.id)
		}
		required := map[string]string{"stackable type": row.typ, "minimum level": row.level, "usable job": "`[all]`", "attach type": "`[trade]`"}
		if row.id == renameBagSource {
			required["action type"] = "`[character name change]`"
			required["action usable place"] = "`[village]` `[seria room]` `[dont use assult]`"
			required["need material"] = "10015553 15"
			required["impossible contents"] = "`gift`"
		}
		for tag, want := range required {
			text, err := gm.ItemField(r.Text, tag)
			if err != nil || text != want {
				return nil, fmt.Errorf("character rename source %d field %q differs from the matched metadata", row.id, tag)
			}
		}
		template, err := r.Gift()
		if err != nil {
			return nil, err
		}
		if template.Kind != 2 || template.FirstBagSlot != 65 || template.PetConsumable {
			return nil, fmt.Errorf("character rename template %d cannot enter normal inventory", row.id)
		}
		out[row.id] = template
	}
	shop, err := readCommerceText(i.source, "etc/(r)cerashop.etc")
	if err != nil {
		return nil, err
	}
	premium, err := commerceBlock(shop, "premium")
	if err != nil {
		return nil, err
	}
	if len(renameShopRow.FindAllString(premium, -1)) != 1 {
		return nil, fmt.Errorf("character rename requires its unique complete native quote")
	}
	block, err := commerceBlock(shop, "buy restrict item")
	if err != nil {
		return nil, err
	}
	restrictions := numbers(block)
	if len(restrictions)%5 != 0 {
		return nil, fmt.Errorf("character rename purchase restriction grammar is invalid")
	}
	matched := 0
	for at := 0; at < len(restrictions); at += 5 {
		row := restrictions[at : at+5]
		if row[1] == "100902" {
			if !slices.Equal(row, []string{"2", "100902", "90", "1", "0"}) {
				return nil, fmt.Errorf("character rename ninety-day resource policy changed")
			}
			matched++
		}
	}
	if matched != 1 {
		return nil, fmt.Errorf("character rename requires one ninety-day policy")
	}
	for _, check := range []struct{ tag, term string }{
		{"discount list", "100902"}, {"specific product mileage", "100902"}, {"return user buy restrict code", "100902"},
		{"buy only cera point", "2682290"}, {"buy only cera", "2682290"},
	} {
		block, err := commerceBlock(shop, check.tag)
		if err != nil {
			return nil, err
		}
		if containsNumber(block, check.term) {
			return nil, fmt.Errorf("character rename has unsupported supplemental %s policy", check.tag)
		}
	}
	for _, resource := range []struct{ path, tag string }{{"etc/chn_cerashop_multibonus.etc", ""}, {"etc/chn_coupon.etc", "coupon"}} {
		text, err := i.source.ReadText(resource.path, scriptLimit)
		if err != nil {
			return nil, err
		}
		if resource.tag != "" {
			text, err = commerceBlock(text, resource.tag)
			if err != nil {
				return nil, err
			}
		}
		if containsNumber(strings.TrimSpace(text), "100902") {
			return nil, fmt.Errorf("character rename bonus or explicit coupon is unsupported")
		}
	}
	return out, nil
}
