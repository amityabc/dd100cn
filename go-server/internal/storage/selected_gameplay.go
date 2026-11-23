package storage

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
)

// SaveSelectedGameplay commits a first-entry initialization against the
// exact character revision used to plan it.
func (s *Store) SaveSelectedGameplay(ctx context.Context, l Lease, c Character, profile json.RawMessage) (Character, error) {
	return writeTx(ctx, s.db, func(tx *sql.Tx) (Character, error) {
		r, err := lockAccount(ctx, tx, l, true)
		if err != nil {
			return Character{}, err
		}
		if r.Phase != Selected || r.SelectedID != c.ID {
			return Character{}, ErrPhase
		}
		v, err := scanCharacter(tx.QueryRowContext(ctx, `UPDATE s9a1.characters
			SET gameplay_json=$4::json,revision=revision+1
			WHERE account=$1 AND wire_id=$2 AND revision=$3 RETURNING `+characterColumns,
			l.Account, c.ID, c.Revision, string(profile)))
		if errors.Is(err, sql.ErrNoRows) {
			return Character{}, ErrConflict
		}
		if err != nil {
			return Character{}, err
		}
		worn := []Character{v}
		err = loadWorn(ctx, tx, l.Account, worn)
		return worn[0], err
	})
}
