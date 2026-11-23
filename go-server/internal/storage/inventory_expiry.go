package storage

import (
	"context"
	"database/sql"
	"encoding/json"
	"strings"
	"time"
)

func dailyExpired(a Asset, prepared map[uint32]json.RawMessage, now int64) (bool, error) {
	if a.Source == "weapon" || a.Location != 0 && a.Location != 2 && a.Location != 12 || len(prepared[a.TemplateID]) == 0 {
		return false, nil
	}
	var t materialDefinition
	var attr materialAttributes
	if json.Unmarshal(a.Template, &t) != nil || json.Unmarshal(a.Attributes, &attr) != nil {
		return false, ErrInvalid
	}
	if t.Kind != 2 || !t.DailyDeletion || attr.ExpiresAt == nil || *attr.ExpiresAt > now ||
		!sameCommerceTemplate(prepared[a.TemplateID], a.Template) {
		return false, nil
	}
	return ordinaryNewCore(a, t, attr)
}

// Login first imports shared materials, then retires matching daily-delete stacks.
// All retirements from this pass share one character revision and their saved check time.
func (s *Store) expireTownInventory(ctx context.Context, tx *sql.Tx, l Lease, c Character, in Inventory, r *Roster, now time.Time) (Character, Inventory, error) {
	checked := now.Unix()
	var changed, shared bool
	var templates map[uint32]json.RawMessage
	if frozen := s.dailyExpiryTemplates.Load(); frozen != nil {
		templates = *frozen
	}
	for _, a := range in.Items {
		expired, err := dailyExpired(a, templates, checked)
		if err != nil {
			return c, in, err
		}
		if !expired {
			continue
		}
		input, err := json.Marshal(freezeItemAsset(a))
		if err != nil {
			return c, in, err
		}
		result, err := json.Marshal(struct {
			Removed   uint32
			CheckedAt int64
		}{a.Count, checked})
		if err != nil {
			return c, in, err
		}
		key := "inventory:daily-expiry:" + strings.ReplaceAll(a.UID, "-", "")
		if err = putDungeonReceiptWithGrants(ctx, tx, c, key, string(input), string(result), []json.RawMessage{}); err != nil {
			return c, in, err
		}
		deleted, err := tx.ExecContext(ctx, `DELETE FROM s9a1.assets WHERE account=$1 AND instance_id=$2 AND count=$3 AND location=$4 AND slot=$5`, l.Account, a.UID, a.Count, a.Location, a.Slot)
		if err != nil {
			return c, in, err
		}
		if n, err := deleted.RowsAffected(); err != nil || n != 1 {
			return c, in, ErrConflict
		}
		shared = shared || a.CharacterID == 0
		changed = true
	}
	if !changed {
		return c, in, nil
	}
	current, err := scanCharacter(tx.QueryRowContext(ctx, `UPDATE s9a1.characters SET revision=revision+1 WHERE account=$1 AND wire_id=$2 AND revision=$3 RETURNING `+characterColumns, l.Account, c.ID, c.Revision))
	if err != nil {
		return c, in, err
	}
	if shared {
		r.AssetRevision++
	}
	after, err := readInventory(ctx, tx, l.Account, *r)
	return current, after, err
}
