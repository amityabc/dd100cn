package storage

import (
	"context"
	"database/sql"
	"errors"
	"strings"
)

// FrozenDungeonResult reads the confirmed entry and any committed C46 in one
// snapshot. A retry must consume the original draw, including across a reset
// of the daily reward period or a lost commit acknowledgement.
func (s *Store) FrozenDungeonResult(ctx context.Context, l Lease, c Character, key, entryInput, entryResult, resultInput string) (string, error) {
	return writeTx(ctx, s.db, func(tx *sql.Tx) (string, error) {
		current, err := dungeonCharacter(ctx, tx, l, c)
		if err != nil {
			return "", err
		}
		if !strings.HasPrefix(key, settlementReceiptPrefix) {
			return "", ErrInvalid
		}
		entry, err := readDungeonReceipt(ctx, tx, current, key+":entry")
		if err != nil {
			return "", err
		}
		if entry.Input != entryInput || entry.Result != entryResult || entry.Grants == nil || len(entry.Grants) != 0 {
			return "", ErrConflict
		}
		result, err := readDungeonReceipt(ctx, tx, current, key+":result")
		if errors.Is(err, sql.ErrNoRows) {
			return "", nil
		}
		if err != nil {
			return "", err
		}
		if result.Input != resultInput || result.Revision <= entry.Revision || result.Grants == nil {
			return "", ErrConflict
		}
		return result.Result, nil
	})
}
