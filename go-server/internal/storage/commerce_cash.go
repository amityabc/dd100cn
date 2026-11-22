package storage

import (
	"encoding/json"
	"fmt"
	"slices"
	"time"
)

type CashLine struct {
	Commodity, CouponTemplate            uint32
	CouponSlot                           uint16
	AvatarFlag, AbilityID, PurchaseModel byte
	Avatar                               bool
}

func (x *CommerceTx) BuyCash(lines []CashLine, catalog CommerceCatalog, now time.Time,
	grantSkillStyle func(Character) (json.RawMessage, error),
	grantBattlePass func(json.RawMessage, CashOffer) (json.RawMessage, error)) error {
	if len(lines) == 0 || len(lines) > 255 {
		return ErrInvalid
	}
	// Freeze coupon sources before granting any item from this cart.
	var cera, gold int64
	var slots int
	var battlePass json.RawMessage
	avatars := make([]AvatarCashOffer, len(lines))
	for i, line := range lines {
		if line.Avatar {
			offer, ok := catalog.AvatarOffers[line.Commodity]
			if !ok || !validCashAvatar(x.Character, line, offer) {
				return ErrInvalid
			}
			avatars[i] = offer
			cera += int64(offer.Cera)
			continue
		}
		offer, ok := catalog.Cash[line.Commodity]
		if !ok || offer.Commodity != line.Commodity || offer.Count == 0 {
			return ErrInvalid
		}
		if err := cashPayment(offer); err != nil {
			return err
		}
		if offer.CharacterSlot {
			slots++
		}
		if offer.BattlePassHash != "" {
			if grantBattlePass == nil {
				return ErrInvalid
			}
			if battlePass == nil {
				var err error
				battlePass, err = battlePassState(x.ctx, x.tx, x.account)
				if err != nil {
					return err
				}
			}
			var err error
			battlePass, err = grantBattlePass(battlePass, offer)
			if err != nil {
				return err
			}
		}
		if offer.capacity() && len(lines) != 1 {
			return ErrInvalid
		}
		if err := x.cashCoupon(line, offer, now); err != nil {
			return err
		}
		for _, a := range x.before.Items {
			if (a.Location == 0 || a.Location == 2) && slices.Contains(offer.OwnedItemExclusions, a.TemplateID) {
				return ErrInvalid
			}
		}
		cera += int64(offer.Cera)
		gold += int64(offer.Gold)
	}
	if slots > 1 {
		return ErrInvalid
	}
	if cera > x.Inventory.Cera {
		if len(lines) == 1 && !lines[0].Avatar {
			offer := catalog.Cash[lines[0].Commodity]
			if offer.capacity() && offer.TargetBagExpansion == 0 {
				return CommerceRejection(20)
			}
		}
		return CommerceRejection(11)
	}
	if gold > x.Inventory.Gold {
		return CommerceRejection(20)
	}
	x.Inventory.Cera -= cera
	x.Inventory.Gold -= gold
	for i, line := range lines {
		if line.Avatar {
			if err := x.grantSpecial(avatars[i].Template, uint16(line.AbilityID), "", now, true); err != nil {
				return err
			}
			continue
		}
		o := catalog.Cash[line.Commodity]
		if o.capacity() {
			if len(lines) != 1 || o.Count != 1 || o.Cera == 0 {
				return ErrInvalid
			}
			return x.cashCapacity(o, catalog.AccountCargo)
		}
		if o.NameTag != nil {
			if len(lines) != 1 {
				return ErrInvalid
			}
			return x.buyNameTag(o, line, now)
		}
		if o.AccountRestriction != 0 {
			if err := x.updateCommercePurchase("", o.AccountRestriction, 1, o.AccountLimit, o.AccountPeriod, now); err != nil {
				return err
			}
		}
		if o.CharacterRestriction != 0 {
			if o.AccountRestriction != 0 {
				return ErrInvalid
			}
			if err := x.updateCommercePurchase("CharacterCashPurchases", o.CharacterRestriction, 1, o.CharacterDailyLimit, o.CharacterPeriod, now); err != nil {
				return err
			}
		}
		if o.PremiumType != 0 {
			if err := x.buyContract(o, now); err != nil {
				return err
			}
			continue
		}
		if o.SkillStyleHash != "" {
			if o.Commodity != 100494 || o.TemplateID != 821 || o.Count != 1 || o.Cera != 5800 || o.Gold != 0 ||
				o.SkillStyleHash != "AD89AC933FCCD2B9A780019D9E185D532F4F7594EA32A7676538DF65F6616FA1" || grantSkillStyle == nil {
				return ErrInvalid
			}
			profile, err := grantSkillStyle(x.Character)
			if err != nil {
				return err
			}
			if !json.Valid(profile) {
				return ErrInvalid
			}
			x.Character.Gameplay = profile
			continue
		}
		if o.CharacterSlot {
			if err := x.buyCharacterSlot(); err != nil {
				return err
			}
			continue
		}
		if o.BattlePassHash != "" {
			continue
		}
		if o.RevivalCoins {
			if o.TemplateID != 1 {
				return ErrInvalid
			}
			x.Inventory.Coins += int64(o.Count)
			continue
		}
		if hasEffect(o.PetTemplate) {
			var t specialPurchaseTemplate
			if o.Count != 1 || json.Unmarshal(o.PetTemplate, &t) != nil || t.Kind != 1 || t.TemplateID != o.TemplateID {
				return ErrInvalid
			}
			if err := x.grantSpecial(o.PetTemplate, 0, "", now, true); err != nil {
				return err
			}
			continue
		}
		var t ordinaryTemplate
		var d materialDefinition
		if json.Unmarshal(o.Template, &t) != nil || t.TemplateID != o.TemplateID || t.Kind != 2 ||
			t.MinimumLevel > x.Character.Level || t.UsableJobs&(1<<x.Character.Job) == 0 {
			return ErrInvalid
		}
		if o.NativeFreePointOnly && (json.Unmarshal(o.Template, &d) != nil || !d.PvfGiftItem || d.CashPackage) {
			return ErrInvalid
		}
		if err := x.grantCommerceItem(o.Template, o.Count, now, true); err != nil {
			return err
		}
		for _, bonus := range o.BonusItems {
			if err := x.grantCommerceItem(catalog.Templates[bonus.TemplateID], bonus.Count, now, true); err != nil {
				return err
			}
		}
	}
	if battlePass != nil {
		if _, err := writeBattlePassState(x.ctx, x.tx, x.account, battlePass, false); err != nil {
			return err
		}
		x.Inventory.AssetRevision++
	}
	return nil
}

