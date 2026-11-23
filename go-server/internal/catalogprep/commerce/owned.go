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

type OwnedData struct {
	Commerce storage.CommerceCatalog
	Weights  map[uint32]int64
	Mail     map[uint32]storage.MailAttachmentPolicy
}

// BuildOwned prepares resource-backed catalog additions for persisted item IDs or one C176 grant.
func BuildOwned(items *Items, base *storage.CommerceCatalog, ids []uint32, gmRequest bool, gmTemplate *ItemTemplate,
	ticket func(gm.ItemSource) (*gm.ItemTemplate, error), myth MythologyResolver) (*OwnedData, error) {
	out := &OwnedData{Weights: map[uint32]int64{}, Mail: map[uint32]storage.MailAttachmentPolicy{}}
	roots := slices.Clone(ids)
	slices.Sort(roots)
	roots = slices.Compact(roots)
	if gmRequest && len(roots) != 1 || !gmRequest && gmTemplate != nil {
		return nil, fmt.Errorf("owned item preparation received incompatible GM roots")
	}
	if len(roots) == 0 {
		return out, nil
	}
	if base == nil {
		base = &storage.CommerceCatalog{}
	}
	rootSet := map[uint32]bool{}
	rootSources := make(map[uint32]ItemSource, len(roots))
	for _, id := range roots {
		if id == 0 || id == math.MaxUint32 {
			return nil, fmt.Errorf("owned item template ID %d is invalid", id)
		}
		src, err := items.Source(id)
		if err != nil {
			return nil, fmt.Errorf("owned item template %d: %w", id, err)
		}
		rootSet[id], rootSources[id] = true, src
		if gmTemplate != nil && (gmTemplate.TemplateId != id || gmTemplate.ResourceHash != src.Hash) {
			return nil, fmt.Errorf("GM inventory template %d differs from its authoritative resource", id)
		}
	}

	prepared := map[uint32]*ItemTemplate{}
	if !gmRequest {
		boxes, err := buildBoxes(items, roots, nil, false)
		if err != nil {
			return nil, err
		}
		for _, set := range []map[uint32]*ItemTemplate{boxes.KeyedTemplates, boxes.LuckyTemplates} {
			if err := registerPrepared(prepared, set); err != nil {
				return nil, err
			}
		}
		out.Commerce.KeyedBoxes = ownedNewRows(boxes.KeyedBoxes, base.KeyedBoxes)
		out.Commerce.LuckyBoxes = ownedNewRows(boxes.LuckyBoxes, base.LuckyBoxes)
		out.Commerce.BoxWeights = ownedBoxWeights(items, base.BoxWeights, boxes.KeyedTemplates, boxes.LuckyTemplates)
	}
	gifts, err := BuildGifts(items, roots, ticket)
	if err != nil {
		return nil, err
	}
	if err := registerPrepared(prepared, gifts.Templates); err != nil {
		return nil, err
	}
	out.Commerce.Gifts = ownedNewRows(gifts.Definitions, base.Gifts)
	for id, src := range rootSources {
		if prepared[id] == nil {
			prepared[id], _ = preparedTemplate(src)
		}
	}
	// LocalPvfItemTemplates.PrepareGm registers an ordinary fallback when
	// neither the gift catalog nor ordinary preparation owns the identity.
	if gmTemplate != nil && prepared[gmTemplate.TemplateId] == nil {
		prepared[gmTemplate.TemplateId] = gmTemplate
	}
	if err := populateOwnedAvatars(items, rootSources, gifts, base, &out.Commerce); err != nil {
		return nil, err
	}
	all := make(map[uint32]bool, len(prepared)+len(roots))
	for _, id := range roots {
		all[id] = true
	}
	for id := range prepared {
		all[id] = true
	}
	if !gmRequest {
		if err := populateOwnedStartup(items, roots, rootSet, gifts, prepared, all, base, out); err != nil {
			return nil, err
		}
	}

	craftRoots := sortedIDs(all)
	if gmRequest {
		craftRoots = roots
	}
	if err := populateOwnedCraft(items, craftRoots, prepared, all, base, out); err != nil {
		return nil, err
	}
	if !gmRequest {
		quality, err := BuildQuality(items, sortedIDs(all), prepared, myth)
		if err != nil {
			return nil, err
		}
		qualitySources := map[uint32]*ItemTemplate{}
		for id, t := range quality.Sources {
			if _, known := base.Quality.Sources[id]; all[id] || !known {
				qualitySources[id] = t
			}
		}
		if err := registerPrepared(prepared, qualitySources); err != nil {
			return nil, err
		}
		out.Commerce.Quality.Sources, err = ownedTemplateRows(qualitySources, base.Quality.Sources)
		if err != nil {
			return nil, err
		}
		out.Commerce.Quality.Targets, err = ownedConvertedRows(quality.Targets, base.Quality.Targets)
		if err != nil {
			return nil, err
		}
		out.Commerce.Quality.Mythology, err = ownedConvertedRows(quality.Mythology, base.Quality.Mythology)
		if err != nil {
			return nil, err
		}
	}
	for id := range prepared {
		all[id] = true
	}
	ids = sortedIDs(all)
	if err := populateOwnedInventory(items, ids, gifts.SpecialIDs, prepared, gmRequest, base, out); err != nil {
		return nil, err
	}
	out.Commerce.ContractGrants = ownedNewRows(BuildContractGrants(items, ids), base.ContractGrants)
	return out, nil
}

