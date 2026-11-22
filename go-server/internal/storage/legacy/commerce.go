package legacy

import (
	"context"
	"database/sql"

	"s9a1/server/internal/storage"
)

func (s *source) copyCommerceHistory(ctx context.Context, pg *sql.Tx, account string) error {
	var next int64
	if err := s.tx.QueryRowContext(ctx, `SELECT next FROM local_asset_sequences WHERE name='pet_serial'`).Scan(&next); err != nil {
		return err
	}
	if _, err := pg.ExecContext(ctx, "UPDATE s9a1.accounts SET pet_serial_next=$2 WHERE account=$1", account, next); err != nil {
		return err
	}
	rows, err := s.tx.QueryContext(ctx, "SELECT restriction_id,period_start,count FROM local_cash_purchases WHERE account='local'")
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var id, count uint32
		var period int64
		if err = rows.Scan(&id, &period, &count); err != nil {
			return err
		}
		_, err = pg.ExecContext(ctx, `INSERT INTO s9a1.commerce_purchases(account,restriction_id,period_start,count)
			VALUES($1,$2,$3,$4)`, account, id, period, count)
		if err != nil {
			return err
		}
	}
	return rows.Err()
}

func (s *source) copyFateExchange(ctx context.Context, pg *sql.Tx, account string, revision int64) error {
	var total int64
	for _, tbl := range s.report.Tables {
		if tbl.Name == "local_fate_exchange_purchases" {
			total = tbl.Rows
			break
		}
	}
	if total == 0 {
		return nil
	}
	if total > revision {
		return storage.ErrInvalid
	}
	rows, err := s.tx.QueryContext(ctx, `SELECT event_id,product_id,period,period_key,purchased,buyer_character_id,
		account_revision,character_revision,receipt_key,receipt_input_json,receipt_result_json,receipt_grants_json
		FROM local_fate_exchange_purchases WHERE account='local' ORDER BY account_revision`)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var r storage.FateExchangeReceipt
		if err = rows.Scan(&r.EventID, &r.ProductID, &r.Period, &r.PeriodKey, &r.Purchased, &r.BuyerCharacterID, &r.AccountRevision,
			&r.CharacterRevision, &r.Key, &r.Input, &r.Result, &r.Grants); err != nil {
			return err
		}
		if r.AccountRevision > revision {
			return storage.ErrInvalid
		}
		if err = storage.InsertFateExchangeReceipt(ctx, pg, account, r); err != nil {
			return err
		}
		total--
	}
	if total != 0 {
		return storage.ErrInvalid
	}
	return rows.Err()
}

func (s *source) copyCharacterServiceDeadlines(ctx context.Context, pg *sql.Tx, account string) error {
	available := false
	for _, tbl := range s.report.Tables {
		available = available || tbl.Name == "local_account_character_services" && tbl.Rows > 0
	}
	if !available {
		return nil
	}
	rows, err := s.tx.QueryContext(ctx, "SELECT slot,expires_at FROM local_account_character_services WHERE account='local'")
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var slot byte
		var expiry uint32
		if err = rows.Scan(&slot, &expiry); err != nil {
			return err
		}
		if _, err = pg.ExecContext(ctx, "INSERT INTO s9a1.character_service_deadlines(account,slot,expires_at) VALUES($1,$2,$3)", account, slot, expiry); err != nil {
			return err
		}
	}
	return rows.Err()
}