func (o CashOffer) capacity() bool {
	return o.TargetBagExpansion != 0 || o.TargetAccountCargoCapacity != 0 ||
		o.TargetPersonalCargoCapacity != 0 || o.TargetAvatarExpansion != 0
}

func cashPayment(offer CashOffer) error {
	regularDeliverable := !offer.capacity() && offer.NameTag == nil && !offer.RevivalCoins && !hasEffect(offer.PetTemplate) &&
		len(offer.BonusItems) == 0 && len(offer.RequiredCouponTemplates) == 0 && len(offer.OwnedItemExclusions) == 0
	if offer.NativeFreePointOnly {
		if offer.Cera != 0 || offer.Gold != 0 || !regularDeliverable || offer.AccountRestriction == 0 ||
			offer.AccountRestriction > 2147483647 || offer.AccountLimit == 0 || offer.AccountLimit > 255 ||
			offer.AccountPeriod != 101 || offer.CharacterRestriction != 0 || offer.CharacterDailyLimit != 0 {
			return ErrInvalid
		}
	} else if offer.Cera == 0 && offer.Gold == 0 || offer.Cera != 0 && offer.Gold != 0 {
		return ErrInvalid
	}
	if offer.Gold != 0 && (!regularDeliverable || offer.AccountRestriction != 0 || offer.AccountLimit != 0 ||
		offer.CharacterRestriction != 0 || offer.CharacterDailyLimit != 0) {
		return ErrInvalid
	}
	return nil
}

func (x *CommerceTx) buyContract(o CashOffer, now time.Time) error {
	if o.PremiumType >= 100 {
		return x.extendCharacterService(o, now)
	}
	if o.Count != 1 || o.PremiumSeconds != 7*86400 || o.PremiumType != 22 && o.PremiumType != 27 && o.PremiumType != 97 {
		return ErrInvalid
	}
	return x.extendPremium(o.PremiumType, int64(o.PremiumSeconds), now)
}