func populateOwnedStartup(items *Items, roots []uint32, rootSet map[uint32]bool, gifts *Gifts,
	prepared map[uint32]*ItemTemplate, all map[uint32]bool, base *storage.CommerceCatalog, out *OwnedData) error {
	// Registered boxes can extend the catalog with new grant and price dependencies.
	boxes, err := buildBoxes(items, nil, sortedIDs(all), false)
	if err != nil {
		return err
	}
	if err := registerPrepared(prepared, boxes.LotteryTemplates); err != nil {
		return err
	}
	out.Commerce.Lottery, err = ownedConvertedRows(boxes.Lottery, base.Lottery)
	if err != nil {
		return err
	}
	out.Commerce.LotteryTemplates, err = ownedTemplateRows(boxes.LotteryTemplates, base.LotteryTemplates)
	if err != nil {
		return err
	}
	for id, w := range ownedBoxWeights(items, base.BoxWeights, boxes.LotteryTemplates) {
		out.Commerce.BoxWeights[id] = w
	}
	var ordinary []uint32
	for id, t := range prepared {
		if t != nil {
			all[id] = true
			ordinary = append(ordinary, id)
		}
	}
	if err := appendDisassemblyRoots(items, ordinary, all); err != nil {
		return err
	}
	for id := range all {
		if !rootSet[id] && prepared[id] == nil {
			prepared[id], _ = items.Prepared(id)
		}
	}
	pets, err := BuildPets(items, slices.Concat(roots, gifts.SpecialIDs))
	if err != nil {
		return err
	}
	out.Commerce.PetHatch = ownedNewRows(pets.PetHatch, base.PetHatch)
	out.Commerce.PetNames = ownedNewRows(pets.PetNames, base.PetNames)
	return nil
}

func populateOwnedAvatars(items *Items, roots map[uint32]ItemSource, gifts *Gifts, base *storage.CommerceCatalog, out *storage.CommerceCatalog) error {
	avatarClones := map[uint32]gm.AvatarClone{}
	var boxes []uint32
	for id, src := range roots {
		if src.Equipment {
			if clone, ok := src.Clone(); ok {
				avatarClones[id] = clone
			}
		} else if strings.Contains(src.Text, "[booster select category]") {
			boxes = append(boxes, id)
		}
	}
	if len(boxes) > 0 || len(gifts.Definitions) > 0 || len(gifts.SpecialIDs) > 0 {
		avatars, err := ownedAvatarGifts(items, boxes, gifts)
		if err != nil {
			return err
		}
		out.AvatarBoxes, err = ownedConvertedRows(avatars.AvatarBoxes, base.AvatarBoxes)
		if err != nil {
			return err
		}
		for id, clone := range avatars.AvatarClones {
			avatarClones[id] = clone
		}
	}
	var err error
	out.AvatarClones, err = ownedConvertedRows(avatarClones, base.AvatarClones)
	return err
}

