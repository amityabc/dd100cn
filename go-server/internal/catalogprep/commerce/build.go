package commerce

import (
	"encoding/json"
	"fmt"
	"maps"
	"math"
	"reflect"
	"slices"

	"s9a1/server/internal/catalogprep/fate"
	"s9a1/server/internal/catalogprep/gm"
	"s9a1/server/internal/catalogprep/towns"
	"s9a1/server/internal/storage"
)

type Options struct {
	GrowthPreparedTemplateIDs []uint32
	QuestTemplateIDs          []uint32
	BattlePass                *BattlePassCashSource
	BattlePassRewards         map[uint32]json.RawMessage
	QuestTicket               func(gm.ItemSource) (*gm.ItemTemplate, error)
	Fate                      *fate.Catalog
	Mythology                 MythologyResolver
	AdditionalPrepared        map[uint32]*ItemTemplate
}

type Catalog struct {
	Commerce                                            map[string]any
	Weights                                             map[uint32]int64
	MailAttachments                                     map[uint32]storage.MailAttachmentPolicy
	Rename                                              map[uint32]*ItemTemplate
	GoldCarryLimits                                     map[byte]int64
	CashPurchasePolicies, CharacterCashPurchasePolicies map[uint32]PurchasePolicy
}

func Build(items *Items, town *towns.Catalog, opt Options) (*Catalog, error) {
	shops, err := BuildShops(items, town)
	if err != nil {
		return nil, err
	}
	var pass []BattlePassCashSource
	if opt.BattlePass != nil {
		pass = append(pass, *opt.BattlePass)
	}
	cash, err := BuildCash(items, pass...)
	if err != nil {
		return nil, err
	}
	packages, err := BuildCashPackageUse(items)
	if err != nil {
		return nil, err
	}
	rename, err := items.Rename()
	if err != nil {
		return nil, err
	}
	merchant, initialGifts, petRoots := commerceResourceRoots(cash, shops)
	initial, err := BuildGifts(items, initialGifts, opt.QuestTicket)
	if err != nil {
		return nil, err
	}
	ids := map[uint32]bool{renameBagSource: true, 10312874: true, 10319994: true, 10312271: true, 10312272: true, 10312273: true, 10312274: true, 10319996: true}
	for _, id := range merchant {
		ids[id] = true
	}
	for _, id := range opt.QuestTemplateIDs {
		if id != 0 {
			ids[id] = true
		}
	}
	for id := range initial.Templates {
		ids[id] = true
	}
	for _, pkg := range packages {
		for _, child := range pkg.Contents {
			ids[child.Template.TemplateId] = true
		}
	}
	if err := appendDisassemblyRoots(items, merchant, ids); err != nil {
		return nil, err
	}
	roots := sortedIDs(ids)
	pinned := map[uint32]*ItemTemplate{renameBagSource: rename[renameBagSource]}
	prepared := maps.Clone(pinned)
	for _, id := range slices.Concat(roots, opt.GrowthPreparedTemplateIDs, []uint32{8008}) {
		if prepared[id] == nil {
			if t, err := items.Prepared(id); err == nil && t != nil {
				prepared[id] = t
			}
		}
	}
	weights, mail, err := items.WeightsAndMail(roots, pinned)
	if err != nil {
		return nil, err
	}
	gold, err := GoldCarryLimits(items.source)
	if err != nil {
		return nil, err
	}
	gifts, err := BuildGifts(items, append(slices.Clone(initialGifts), roots...), opt.QuestTicket)
	if err != nil {
		return nil, err
	}
	allGiftIDs := append(slices.Clone(roots), sortedIDs(gifts.Templates)...)
	boxes, err := BuildBoxes(items, roots, allGiftIDs)
	if err != nil {
		return nil, err
	}
	if err := registerPrepared(prepared, boxes.KeyedTemplates); err != nil {
		return nil, err
	}
	templates := make(map[uint32]*ItemTemplate, len(prepared))
	for _, id := range roots {
		if t := gifts.Templates[id]; t != nil {
			templates[id] = t
		} else if t := prepared[id]; t != nil {
			templates[id] = t
		}
	}
	// The inventory sort snapshot precedes the NPC payment overlays.
	avatars, err := BuildAvatarProducts(items, allGiftIDs, gifts)
	if err != nil {
		return nil, err
	}
	avatarSort := make([]uint32, 0, len(avatars.AvatarOffers))
	for _, row := range avatars.AvatarOffers {
		avatarSort = append(avatarSort, row.Template.TemplateId)
	}
	coupons, err := BuildContainerCoupons(items)
	if err != nil {
		return nil, err
	}
	cargoGift, err := BuildCargoGift(items)
	if err != nil {
		return nil, err
	}
	pets, err := BuildPets(items, append(petRoots, gifts.SpecialIDs...))
	if err != nil {
		return nil, err
	}
	if err := registerPrepared(prepared, opt.AdditionalPrepared); err != nil {
		return nil, err
	}
	quality, err := BuildQuality(items, roots, prepared, opt.Mythology)
	if err != nil {
		return nil, err
	}
	if err := registerPrepared(prepared, quality.Sources); err != nil {
		return nil, err
	}
	craft, craftWeights, err := BuildCraft(items, roots, sortedIDs(prepared), shops)
	if err != nil {
		return nil, err
	}
	for id, raw := range craft.Templates {
		if prepared[id] == nil {
			var t ItemTemplate
			if err := json.Unmarshal(raw, &t); err != nil {
				return nil, err
			}
			prepared[id] = &t
		}
	}
	inventory, err := BuildInventory(items, roots, templates, avatarSort, prepared)
	if err != nil {
		return nil, err
	}
	for id, t := range shops.PaymentTemplates {
		if t != nil {
			templates[id] = t
		}
	}
	consumables, err := BuildConsumables(items, roots, sortedIDs(prepared), prepared)
	if err != nil {
		return nil, err
	}
	seals, err := BuildMagicSeal(items, sortedIDs(prepared))
	if err != nil {
		return nil, err
	}
	exchange, fateBoxes, fateIDs, err := BuildFateCommerce(items, opt.Fate)
	if err != nil {
		return nil, err
	}
	boxIDs := map[uint32]bool{}
	for _, set := range []map[uint32]*ItemTemplate{boxes.LotteryTemplates, boxes.KeyedTemplates, boxes.LuckyTemplates} {
		for id := range set {
			boxIDs[id] = true
		}
	}
	for _, id := range fateIDs {
		boxIDs[id] = true
	}
	boxWeights := maps.Clone(craftWeights)
	if boxWeights == nil {
		boxWeights = map[uint32]int64{}
	}
	for _, id := range sortedIDs(boxIDs) {
		if weight, err := items.Weight(id); err == nil {
			boxWeights[id] = weight
		}
	}
	projectedCash, err := composeCashTemplates(cash.Offers, templates)
	if err != nil {
		return nil, err
	}
	daily, err := dailyExpiry(shops, opt.BattlePassRewards, coupons, boxes, gifts, prepared)
	if err != nil {
		return nil, err
	}
	dungeonPeers := make([]struct{ Npc, Shop uint32 }, 0, len(shops.DungeonPeers))
	for _, peer := range shops.DungeonPeers {
		dungeonPeers = append(dungeonPeers, struct{ Npc, Shop uint32 }{peer.NPC, peer.Shop})
	}
	commerce := map[string]any{
		"AccountCargo": cash.AccountCargo, "DailyExpiryTemplates": daily,
		"TownPeers": shops.TownPeers, "TownAreas": shops.TownAreas, "TownPhases": shops.TownPhases, "TownMaps": shops.TownMaps,
		"Visibility": shops.Visibility, "DungeonPeers": dungeonPeers, "OculusPeers": shops.OculusPeers,
		"Products": shops.Products, "DungeonProducts": shops.DungeonProducts,
		"SalePrices": inventory.SalePrices, "Templates": templates, "PurchaseCodes": cash.AccountCodes,
		"Gifts": gifts.Definitions, "CashPackages": packages, "CargoGift": cargoGift, "ContractGrants": BuildContractGrants(items, allGiftIDs),
		"Potions": consumables.Potions, "FatigueItems": consumables.FatigueItems, "Discard": inventory.Discard,
		"Disassemblers": shops.Disassemblers, "Disassembly": inventory.Disassembly, "RepairNPCs": shops.RepairNPCs, "RepairDefinitions": inventory.RepairDefinitions,
		"SortMetadata": inventory.SortMetadata, "AvatarOffers": avatars.AvatarOffers, "AvatarBoxes": avatars.AvatarBoxes, "AvatarClones": avatars.AvatarClones,
		"ContainerCoupons": coupons, "MagicSeal": seals, "PetHatch": pets.PetHatch, "PetNames": pets.PetNames,
		"PetRenameCard": pets.PetRenameCard, "PetCare": pets.PetCare, "PetFeed": pets.PetFeed,
		"Lottery": boxes.Lottery, "LotteryTemplates": boxes.LotteryTemplates, "KeyedBoxes": boxes.KeyedBoxes, "LuckyBoxes": boxes.LuckyBoxes,
		"Quality": quality, "Craft": craft, "GmApcs": inventory.GmApcs, "FateExchange": exchange, "FateBoxes": fateBoxes, "BoxWeights": boxWeights,
		"Activities": []uint16{4171, 2520, 2554}, "Cash": projectedCash,
	}
	return &Catalog{Commerce: commerce, Weights: weights, MailAttachments: mail, Rename: rename, GoldCarryLimits: gold,
		CashPurchasePolicies: cash.AccountPolicies, CharacterCashPurchasePolicies: cash.CharacterPolicies}, nil
}

