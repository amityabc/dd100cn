package storage

import (
	"encoding/json"
	"time"
)

type ContainerCoupon struct {
	Template                            json.RawMessage
	Cargo, Village, Seria, ForbidScene8 bool
}

func (x *CommerceTx) ContainerCoupon(slot uint16, cargo bool, place string, catalog map[uint32]ContainerCoupon, now time.Time) error {
	i, _, _, _, err := x.giftSource(slot, now)
	if err != nil {
		return err
	}
	item := x.Inventory.Items[i]
	def, ok := catalog[item.TemplateID]
	if !ok || def.Cargo != cargo || !sameCommerceTemplate(def.Template, item.Template) ||
		!(def.Seria && place == "seria room" || def.Village && place == "village") {
		return ErrInvalid
	}
	if cargo {
		capacity := x.Inventory.PersonalCapacity
		if capacity < 8 || capacity >= 264 || (capacity-8)%16 != 0 {
			return ErrInvalid
		}
		x.Inventory.PersonalCapacity += 16
	} else {
		capacity := x.Inventory.BagExpansion
		if capacity >= 24 || capacity%8 != 0 {
			return ErrInvalid
		}
		x.Inventory.BagExpansion += 8
	}
	return x.debitAsset(i, 1)
}