func populateOwnedCraft(items *Items, roots []uint32, prepared map[uint32]*ItemTemplate, all map[uint32]bool, base *storage.CommerceCatalog, out *OwnedData) error {
	craft, weights, err := BuildCraft(items, roots, nil, nil)
	if err != nil {
		return err
	}
	requested := make(map[uint32]bool, len(roots))
	for _, id := range roots {
		requested[id] = true
	}
	for id := range craft.Definitions {
		if !requested[id] {
			delete(craft.Definitions, id)
		}
	}
	for id := range craft.Tickets {
		if !requested[id] {
			delete(craft.Tickets, id)
		}
	}
	for id := range craft.Enchants {
		if !requested[id] {
			delete(craft.Enchants, id)
		}
	}
	for id := range craft.Destruction {
		if !requested[id] {
			delete(craft.Destruction, id)
		}
	}
	craft.Definitions = ownedNewRows(craft.Definitions, base.Craft.Definitions)
	craft.Tickets = ownedNewRows(craft.Tickets, base.Craft.Tickets)
	craft.Enchants = ownedNewRows(craft.Enchants, base.Craft.Enchants)
	craft.Destruction = ownedNewRows(craft.Destruction, base.Craft.Destruction)
	needed := map[uint32]bool{}
	quoteIDs := map[int]int{}
	var quotes []storage.CraftStages
	for _, id := range sortedIDs(craft.Definitions) {
		def := craft.Definitions[id]
		quote, ok := quoteIDs[def.Quote]
		if !ok {
			stages := craft.Quotes[def.Quote]
			quote = len(quotes)
			quoteIDs[def.Quote] = quote
			quotes = append(quotes, stages)
			for _, group := range [][]*storage.CraftQuote{stages.Reinforce, stages.Amplify, stages.Forge, stages.Remodel} {
				for _, row := range group {
					if row != nil {
						needed[row.MaterialID] = true
						for reward := range row.DestroyRewards {
							needed[reward] = true
						}
					}
				}
			}
		}
		def.Quote = quote
		craft.Definitions[id] = def
	}
	craft.Quotes = quotes
	for _, row := range craft.Destruction {
		addCraftDestructionMaterials(needed, row)
	}
	for id := range craft.Enchants {
		needed[id] = true
	}
	delete(needed, 0)
	for id, raw := range craft.Templates {
		if !needed[id] {
			delete(craft.Templates, id)
			continue
		}
		if prepared[id] == nil && base.Templates[id] == nil {
			var t ItemTemplate
			if err := json.Unmarshal(raw, &t); err != nil {
				return err
			}
			prepared[id] = &t
			all[id] = true
		}
	}
	craft.Templates = ownedNewRows(craft.Templates, base.Craft.Templates)
	for id := range needed {
		if w, ok := weights[id]; ok {
			out.Weights[id] = w
		}
	}
	out.Commerce.Craft = craft
	return nil
}