func commerceResourceRoots(cash *CashData, shops *Shops) (merchant, giftRoots, pets []uint32) {
	ids := map[uint32]bool{}
	candidates := map[uint32]bool{}
	for _, p := range append(slices.Clone(shops.Products), shops.DungeonProducts...) {
		ids[p.TemplateID] = true
		for _, m := range p.Materials {
			ids[m.TemplateID] = true
		}
	}
	for _, id := range shops.InputIDs {
		ids[id] = true
	}
	for _, q := range cash.Offers {
		if len(q.PetTemplate) != 0 && string(q.PetTemplate) != "null" {
			pets = append(pets, q.TemplateID)
		}
		if q.TargetBagExpansion == 0 && q.TargetPersonalCargoCapacity == 0 && q.TargetAccountCargoCapacity == 0 && q.TargetAvatarExpansion == 0 &&
			q.NameTag == nil && !q.RevivalCoins && len(q.PetTemplate) == 0 && q.PremiumType == 0 && q.BattlePassHash == "" && q.SkillStyleHash == "" {
			ids[q.TemplateID], candidates[q.TemplateID] = true, true
		}
		for _, m := range q.BonusItems {
			ids[m.TemplateID], candidates[m.TemplateID] = true, true
		}
		for id := range q.RequiredCouponTemplates {
			ids[id], candidates[id] = true, true
		}
	}
	delete(ids, 0)
	delete(candidates, 0)
	return sortedIDs(ids), sortedIDs(candidates), pets
}

