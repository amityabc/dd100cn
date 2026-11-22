package inventorywire

import (
	"bytes"
	"encoding/json"
	"math"
	"time"

	"s9a1/server/internal/protocol"
)

// SpecialActor uses the same frozen details as the inventory view, while
// keeping creatures separate from the ordinary appearance equipment list.
func SpecialActor(data json.RawMessage, actor *protocol.Actor, now time.Time) error {
	var p struct{ SpecialAssets specialState }
	if err := json.Unmarshal(data, &p); err != nil {
		return err
	}
	for _, item := range p.SpecialAssets.Items {
		if item.Location != 3 {
			continue
		}
		if _, err := item.view(Options{Now: now}); err != nil {
			return err
		}
		if item.Template.Kind == 1 {
			v := item.Pet
			seconds := int32(-1)
			if attr := item.Attributes; attr.ExpiresAt != nil {
				seconds = int32(max(0, min(math.MaxInt32, *attr.ExpiresAt-now.Unix())))
			}
			creature := &protocol.ActorCreature{TemplateID: item.Template.TemplateID, NativeSerial: v.NativeSerial,
				Name: v.Name, Level: v.Level, Satiety: v.Satiety, ModeFlag: v.ModeFlag, TailFlag: v.TailFlag,
				TownDisplay: seconds != 0, Core119: bytes.Clone(item.Core153[:119]), RemainingSeconds: seconds}
			switch item.Slot {
			case 26:
				actor.Pet = creature
			case 33:
				actor.PetSkin = creature
			}
			continue
		}
		attr := item.Attributes
		e := protocol.Equipment{Slot: byte(item.Slot), TemplateID: item.Template.TemplateID, QualitySeed: attr.QualitySeed,
			Durability: attr.Durability, Raw119: bytes.Clone(item.Core153[:119]), Reinforcement: attr.Reinforcement,
			Forge: attr.Forge, AmplificationType: attr.AmplificationType, AmplificationValue: attr.AmplificationValue}
		if item.Avatar != nil {
			e.AvatarSockets, e.AvatarColors = item.Avatar.Sockets, item.Avatar.Colors
			if item.Template.WornSlot <= 7 && item.Avatar.Option != math.MaxUint32 {
				e.LinkedTemplateID = item.Avatar.Option
			}
		}
		actor.Worn = append(actor.Worn, e)
	}
	return nil
}
