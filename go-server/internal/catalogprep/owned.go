package catalogprep

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"slices"

	"s9a1/server/internal/catalogprep/commerce"
	"s9a1/server/internal/catalogprep/dungeons"
	"s9a1/server/internal/catalogprep/gm"
	"s9a1/server/internal/catalogprep/quest"
	"s9a1/server/internal/pvf"
	"s9a1/server/internal/resources"
	"s9a1/server/internal/storage"
)

// PrepareOwned binds gameplay operations for the authoritative saved items
// found at startup. The caller publishes the catalog after preparation succeeds.
func PrepareOwned(ctx context.Context, current *resources.CharacterCatalog, pvfPath, profilePath string, owned []uint32) error {
	if len(owned) == 0 {
		return nil
	}
	return prepareItems(ctx, current, pvfPath, profilePath, owned, false)
}

// PrepareGMItem uses the upstream per-grant preparation rules. Maps and quote
// tables referenced by an earlier game frame remain valid until it finishes.
func PrepareGMItem(ctx context.Context, current *resources.CharacterCatalog, pvfPath, profilePath string, id uint32) error {
	return prepareItems(ctx, current, pvfPath, profilePath, []uint32{id}, true)
}

func openItemPreparation(ctx context.Context, current *resources.CharacterCatalog, pvfPath, profilePath string) (*pvf.Archive, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if current == nil {
		return nil, errors.New("item preparation requires a loaded character catalog")
	}
	hash, err := gm.FileMetadata(pvfPath, profilePath, false)
	if err != nil {
		return nil, err
	}
	if current.GM.Pvf != hash.PvfSha256 || current.GM.Sidecar != hash.SidecarSha256 || current.GM.Profile != hash.ProfileSha256 {
		return nil, errors.New("saved item preparation inputs differ from the active catalog")
	}
	return pvf.Open(pvfPath, profilePath)
}

func prepareItems(ctx context.Context, current *resources.CharacterCatalog, pvfPath, profilePath string, ids []uint32, gmRequest bool) error {
	a, err := openItemPreparation(ctx, current, pvfPath, profilePath)
	if err != nil {
		return err
	}
	defer a.Close()
	items, err := commerce.NewItems(a)
	if err != nil {
		return err
	}
	var myth commerce.MythologyResolver
	var grant resources.GmGrant
	var gmTemplate *gm.ItemTemplate
	gmView := current.GM
	if gmRequest {
		var prepared bool
		grant, prepared = gmView.PreparedGrant(ids[0])
		if !prepared {
			src, err := items.Source(ids[0])
			if err != nil {
				return err
			}
			grant, err = gm.PrepareGrant(a, src)
			if err != nil {
				return err
			}
			gmView, err = gmView.WithGrant(ids[0], grant)
			if err != nil {
				return err
			}
		}
		if len(grant.Template) != 0 && string(grant.Template) != "null" {
			if err = json.Unmarshal(grant.Template, &gmTemplate); err != nil {
				return err
			}
		}
	} else {
		myth, err = dungeons.BuildMythologyResolver(a)
		if err != nil {
			return err
		}
	}
	data, err := commerce.BuildOwned(items, &current.Commerce, ids, gmRequest, gmTemplate, quest.GiftQuestTicket, myth)
	if err != nil {
		return err
	}
	if err = ctx.Err(); err != nil {
		return err
	}
	next := *current
	next.Commerce, err = mergeOwnedCommerce(current.Commerce, data.Commerce)
	if err != nil {
		return err
	}
	next.MailAttachments = addResources(current.MailAttachments, data.Mail)
	next.Weights = addResources(current.Weights, data.Weights)
	next.GM = gmView.WithOrdinary(next.Commerce.Templates)
	*current = next
	return nil
}

func addResources[V any](base, additions map[uint32]V) map[uint32]V {
	updates := false
	for id := range additions {
		if _, exists := base[id]; !exists {
			updates = true
			break
		}
	}
	if !updates {
		return base
	}
	next := make(map[uint32]V, len(base)+len(additions))
	maps.Copy(next, base)
	for id, v := range additions {
		if _, exists := base[id]; !exists {
			next[id] = v
		}
	}
	return next
}