func appendDisassemblyRoots(items *Items, merchant []uint32, ids map[uint32]bool) error {
	disjoint, err := readDisjoint(items.source)
	if err != nil {
		return err
	}
	for _, id := range merchant {
		if items.equipment[int64(id)] == "" {
			continue
		}
		r, err := items.Source(id)
		if err != nil {
			return err
		}
		fields, err := gm.ItemFields(r.Text)
		if err != nil {
			return err
		}
		price, err := itemValue(fields)
		if err != nil {
			return err
		}
		if price < 0 || price > math.MaxUint32 || math.Trunc(price) != price || firstNumeric(cashField(r, "not for sale"), 0) != 0 || firstNumeric(cashField(r, "cannot sell"), 0) != 0 {
			continue
		}
		row, ok := disjoint.recipe(r, price)
		if !ok {
			continue
		}
		rewards := append(slices.Clone(row.Additional), row.Cube, row.Expanded)
		if slices.ContainsFunc(rewards, func(id uint32) bool {
			return id != 0 && (items.equipment[int64(id)] != "" || items.stackable[int64(id)] == "")
		}) {
			continue
		}
		for _, id := range rewards {
			if id != 0 {
				ids[id] = true
			}
		}
	}
	return nil
}

func registerPrepared(dst, src map[uint32]*ItemTemplate) error {
	for id, t := range src {
		e := dst[id]
		if e == nil || reflect.DeepEqual(e, t) {
			dst[id] = t
			continue
		}
		plain := *t
		plain.PvfGiftItem, plain.DailyDeletion, plain.InitialSealed, plain.FatigueTimeMonthlyDeletion = false, false, false, false
		plain.ResourceExpiration, plain.WeeklyDeletionDay, plain.RelativeLifetimeSeconds = nil, nil, 0
		if e.PvfGiftItem || !t.PvfGiftItem || !reflect.DeepEqual(e, &plain) {
			return fmt.Errorf("gift acquisition for %d differs from the prepared ordinary template", id)
		}
	}
	return nil
}

