package commerce

import (
	"encoding/json"
	"strings"

	"s9a1/server/internal/catalogprep/gm"
	"s9a1/server/internal/storage"
)

// RegisterGmMailItem prepares the effects of LocalPvfItemTemplates.RegisterGiftTemplates.
// Sort metadata is also frozen here, as the Go sort request reads the current catalog.
func RegisterGmMailItem(items *Items, src ItemSource, raw json.RawMessage) (storage.CommerceCatalog, error) {
	var out storage.CommerceCatalog
	var t ItemTemplate
	if err := json.Unmarshal(raw, &t); err != nil {
		return out, err
	}
	if t.TemplateId != src.Id || t.ResourceHash != src.Hash {
		return out, storage.ErrInvalid
	}
	id := src.Id
	out.Templates = map[uint32]json.RawMessage{id: raw}
	if t.Kind == 2 && t.DailyDeletion {
		out.DailyExpiryTemplates = map[uint32]json.RawMessage{id: raw}
	}
	if discard, ok, err := discardPolicy(src, &t); err != nil {
		return out, err
	} else if ok {
		out.Discard = map[uint32]storage.DiscardPolicy{id: discard}
	}
	fields, err := gm.ItemFields(src.Text)
	if err == nil {
		if sort, err := sortMetadata(src, fields); err == nil {
			out.SortMetadata, err = ownedConvertedRows(map[uint32]SortMetadata{id: sort}, out.SortMetadata)
			if err != nil {
				return out, err
			}
		}
		if t.Kind == 2 && t.FirstBagSlot == 65 && t.LastBagSlot == 120 {
			script := consumableScript{fields}
			if potion, err := parsePotion(src, &t, script); err == nil {
				out.Potions = map[uint32]storage.PotionDefinition{id: potion}
			}
			if strings.Contains(src.Text, "[add fatigue]") {
				if fatigue, err := parseFatigueItem(src, &t, script); err == nil {
					out.FatigueItems = map[uint32]storage.FatigueItemDefinition{id: fatigue}
				}
			}
		}
	}
	if t.Kind != 2 {
		out.MagicSeal, err = BuildMagicSeal(items, []uint32{id})
		return out, err
	}
	return out, nil
}
