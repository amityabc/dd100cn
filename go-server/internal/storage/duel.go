package storage

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
)

// EnsureDuelSkills runs the original build planner on the most recent selected
// character. An existing duel page is independently owned and must survive
// later selections and ordinary skill changes.
func (s *Store) EnsureDuelSkills(ctx context.Context, l Lease, id uint16, plan func(Character) (json.RawMessage, error)) (Character, error) {
	return writeTx(ctx, s.db, func(tx *sql.Tx) (Character, error) {
		r, err := lockAccount(ctx, tx, l, true)
		if err != nil {
			return Character{}, err
		}
		if id == 0 || r.SelectedID != id || r.Phase != Selected {
			return Character{}, ErrPhase
		}
		c, err := scanCharacter(tx.QueryRowContext(ctx, "SELECT "+characterColumns+" FROM s9a1.characters WHERE account=$1 AND wire_id=$2", l.Account, id))
		if err != nil {
			return Character{}, err
		}
		var profile struct{ DuelSkills json.RawMessage }
		if err = json.Unmarshal(c.Gameplay, &profile); err != nil {
			return Character{}, err
		}
		if len(profile.DuelSkills) == 0 || bytes.Equal(bytes.TrimSpace(profile.DuelSkills), []byte("null")) {
			if plan == nil {
				return Character{}, ErrInvalid
			}
			build, err := plan(c)
			if err != nil {
				return Character{}, err
			}
			if raw := bytes.TrimSpace(build); !json.Valid(raw) || raw[0] != '{' {
				return Character{}, ErrInvalid
			}
			c, err = scanCharacter(tx.QueryRowContext(ctx, `UPDATE s9a1.characters SET revision=revision+1,
				gameplay_json=jsonb_set(gameplay_json::jsonb,'{DuelSkills}',$4::jsonb)::json
				WHERE account=$1 AND wire_id=$2 AND revision=$3 RETURNING `+characterColumns, l.Account, id, c.Revision, string(build)))
			if err != nil {
				return Character{}, err
			}
		}
		characters := []Character{c}
		err = loadWorn(ctx, tx, l.Account, characters)
		return characters[0], err
	})
}