type cashProjection struct {
	storage.CashOffer
	SkillStyleHash, BattlePassHash *string
}

func composeCashTemplates(offers map[uint32]storage.CashOffer, templates map[uint32]*ItemTemplate) (map[uint32]cashProjection, error) {
	out := make(map[uint32]cashProjection, len(offers))
	for sku, offer := range offers {
		if t := templates[offer.TemplateID]; t != nil {
			raw, err := json.Marshal(t)
			if err != nil {
				return nil, err
			}
			offer.Template = raw
		} else {
			offer.Template = nil
		}
		row := cashProjection{CashOffer: offer}
		if offer.SkillStyleHash != "" {
			row.SkillStyleHash = &offer.SkillStyleHash
		}
		if offer.BattlePassHash != "" {
			row.BattlePassHash = &offer.BattlePassHash
		}
		out[sku] = row
	}
	return out, nil
}

func dailyExpiry(shops *Shops, pass map[uint32]json.RawMessage, coupons map[uint32]storage.ContainerCoupon, boxes *Boxes, gifts *Gifts, prepared map[uint32]*ItemTemplate) (map[uint32]*ItemTemplate, error) {
	all := map[uint32]*ItemTemplate{}
	add := func(t *ItemTemplate) {
		if t != nil && all[t.TemplateId] == nil {
			all[t.TemplateId] = t
		}
	}
	addRaw := func(raw json.RawMessage) error {
		var t ItemTemplate
		if err := json.Unmarshal(raw, &t); err != nil {
			return err
		}
		add(&t)
		return nil
	}
	for _, t := range shops.PurchaseTemplates {
		add(t)
	}
	for _, t := range pass {
		if err := addRaw(t); err != nil {
			return nil, err
		}
	}
	for _, c := range coupons {
		if err := addRaw(c.Template); err != nil {
			return nil, err
		}
	}
	for _, set := range []map[uint32]*ItemTemplate{boxes.KeyedTemplates, boxes.LuckyTemplates, gifts.Templates, prepared, boxes.LotteryTemplates} {
		for _, t := range set {
			add(t)
		}
	}
	for id, t := range all {
		if t.Kind != 2 || !t.DailyDeletion {
			delete(all, id)
		}
	}
	return all, nil
}