func (x *CommerceTx) extendPremium(kind byte, seconds int64, now time.Time) error {
	field := map[byte]string{22: "conqueror_expires_at", 27: "skill_master_expires_at", 97: "crystal_expires_at", 84: "growth_expires_at"}[kind]
	if field == "" || seconds <= 0 || seconds > 2147483647 {
		return ErrInvalid
	}
	q := fmt.Sprintf("UPDATE s9a1.accounts SET %[1]s=greatest(%[1]s,$2)+$3 WHERE account=$1 AND greatest(%[1]s,$2)+$3<=253402300799", field)
	res, err := x.tx.ExecContext(x.ctx, q, x.account, now.Unix(), seconds)
	if err != nil {
		return err
	}
	if n, err := res.RowsAffected(); err != nil || n != 1 {
		return ErrInvalid
	}
	x.Inventory.AssetRevision++
	return nil
}

func (x *CommerceTx) buyCharacterSlot() error {
	if x.slotsPurchased || x.accountCapacity >= 100 {
		return ErrInvalid
	}
	_, err := x.tx.ExecContext(x.ctx, "UPDATE s9a1.accounts SET capacity=capacity+1 WHERE account=$1", x.account)
	if err == nil {
		x.slotsPurchased = true
		x.Inventory.AssetRevision++
	}
	return err
}

func (x *CommerceTx) cashCoupon(line CashLine, o CashOffer, now time.Time) error {
	if len(o.RequiredCouponTemplates) == 0 {
		if line.CouponTemplate != 0 || line.CouponSlot != 0 {
			return ErrInvalid
		}
		return nil
	}
	frozen, ok := o.RequiredCouponTemplates[line.CouponTemplate]
	if !ok {
		return ErrInvalid
	}
	i := slices.IndexFunc(x.Inventory.Items, func(a Asset) bool {
		return a.CharacterID == x.Character.ID && a.Location == 0 && a.Slot == line.CouponSlot && a.TemplateID == line.CouponTemplate && a.Count > 0
	})
	if i < 0 {
		return ErrInvalid
	}
	a := x.Inventory.Items[i]
	attr, err := movementAttributes(&a)
	if err != nil {
		return err
	}
	var t ordinaryTemplate
	var d materialDefinition
	if json.Unmarshal(frozen, &t) != nil || json.Unmarshal(a.Template, &d) != nil || t.Kind != 2 ||
		t.TemplateID != a.TemplateID || t.FirstBagSlot != 65 || t.LastBagSlot != 120 ||
		line.CouponSlot > 8 && line.CouponSlot > t.bagEnd(x.Inventory.BagExpansion) ||
		!sameCommerceTemplate(frozen, a.Template) || attr.Locked || attr.ExpiresAt != nil && *attr.ExpiresAt <= now.Unix() ||
		d.ResourceExpiration != nil && *d.ResourceExpiration <= now.Unix() || t.MinimumLevel > x.Character.Level ||
		t.UsableJobs&(1<<x.Character.Job) == 0 {
		return ErrInvalid
	}
	plain, err := plainMaterial(a, d)
	if err != nil {
		return err
	}
	if !plain {
		return ErrInvalid
	}
	return x.debitAsset(i, 1)
}

func (x *CommerceTx) cashCapacity(o CashOffer, catalog CargoCatalog) error {
	switch {
	case o.TargetAvatarExpansion != 0:
		return x.expandAvatars(o.TargetAvatarExpansion)
	case o.TargetBagExpansion != 0:
		if o.TargetBagExpansion != x.Inventory.BagExpansion+8 || o.TargetBagExpansion > 24 {
			return ErrInvalid
		}
		x.Inventory.BagExpansion = o.TargetBagExpansion
	case o.TargetPersonalCargoCapacity != 0:
		if o.PreviousPersonalCargoCapacity != x.Inventory.PersonalCapacity || o.TargetPersonalCargoCapacity > 264 ||
			o.TargetPersonalCargoCapacity != o.PreviousPersonalCargoCapacity+16 {
			return ErrInvalid
		}
		x.Inventory.PersonalCapacity = o.TargetPersonalCargoCapacity
	case o.TargetAccountCargoCapacity != 0:
		i := slices.IndexFunc(catalog.Stages, func(s CargoStage) bool {
			return s.Previous == x.Inventory.CargoCapacity && s.Capacity == o.TargetAccountCargoCapacity && int64(s.Commodity) == int64(o.Commodity)
		})
		if i < 0 {
			return ErrInvalid
		}
		x.Inventory.CargoCapacity = o.TargetAccountCargoCapacity
	}
	return nil
}