// An exported quote number refers only to positions in its own Quotes list.
func mergeCraft(base, extra storage.CraftCatalog) (storage.CraftCatalog, error) {
	var missing []uint32
	for id := range extra.Definitions {
		if _, ok := base.Definitions[id]; !ok {
			missing = append(missing, id)
		}
	}
	if len(missing) > 0 {
		slices.Sort(missing)
		groups := map[[5]int]int{}
		for _, r := range base.Definitions {
			groups[r.Equipment.QuoteGroup()] = r.Quote
		}
		defs := make(map[uint32]storage.CraftEntry, len(base.Definitions)+len(missing))
		maps.Copy(defs, base.Definitions)
		base.Quotes = slices.Clone(base.Quotes)
		for _, id := range missing {
			r := extra.Definitions[id]
			if r.Quote < 0 || r.Quote >= len(extra.Quotes) {
				return storage.CraftCatalog{}, fmt.Errorf("invalid additional crafting quote for %d", id)
			}
			k := r.Equipment.QuoteGroup()
			index, seen := groups[k]
			if !seen {
				index = len(base.Quotes)
				groups[k] = index
				base.Quotes = append(base.Quotes, extra.Quotes[r.Quote])
			}
			r.Quote = index
			defs[id] = r
		}
		base.Definitions = defs
	}
	base.Tickets = addResources(base.Tickets, extra.Tickets)
	base.Enchants = addResources(base.Enchants, extra.Enchants)
	base.Destruction = addResources(base.Destruction, extra.Destruction)
	base.Templates = addResources(base.Templates, extra.Templates)
	return base, nil
}

func mergeOwnedCommerce(base, extra storage.CommerceCatalog) (storage.CommerceCatalog, error) {
	var err error
	base.Craft, err = mergeCraft(base.Craft, extra.Craft)
	if err != nil {
		return storage.CommerceCatalog{}, err
	}
	base.Quality.Sources = addResources(base.Quality.Sources, extra.Quality.Sources)
	base.Quality.Targets = addResources(base.Quality.Targets, extra.Quality.Targets)
	base.Quality.Mythology = addResources(base.Quality.Mythology, extra.Quality.Mythology)
	base.Templates = addResources(base.Templates, extra.Templates)
	base.SalePrices = addResources(base.SalePrices, extra.SalePrices)
	base.Potions = addResources(base.Potions, extra.Potions)
	base.FatigueItems = addResources(base.FatigueItems, extra.FatigueItems)
	base.Discard = addResources(base.Discard, extra.Discard)
	base.Disassembly = addResources(base.Disassembly, extra.Disassembly)
	base.RepairDefinitions = addResources(base.RepairDefinitions, extra.RepairDefinitions)
	base.SortMetadata = addResources(base.SortMetadata, extra.SortMetadata)
	base.Gifts = addResources(base.Gifts, extra.Gifts)
	base.ContractGrants = addResources(base.ContractGrants, extra.ContractGrants)
	base.AvatarClones = addResources(base.AvatarClones, extra.AvatarClones)
	base.AvatarBoxes = addResources(base.AvatarBoxes, extra.AvatarBoxes)
	base.MagicSeal = addResources(base.MagicSeal, extra.MagicSeal)
	base.PetHatch = addResources(base.PetHatch, extra.PetHatch)
	base.PetNames = addResources(base.PetNames, extra.PetNames)
	base.Lottery = addResources(base.Lottery, extra.Lottery)
	base.LotteryTemplates = addResources(base.LotteryTemplates, extra.LotteryTemplates)
	base.KeyedBoxes = addResources(base.KeyedBoxes, extra.KeyedBoxes)
	base.LuckyBoxes = addResources(base.LuckyBoxes, extra.LuckyBoxes)
	base.BoxWeights = addResources(base.BoxWeights, extra.BoxWeights)
	base.DailyExpiryTemplates = addResources(base.DailyExpiryTemplates, extra.DailyExpiryTemplates)
	return base, nil
}
