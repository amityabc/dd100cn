package storage

import (
	"encoding/json"
	"errors"
	"time"
)

var ErrSkinTicketMissing = errors.New("skin unlock ticket is absent")

func (x *CommerceTx) UnlockSkinSlot(source, target uint16, cat AvatarEditCatalog, now time.Time) error {
	var profile struct{ AuraSkinUnlocked, PetSkinUnlocked bool }
	if !x.livingEmblemUser(now) || json.Unmarshal(x.Character.Gameplay, &profile) != nil ||
		target != 11 && target != 33 || target == 11 && profile.AuraSkinUnlocked || target == 33 && profile.PetSkinUnlocked {
		return ErrInvalid
	}
	if source == 65535 {
		return ErrSkinTicketMissing
	}
	i, t, err := x.emblemBag(source, now)
	if err != nil {
		return err
	}
	field := "AuraSkinUnlocked"
	if target == 11 {
		if t.TemplateID != 490700411 || t.FirstBagSlot != 121 || t.LastBagSlot != 176 {
			return ErrInvalid
		}
	} else {
		field = "PetSkinUnlocked"
		if t.TemplateID != 490020769 && t.TemplateID != 490022060 && t.TemplateID != 490703887 {
			return ErrInvalid
		}
	}
	if err = x.eligibleAvatarCatalyst(i, cat.SkinTickets[t.TemplateID], now); err != nil {
		return err
	}
	if err = x.debitAsset(i, 1); err != nil {
		return err
	}
	return replaceMailFields(&x.Character, map[string]any{field: true})
}
