package storage

import (
	"context"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"slices"
	"time"
)

type FateExchangeCatalog struct {
	Shops                    []FateExchangeShop
	Offers                   map[uint32]FateExchangeOffer
	ResourceHash             string
	ArchivedEventReactivated bool
}

type FateExchangeShop struct {
	NPC        uint32 `json:"Npc"`
	Shop       uint32
	Town, Area uint16
}

type FateExchangeOffer struct {
	Product, Material json.RawMessage
	Cost              uint32
	Period            byte
	SourceExpiration  string
}

type FateExchangeRequest struct {
	NPC, Shop, Template, Count uint32
	Sequence                   uint16
}

type FateExchangeReceipt struct {
	EventID, ProductID, Purchased      uint32
	Period                             byte
	PeriodKey                          string
	BuyerCharacterID                   uint16
	AccountRevision, CharacterRevision int64
	Key, Input, Result, Grants         string
}

type fateExchangeInput struct {
	Offer        FateExchangeOffer
	Shop         FateExchangeShop
	Request      FateExchangeRequest
	PeriodKey    string
	PlannedAt    int64
	ResourceHash string
}

type fateExchangeOutput struct {
	Template    json.RawMessage
	Count, Cost uint32
	Material    json.RawMessage
}

type fateExchangeGrant struct {
	InstanceID string
	TemplateID uint32
	Location   byte
	Slot       uint16
	Granted    uint32
}

var fateExchangeTerms = map[uint32]struct {
	Cost   uint32
	Period byte
}{
	490023504: {300, 0}, 490023506: {15, 1}, 490023507: {60, 2}, 490023509: {200, 0}, 490023510: {20, 2}, 490023514: {200, 0}, 490023515: {30, 0},
}

func IsFateExchangeNPC(npc uint32) bool { return npc == 400001285 || npc == 400001286 }

func fateExchangeKey(period byte, now time.Time) string {
	local := now.UTC().Add(2 * time.Hour)
	switch period {
	case 1:
		return local.Format("20060102")
	case 2:
		days := (int(local.Weekday()) - int(time.Thursday) + 7) % 7
		return local.AddDate(0, 0, -days).Format("20060102")
	default:
		return ""
	}
}

func fateExchangeHash(hash string) bool {
	b, err := hex.DecodeString(hash)
	return err == nil && len(b) == 32
}

func validFateExchangeOffer(id uint32, o FateExchangeOffer) bool {
	want, ok := fateExchangeTerms[id]
	var p, m ordinaryTemplate
	return ok && want.Cost == o.Cost && want.Period == o.Period && o.SourceExpiration == "2030-11-11 06:00:00" &&
		json.Unmarshal(o.Product, &p) == nil && json.Unmarshal(o.Material, &m) == nil &&
		fateExchangeHash(p.ResourceHash) && fateExchangeHash(m.ResourceHash) && p.TemplateID == id && p.Kind == 2 &&
		!p.AccountCommonInventory && !p.FateCollection && p.FirstBagSlot == 65 && p.LastBagSlot == 120 && p.MinimumLevel == 1 && p.UsableJobs == 65535 &&
		m.TemplateID == 490023498 && m.AccountCommonInventory && m.Kind == 2 && !m.FateCollection && m.MinimumLevel == 1
}

func validFateExchangeShop(shop FateExchangeShop) bool {
	return shop == (FateExchangeShop{400001285, 400000197, 38, 1}) ||
		shop == (FateExchangeShop{400001286, 400000199, 135, 0})
}

