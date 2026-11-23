package storage

import (
	"encoding/json"
	"errors"
	"math"
	"slices"
	"time"
)

type CommerceSale struct {
	Slot  uint16
	Count uint32
}

var (
	ErrSaleMissing      = errors.New("sale source is absent")
	ErrSaleUnavailable  = errors.New("sale product has no supported price")
	ErrSaleAssetChanged = errors.New("sale source quantity, lock, or expiry changed")
)

func (x *CommerceTx) Sell(rows []CommerceSale, prices map[uint32]uint32) error {
	now := time.Now().Unix()
	for _, line := range rows {
		i := slices.IndexFunc(x.Inventory.Items, func(a Asset) bool { return a.Location == 0 && a.Slot == line.Slot && a.Count > 0 })
		if i < 0 {
			return ErrSaleMissing
		}
		a := x.Inventory.Items[i]
		t, err := moveTemplate(&a)
		if a.Source == "shared" {
			err = json.Unmarshal(a.Template, &t)
		}
		if err != nil || t.Kind != 2 && line.Count != 1 {
			return ErrInvalid
		}
		attr, err := movementAttributes(&a)
		if err != nil {
			return err
		}
		if line.Count > a.Count || attr.Locked || attr.ExpiresAt != nil && *attr.ExpiresAt <= now {
			return ErrSaleAssetChanged
		}
		price, ok := prices[a.TemplateID]
		if !ok {
			return ErrSaleUnavailable
		}
		credit := uint64(price) * uint64(line.Count)
		if credit > uint64(math.MaxUint32-x.Inventory.Gold) {
			return ErrFunds
		}
		if err = x.debitAsset(i, line.Count); err != nil {
			return err
		}
		x.Inventory.Gold += int64(credit)
	}
	return nil
}

func (x *CommerceTx) BuyNPC(offer CommerceProduct, quantity uint32, catalog CommerceCatalog, now time.Time) error {
	var t ordinaryTemplate
	if json.Unmarshal(offer.Template, &t) != nil || t.TemplateID != offer.TemplateID || (t.Kind != 2) != offer.Equipment {
		return ErrInvalid
	}
	if offer.Equipment && quantity == 0 {
		quantity = 1
	}
	if quantity == 0 || quantity > math.MaxUint16 || offer.Equipment && quantity != 1 || x.Character.Level < max(t.MinimumLevel, offer.MinimumLevel) ||
		t.UsableJobs&(1<<x.Character.Job) == 0 || offer.Gold == 0 && len(offer.Materials) == 0 {
		return ErrInvalid
	}
	cost := int64(offer.Gold) * int64(quantity)
	if x.Inventory.Gold < cost {
		return ErrFunds
	}
	x.Inventory.Gold -= cost
	for _, quota := range offer.Limits {
		if quota.Legacy || quota.Count == 0 || quota.Count > math.MaxInt32 {
			return ErrInvalid
		}
		field, id := "NpcPurchases", offer.TemplateID
		if quota.Account {
			field, id = "", id|0x80000000
		}
		if err := x.updateCommercePurchase(field, id, quantity, quota.Count, quota.Period, now); err != nil {
			return err
		}
	}
	for _, m := range offer.Materials {
		if err := x.spendNPCMaterial(m, catalog.Templates[m.TemplateID], quantity, now); err != nil {
			return err
		}
	}
	return x.grantCommerceItem(offer.Template, quantity, now, true)
}

func (offer CommerceProduct) DPLLifetime() bool {
	if offer.TemplateID != 490016725 && offer.TemplateID != 490016727 {
		return false
	}
	var d materialDefinition
	expiry := time.Date(2030, 11, 30, 6, 0, 0, 0, commerceZone).Unix()
	return offer.NPC == 2 && offer.Shop == 15 && offer.Gold == 1 && !offer.Equipment && len(offer.Materials) == 0 &&
		json.Unmarshal(offer.Template, &d) == nil && d.Kind == 2 && d.PvfGiftItem && d.ResourceExpiration != nil && *d.ResourceExpiration == expiry &&
		d.RelativeLifetimeSeconds == 0 && !d.DailyDeletion && d.WeeklyDeletionDay == nil
}

func (x *CommerceTx) spendNPCMaterial(m CommercePayment, template json.RawMessage, quantity uint32, now time.Time) error {
	var mt ordinaryTemplate
	if json.Unmarshal(template, &mt) != nil || mt.TemplateID != m.TemplateID || mt.Kind != 2 || m.Count == 0 {
		return ErrInvalid
	}
	location := byte(0)
	if mt.AccountCommonInventory {
		location = 45
	}
	left := uint64(m.Count) * uint64(quantity)
	for i, a := range x.Inventory.Items {
		if left == 0 {
			break
		}
		if a.Location != location || a.TemplateID != m.TemplateID || a.Count == 0 {
			continue
		}
		attr, err := movementAttributes(&a)
		if err != nil {
			return err
		}
		if attr.Locked || attr.ExpiresAt != nil && *attr.ExpiresAt <= now.Unix() {
			continue
		}
		var def materialDefinition
		if err = json.Unmarshal(a.Template, &def); err != nil {
			return err
		}
		aTemplate, err := frozenMaterialObject(a.Template, materialTemplateDefaults)
		if err != nil {
			return err
		}
		full, err := json.Marshal(aTemplate)
		if err != nil {
			return err
		}
		if !equalJSON(template, full) {
			continue
		}
		plain, err := plainMaterial(a, def)
		if err != nil {
			return err
		}
		if !plain {
			continue
		}
		take := uint32(min(left, uint64(a.Count)))
		if err = x.debitAsset(i, take); err != nil {
			return err
		}
		left -= uint64(take)
	}
	if left != 0 {
		return ErrFunds
	}
	return nil
}

func (x *CommerceTx) CheckCarry(limits map[byte]int64, weights map[uint32]int64) error {
	if x.Inventory.Gold > x.before.Gold {
		limit, err := MailGoldLimit(x.Character, limits)
		if err != nil {
			return err
		}
		if x.Inventory.Gold > limit {
			return ErrFunds
		}
	}
	deltas := map[uint32]int64{}
	for _, a := range x.before.Items {
		if a.Location == 0 && a.CharacterID == x.Character.ID {
			deltas[a.TemplateID] -= int64(a.Count)
		}
	}
	for _, a := range x.Inventory.Items {
		if a.Location == 0 && a.CharacterID == x.Character.ID {
			deltas[a.TemplateID] += int64(a.Count)
		}
	}
	return checkBagWeight(x.Character, x.before, deltas, weights)
}
