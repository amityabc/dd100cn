package storage

import (
	"encoding/json"
	"slices"
)

func QuestOrdinarySource(a Asset, template json.RawMessage) bool {
	var t materialDefinition
	if json.Unmarshal(template, &t) != nil || t.TemplateID != a.TemplateID {
		return false
	}
	plain, shape := plainMaterial(a, t)
	return shape == nil && plain && sameCommerceTemplate(template, a.Template)
}

func QuestConsumeInstance(in Inventory, uid string) (Inventory, error) {
	i := slices.IndexFunc(in.Items, func(a Asset) bool { return a.UID == uid && a.Count > 0 })
	if i < 0 {
		return in, ErrInvalid
	}
	in.Items = slices.Clone(in.Items)
	in.Items[i].Count--
	if in.Items[i].Count == 0 {
		in.Items = slices.Delete(in.Items, i, i+1)
	}
	return in, nil
}

func QuestTeleportSource(a Asset, template json.RawMessage, in Inventory, c Character, now int64) bool {
	var t ordinaryTemplate
	return a.CharacterID == c.ID && a.Location == 0 && a.Source == "item" && a.Count > 0 &&
		validEmblemID(a.UID) && accessibleEmblem(a.Attributes, now) && json.Unmarshal(a.Template, &t) == nil &&
		t.Kind == 2 && t.TemplateID == a.TemplateID && t.FirstBagSlot == 65 &&
		(a.Slot >= 3 && a.Slot <= 8 && !t.AccountCommonInventory && !t.FateCollection && !t.PetConsumable ||
			a.Slot >= 65 && a.Slot <= t.bagEnd(in.BagExpansion)) &&
		c.Level >= t.MinimumLevel && c.Job < 16 && t.UsableJobs&(1<<c.Job) != 0 && sameCommerceTemplate(template, a.Template)
}