func InsertFateExchangeReceipt(ctx context.Context, tx *sql.Tx, account string, r FateExchangeReceipt) error {
	want, ok := fateExchangeTerms[r.ProductID]
	var input fateExchangeInput
	var result fateExchangeOutput
	var grants []fateExchangeGrant
	key := fmt.Sprintf("fate-exchange:4171:%d:%d:%s", r.ProductID, r.Period, r.PeriodKey)
	if !ok || r.EventID != 4171 || r.Purchased != 1 || r.Period != want.Period || r.BuyerCharacterID == 0 || r.AccountRevision <= 0 ||
		r.CharacterRevision <= 0 || r.Key != key || json.Unmarshal([]byte(r.Input), &input) != nil || json.Unmarshal([]byte(r.Result), &result) != nil ||
		json.Unmarshal([]byte(r.Grants), &grants) != nil || len(grants) != 1 ||
		!validFateExchangeOffer(r.ProductID, input.Offer) || !validFateExchangeShop(input.Shop) || !fateExchangeHash(input.ResourceHash) ||
		input.Request.Count != 1 || input.Request.Template != r.ProductID || input.Request.NPC != input.Shop.NPC || input.Request.Shop != input.Shop.Shop ||
		input.PeriodKey != r.PeriodKey || input.PlannedAt <= 0 || fateExchangeKey(r.Period, time.Unix(input.PlannedAt, 0)) != r.PeriodKey ||
		result.Count != 1 || result.Cost != input.Offer.Cost || !equalJSON(result.Template, input.Offer.Product) || !equalJSON(result.Material, input.Offer.Material) {
		return ErrInvalid
	}
	if r.Period != 0 {
		if date, err := time.Parse("20060102", r.PeriodKey); err != nil || date.Year() == 0 {
			return ErrInvalid
		}
	}
	grant := grants[0]
	if !validAssetUUID(grant.InstanceID) || grant.Location != 0 || grant.Granted != 1 || grant.TemplateID != r.ProductID ||
		(grant.Slot < 3 || grant.Slot > 8) && (grant.Slot < 65 || grant.Slot > 120) {
		return ErrInvalid
	}
	_, err := tx.ExecContext(ctx, `INSERT INTO s9a1.fate_exchange_purchases (account,event_id,product_id,period,period_key,purchased,
		buyer_character_id,account_revision,character_revision,receipt_key,receipt_input_json,receipt_result_json,receipt_grants_json)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13)`, account, r.EventID, r.ProductID, r.Period, r.PeriodKey, r.Purchased,
		r.BuyerCharacterID, r.AccountRevision, r.CharacterRevision, r.Key, r.Input, r.Result, r.Grants)
	return err
}

func validAssetUUID(id string) bool {
	if len(id) != 36 || id[8] != '-' || id[13] != '-' || id[18] != '-' || id[23] != '-' {
		return false
	}
	raw := id[:8] + id[9:13] + id[14:18] + id[19:23] + id[24:]
	_, err := hex.DecodeString(raw)
	return err == nil && raw != "00000000000000000000000000000000"
}

func (d *FateExchangeCatalog) Shop(c Character, npc uint32) (FateExchangeShop, bool) {
	if d == nil || !d.ArchivedEventReactivated || !fateExchangeHash(d.ResourceHash) || len(d.Shops) != 2 ||
		len(d.Offers) != len(fateExchangeTerms) || c.Level < 100 {
		return FateExchangeShop{}, false
	}
	for _, row := range d.Shops {
		if row.NPC == npc && row.Town == c.Position.Town && row.Area == c.Position.Area && validFateExchangeShop(row) {
			return row, true
		}
	}
	return FateExchangeShop{}, false
}

