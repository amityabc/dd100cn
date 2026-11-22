package storage

import (
	"context"
	"database/sql"
	"encoding/json"
	"time"
)

type CommercePurchase struct {
	PeriodStart int64
	Count       uint32
}

var commerceZone = time.FixedZone("UTC+8", 8*3600)

func CommerceWindow(period int, now time.Time, previous CommercePurchase) (start, reset int64) {
	local := now.In(commerceZone)
	bound := time.Date(local.Year(), local.Month(), local.Day(), 6, 0, 0, 0, commerceZone)
	switch period {
	case 0:
		return 1, 0x7fff53f0
	case 1:
		if local.Before(bound) {
			bound = bound.AddDate(0, 0, -1)
		}
		return bound.Unix(), bound.AddDate(0, 0, 1).Unix()
	case 101:
		legacy := time.Unix(previous.PeriodStart, 0).In(commerceZone)
		if previous.PeriodStart > 0 && previous.PeriodStart <= now.Unix() && now.Unix() < previous.PeriodStart+7*86400 && legacy.Weekday() == time.Thursday && legacy.Hour() == 6 && legacy.Minute() == 0 && legacy.Second() == 0 {
			return previous.PeriodStart, previous.PeriodStart + 7*86400
		}
		bound = bound.AddDate(0, 0, -((int(bound.Weekday()) + 6) % 7))
		if local.Before(bound) {
			bound = bound.AddDate(0, 0, -7)
		}
		return bound.Unix(), bound.AddDate(0, 0, 7).Unix()
	case 102:
		bound = time.Date(local.Year(), local.Month(), 1, 6, 0, 0, 0, commerceZone)
		if local.Before(bound) {
			bound = bound.AddDate(0, -1, 0)
		}
		return bound.Unix(), bound.AddDate(0, 1, 0).Unix()
	}
	return 0, 0
}

func readCommercePurchases(ctx context.Context, tx *sql.Tx, account string) (map[uint32]CommercePurchase, error) {
	results := map[uint32]CommercePurchase{}
	rows, err := tx.QueryContext(ctx, `SELECT DISTINCT ON (restriction_id) restriction_id,period_start,count
		FROM s9a1.commerce_purchases WHERE account=$1 ORDER BY restriction_id,period_start DESC`, account)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var id uint32
		var p CommercePurchase
		if err = rows.Scan(&id, &p.PeriodStart, &p.Count); err != nil {
			return nil, err
		}
		results[id] = p
	}
	return results, rows.Err()
}

func (x *CommerceTx) updateCommercePurchase(field string, id, quantity, limit uint32, period byte, now time.Time) error {
	var purchases map[uint32]CommercePurchase
	if field == "" {
		purchases = x.Inventory.AccountPurchases
	} else {
		var profile map[string]json.RawMessage
		if err := json.Unmarshal(x.Character.Gameplay, &profile); err != nil {
			return err
		}
		purchases = map[uint32]CommercePurchase{}
		if old := profile[field]; len(old) > 0 {
			if err := json.Unmarshal(old, &purchases); err != nil {
				return err
			}
			if purchases == nil {
				return ErrInvalid
			}
		}
	}
	previous := purchases[id]
	start, _ := CommerceWindow(int(period), now, previous)
	if id == 0 || start <= 0 || previous.PeriodStart > start || quantity == 0 || quantity > limit {
		return ErrInvalid
	}
	var used uint32
	if previous.PeriodStart == start {
		used = previous.Count
	}
	if used > limit-quantity {
		return ErrInvalid
	}
	p := CommercePurchase{start, used + quantity}
	purchases[id] = p
	if field != "" {
		return replaceMailFields(&x.Character, map[string]any{field: purchases})
	}
	_, err := x.tx.ExecContext(x.ctx, `INSERT INTO s9a1.commerce_purchases(account,restriction_id,period_start,count)
		VALUES($1,$2,$3,$4) ON CONFLICT(account,restriction_id,period_start) DO UPDATE SET count=excluded.count`, x.account, id, p.PeriodStart, p.Count)
	if err == nil {
		x.Inventory.AssetRevision++
	}
	return err
}
