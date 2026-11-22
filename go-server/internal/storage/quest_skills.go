package storage

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
)

// ChangeSkillBuild fences gameplay-only updates by the selected character and
// current lease. The planner may propose equal state for an acknowledgement.
func (s *Store) ChangeSkillBuild(ctx context.Context, l Lease, plan func(Character, Roster) (json.RawMessage, error)) (Character, error) {
	return writeTx(ctx, s.db, func(tx *sql.Tx) (Character, error) {
		r, err := lockAccount(ctx, tx, l, true)
		if err != nil {
			return Character{}, err
		}
		if r.SelectedID == 0 {
			return Character{}, ErrPhase
		}
		c, err := scanCharacter(tx.QueryRowContext(ctx, "SELECT "+characterColumns+" FROM s9a1.characters WHERE account=$1 AND wire_id=$2", l.Account, r.SelectedID))
		if err != nil {
			return c, err
		}
		b, err := plan(c, r)
		if err != nil || len(b) == 0 {
			return c, err
		}
		if !json.Valid(b) {
			return c, ErrInvalid
		}
		next, err := scanCharacter(tx.QueryRowContext(ctx, `UPDATE s9a1.characters SET gameplay_json=$1::json, revision=revision+1
			WHERE account=$2 AND wire_id=$3 AND revision=$4 RETURNING `+characterColumns, string(b), l.Account, c.ID, c.Revision))
		if errors.Is(err, sql.ErrNoRows) {
			return c, ErrConflict
		}
		return next, err
	})
}