func (x *CommerceTx) BuyFateExchange(q FateExchangeRequest, d *FateExchangeCatalog, now time.Time) error {
	s, ok := d.Shop(x.Character, q.NPC)
	if !ok || q.Count != 1 || s.Shop != q.Shop {
		return ErrInvalid
	}
	o, ok := d.Offers[q.Template]
	if !ok || !validFateExchangeOffer(q.Template, o) {
		return ErrInvalid
	}
	period := fateExchangeKey(o.Period, now)
	var old sql.NullString
	err := x.tx.QueryRowContext(x.ctx, "SELECT max(period_key) FROM s9a1.fate_exchange_purchases WHERE account=$1 AND product_id=$2", x.account, q.Template).Scan(&old)
	if err != nil {
		return err
	}
	if old.Valid && old.String >= period {
		return ErrInvalid
	}
	left := o.Cost
	for i, a := range x.Inventory.Items {
		if a.CharacterID != 0 || a.Location != 45 || a.TemplateID != 490023498 || a.Count == 0 || !sameCommerceTemplate(o.Material, a.Template) {
			continue
		}
		var t materialDefinition
		var v materialAttributes
		if json.Unmarshal(a.Template, &t) != nil || json.Unmarshal(a.Attributes, &v) != nil || v.ExpiresAt != nil && *v.ExpiresAt <= now.Unix() {
			continue
		}
		plain, err := plainMaterial(a, t)
		if err != nil {
			return err
		}
		if !plain {
			continue
		}
		n := min(left, a.Count)
		if err = x.debitAsset(i, n); err != nil {
			return err
		}
		left -= n
		if left == 0 {
			break
		}
	}
	if left != 0 {
		return ErrFunds
	}
	if err = x.grantCommerceItem(o.Product, 1, now, true); err != nil {
		return err
	}
	if len(x.Grants) != 1 || x.Granted.Location != 0 || x.Granted.Count != 1 || fateExchangeKey(o.Period, time.Now()) != period {
		return ErrInvalid
	}
	if err = x.recordFateExchange(q, d, s, o, period, now); err != nil {
		return err
	}
	return nil
}

func (x *CommerceTx) recordFateExchange(q FateExchangeRequest, d *FateExchangeCatalog, s FateExchangeShop, o FateExchangeOffer, period string, now time.Time) error {
	input, err := json.Marshal(fateExchangeInput{o, s, q, period, now.Unix(), d.ResourceHash})
	if err != nil {
		return err
	}
	output, err := json.Marshal(fateExchangeOutput{o.Product, 1, o.Cost, o.Material})
	if err != nil {
		return err
	}
	reward, err := json.Marshal([]fateExchangeGrant{{materialGUID(x.Granted.UID), x.Granted.TemplateID, x.Granted.Location, x.Granted.Slot, 1}})
	if err != nil {
		return err
	}
	return InsertFateExchangeReceipt(x.ctx, x.tx, x.account, FateExchangeReceipt{
		EventID: 4171, ProductID: q.Template, Purchased: 1, Period: o.Period, PeriodKey: period, BuyerCharacterID: x.Character.ID,
		AccountRevision: x.before.AssetRevision + 1, CharacterRevision: x.Character.Revision + 1,
		Key: fmt.Sprintf("fate-exchange:4171:%d:%d:%s", q.Template, o.Period, period), Input: string(input), Result: string(output), Grants: string(reward),
	})
}

func (s *Store) FateExchangeCounts(ctx context.Context, l Lease, d *FateExchangeCatalog, npc uint32, now time.Time) (map[uint32]uint32, error) {
	return writeTx(ctx, s.db, func(tx *sql.Tx) (map[uint32]uint32, error) {
		r, err := lockAccount(ctx, tx, l, false)
		if err != nil {
			return nil, err
		}
		if r.Phase != TownReady || r.SelectedID == 0 {
			return nil, ErrPhase
		}
		c, err := mailCharacter(ctx, tx, l.Account, r.SelectedID)
		if err != nil {
			return nil, err
		}
		if _, ok := d.Shop(c, npc); !ok {
			return nil, ErrPhase
		}
		counts := map[uint32]uint32{}
		for id, offer := range d.Offers {
			if !validFateExchangeOffer(id, offer) {
				return nil, ErrInvalid
			}
			counts[id] = 0
		}
		rows, err := tx.QueryContext(ctx, "SELECT product_id,max(period_key) FROM s9a1.fate_exchange_purchases WHERE account=$1 GROUP BY product_id", l.Account)
		if err != nil {
			return nil, err
		}
		defer rows.Close()
		for rows.Next() {
			var id uint32
			var period string
			if err = rows.Scan(&id, &period); err != nil {
				return nil, err
			}
			if o, ok := d.Offers[id]; ok && period >= fateExchangeKey(o.Period, now) {
				counts[id] = 1
			}
		}
		return counts, rows.Err()
	})
}

func FateExchangeProducts() []uint32 {
	out := make([]uint32, 0, len(fateExchangeTerms))
	for id := range fateExchangeTerms {
		out = append(out, id)
	}
	slices.Sort(out)
	return out
}