func populateOwnedInventory(items *Items, ids, special []uint32, prepared map[uint32]*ItemTemplate, gmRequest bool,
	base *storage.CommerceCatalog, out *OwnedData) error {
	for _, id := range ids {
		w, err := items.Weight(id)
		if err != nil {
			return fmt.Errorf("owned item %d carrying metadata: %w", id, err)
		}
		out.Weights[id] = w
		if t := prepared[id]; t != nil {
			p, err := items.MailAttachment(id, t)
			if err != nil {
				return err
			}
			if p != nil {
				out.Mail[id] = *p
			}
		}
	}
	invIDs := make([]uint32, 0, len(ids))
	specialSort := map[uint32]bool{}
	for _, id := range ids {
		if prepared[id] != nil {
			invIDs = append(invIDs, id)
		} else if items.equipment[int64(id)] != "" {
			specialSort[id] = true
		}
	}
	for id := range out.Commerce.AvatarClones {
		specialSort[id] = true
	}
	for _, id := range special {
		specialSort[id] = true
	}
	if len(invIDs) != 0 || len(specialSort) != 0 {
		var inv *Inventory
		var err error
		specialIDs := sortedIDs(specialSort)
		if gmRequest {
			inv, err = gmOwnedInventory(items, invIDs, specialIDs, prepared)
		} else {
			inv, err = inventoryItems(items, invIDs, prepared, specialIDs, prepared)
		}
		if err != nil {
			return err
		}
		if !gmRequest {
			out.Commerce.SalePrices = ownedNewRows(inv.SalePrices, base.SalePrices)
			out.Commerce.Disassembly = ownedNewRows(inv.Disassembly, base.Disassembly)
			out.Commerce.RepairDefinitions = ownedNewRows(inv.RepairDefinitions, base.RepairDefinitions)
		}
		out.Commerce.Discard = ownedNewRows(inv.Discard, base.Discard)
		out.Commerce.SortMetadata, err = ownedConvertedRows(inv.SortMetadata, base.SortMetadata)
		if err != nil {
			return err
		}
	}
	consumables, err := BuildConsumables(items, ids, ids, prepared)
	if err != nil {
		return err
	}
	out.Commerce.Potions = ownedNewRows(consumables.Potions, base.Potions)
	out.Commerce.FatigueItems = ownedNewRows(consumables.FatigueItems, base.FatigueItems)
	ordinaryEquipment := []uint32{}
	for _, id := range ids {
		if t := prepared[id]; t != nil && t.Kind < 2 {
			ordinaryEquipment = append(ordinaryEquipment, id)
		}
	}
	if len(ordinaryEquipment) != 0 {
		seals, err := BuildMagicSeal(items, ordinaryEquipment)
		if err != nil {
			return err
		}
		out.Commerce.MagicSeal = ownedNewRows(seals, base.MagicSeal)
	}
	out.Commerce.Templates, err = ownedTemplateRows(prepared, base.Templates)
	if err != nil {
		return err
	}
	daily := map[uint32]*ItemTemplate{}
	for id, t := range prepared {
		if t != nil && t.Kind == 2 && t.DailyDeletion {
			daily[id] = t
		}
	}
	out.Commerce.DailyExpiryTemplates, err = ownedTemplateRows(daily, base.DailyExpiryTemplates)
	return err
}

func gmOwnedInventory(items *Items, ordinary, avatars []uint32, prepared map[uint32]*ItemTemplate) (*Inventory, error) {
	out := &Inventory{Discard: map[uint32]storage.DiscardPolicy{}, SortMetadata: map[uint32]SortMetadata{}}
	for _, id := range append(slices.Clone(ordinary), avatars...) {
		src, err := items.Source(id)
		if err != nil {
			return nil, err
		}
		if t := prepared[id]; t != nil {
			discard, ok, err := discardPolicy(src, t)
			if err != nil {
				return nil, err
			}
			if ok {
				out.Discard[id] = discard
			}
		}
		if fields, err := gm.ItemFields(src.Text); err == nil {
			if sort, err := sortMetadata(src, fields); err == nil {
				out.SortMetadata[id] = sort
			}
		}
	}
	return out, nil
}

func ownedBoxWeights(items *Items, base map[uint32]int64, sets ...map[uint32]*ItemTemplate) map[uint32]int64 {
	out := map[uint32]int64{}
	for _, rows := range sets {
		for id := range rows {
			if _, exists := base[id]; !exists {
				if w, err := items.Weight(id); err == nil {
					out[id] = w
				}
			}
		}
	}
	return out
}

func ownedNewRows[T any](rows, base map[uint32]T) map[uint32]T {
	for id := range rows {
		if _, exists := base[id]; exists {
			delete(rows, id)
		}
	}
	return rows
}

func ownedTemplateRows(rows map[uint32]*ItemTemplate, base map[uint32]json.RawMessage) (map[uint32]json.RawMessage, error) {
	out := make(map[uint32]json.RawMessage)
	for id, t := range rows {
		if _, exists := base[id]; exists || t == nil {
			continue
		}
		b, err := json.Marshal(t)
		if err != nil {
			return nil, err
		}
		out[id] = b
	}
	return out, nil
}

func ownedConvertedRows[A, B any](rows map[uint32]A, base map[uint32]B) (map[uint32]B, error) {
	out := make(map[uint32]B)
	for id, a := range rows {
		if _, exists := base[id]; exists {
			continue
		}
		b, err := json.Marshal(a)
		if err != nil {
			return nil, err
		}
		var row B
		if err := json.Unmarshal(b, &row); err != nil {
			return nil, err
		}
		out[id] = row
	}
	return out, nil
}
