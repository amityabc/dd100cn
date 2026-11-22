package storage

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
)

func (s *Store) OculusBossAllowance(ctx context.Context, l Lease, c Character, e OculusEntry) (byte, string, error) {
	type allowance struct {
		Used  byte
		Input string
	}
	z, err := writeTx(ctx, s.db, func(tx *sql.Tx) (allowance, error) {
		current, err := dungeonCharacter(ctx, tx, l, c)
		if err != nil {
			return allowance{}, err
		}
		r, err := readDungeonReceipt(ctx, tx, current, e.Key())
		if err != nil {
			return allowance{}, err
		}
		if r.Result != "admitted" || !equalJSON(json.RawMessage(r.Input), mustDungeonJSON(e)) {
			return allowance{}, ErrConflict
		}
		n, err := oculusBossUsed(ctx, tx, current, e.Week)
		return allowance{n, r.Input}, err
	})
	return z.Used, z.Input, err
}

func oculusBossUsed(ctx context.Context, tx *sql.Tx, c Character, week int64) (byte, error) {
	prefix := fmt.Sprintf("oculus-boss-reward:week:100000007:%d", week)
	var count int
	err := tx.QueryRowContext(ctx, `SELECT count(*) FROM s9a1.reward_receipts
		WHERE character_id=$1 AND (event_key=$2 OR event_key LIKE $2||':%')`, c.ID, prefix).Scan(&count)
	if count < 0 || count > 127 {
		return 0, ErrConflict
	}
	return byte(count), err
}

func attachOculusBossMarker(ctx context.Context, tx *sql.Tx, c Character, change DungeonChange, grants []json.RawMessage) error {
	if change.OculusBossMarker == "" {
		return nil
	}
	var proof struct {
		OculusBossPolicy struct {
			Policy, EntryInput         string
			Week                       int64
			WeeklyOrdinal, WeeklyLimit byte
		}
	}
	if json.Unmarshal([]byte(change.Result), &proof) != nil {
		return ErrConflict
	}
	p := proof.OculusBossPolicy
	key := fmt.Sprintf("oculus-boss-reward:week:100000007:%d:%d", p.Week, p.WeeklyOrdinal)
	if p.Week < 19991230 || key != change.OculusBossMarker || p.WeeklyOrdinal == 0 || p.WeeklyLimit < p.WeeklyOrdinal || p.WeeklyLimit > 127 ||
		p.Policy != "OculusBossTimeGuide/PVF-weekly-limit;one-total;ground-max-five;stable-sha256-rejection/v4" || len(grants) != 0 {
		return ErrConflict
	}
	var admission OculusEntry
	if json.Unmarshal([]byte(p.EntryInput), &admission) != nil || admission.Week != p.Week || admission.CharacterId != c.ID {
		return ErrConflict
	}
	r, err := readDungeonReceipt(ctx, tx, c, admission.Key())
	if err != nil {
		return err
	}
	if r.Result != "admitted" || r.Input != p.EntryInput {
		return ErrConflict
	}
	used, err := oculusBossUsed(ctx, tx, c, p.Week)
	if err != nil {
		return err
	}
	if p.WeeklyOrdinal != used+1 {
		return ErrConflict
	}
	return putDungeonReceiptWithGrants(ctx, tx, c, key, change.Input, change.Result, grants)
}
