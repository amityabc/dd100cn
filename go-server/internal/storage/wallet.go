package storage

import (
	"context"
	"database/sql"
	"errors"
	"math"
)

// Operation is a durable game event, e.g. one eligible member's claim on one
// persisted run. The transport sequence number cannot supply this identity.
type GoldMutation struct {
	CharacterID  uint16
	Operation    string
	Delta, Limit int64
}

type GoldResult struct {
	Gold, Revision int64
	Applied        bool
}

// ApplyGold performs the complete debit/credit and receipt in one commit.
// The caller provides the effective carry limit from its verified game rules.
func (s *Store) ApplyGold(ctx context.Context, l Lease, m GoldMutation) (GoldResult, error) {
	if m.Operation == "" || m.CharacterID == 0 || m.Delta == 0 || m.Delta < -math.MaxUint32 || m.Delta > math.MaxUint32 || m.Limit < 0 || m.Limit > math.MaxUint32 {
		return GoldResult{}, ErrInvalid
	}
	return writeTx(ctx, s.db, func(tx *sql.Tx) (GoldResult, error) {
		r, err := lockAccount(ctx, tx, l, true)
		if err != nil {
			return GoldResult{}, err
		}
		if r.SelectedID != m.CharacterID {
			return GoldResult{}, ErrPhase
		}
		var historical bool
		if err = tx.QueryRowContext(ctx, "SELECT EXISTS(SELECT 1 FROM s9a1.reward_receipts WHERE character_id=$1 AND event_key=$2)", m.CharacterID, m.Operation).Scan(&historical); err != nil {
			return GoldResult{}, err
		}
		if historical {
			return GoldResult{}, ErrConflict
		}
		var result GoldResult
		res, err := tx.ExecContext(ctx, `INSERT INTO s9a1.gold_receipts(character_id,operation,delta) VALUES($1,$2,$3)
			ON CONFLICT(character_id,operation) DO NOTHING`, m.CharacterID, m.Operation, m.Delta)
		if err != nil {
			return result, err
		}
		n, err := res.RowsAffected()
		if err != nil {
			return result, err
		}
		if n == 0 {
			var prior int64
			err = tx.QueryRowContext(ctx, `SELECT c.gold,c.revision,r.delta FROM s9a1.characters c
				JOIN s9a1.gold_receipts r ON r.character_id=c.wire_id WHERE c.account=$1 AND c.wire_id=$2 AND r.operation=$3`,
				l.Account, m.CharacterID, m.Operation).Scan(&result.Gold, &result.Revision, &prior)
			if err != nil {
				return result, err
			}
			if prior != m.Delta {
				return result, ErrConflict
			}
			return result, nil
		}
		err = tx.QueryRowContext(ctx, `UPDATE s9a1.characters SET gold=gold+$3,revision=revision+1
			WHERE account=$1 AND wire_id=$2 AND gold+$3 BETWEEN 0 AND 4294967295
			AND ($3::bigint<0 OR gold+$3<=$4) RETURNING gold,revision`, l.Account, m.CharacterID, m.Delta, m.Limit).Scan(&result.Gold, &result.Revision)
		if errors.Is(err, sql.ErrNoRows) {
			return result, ErrFunds
		}
		if err != nil {
			return result, err
		}
		result.Applied = true
		return result, nil
	})
}
