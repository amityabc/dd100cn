package storage

import (
	"context"
	"database/sql"
	"encoding/json"
	"strings"
)

type OculusPlayerLedger struct {
	AdmissionRevision, CharacterRevision int64
	Receipts                             map[string]DungeonSceneReceipt
}

func (s *Store) OculusPlayerReceipts(ctx context.Context, lease Lease, c Character, entry OculusEntry) (OculusPlayerLedger, error) {
	return writeTx(ctx, s.db, func(tx *sql.Tx) (OculusPlayerLedger, error) {
		var ledger OculusPlayerLedger
		current, err := dungeonCharacter(ctx, tx, lease, c)
		if err != nil {
			return ledger, err
		}
		if entry.Owner != lease.Owner || entry.Epoch != lease.Epoch || entry.CharacterId != current.ID || !matchesUUID(entry.RunId) {
			return ledger, ErrConflict
		}
		admission, err := readDungeonReceipt(ctx, tx, current, entry.Key())
		if err != nil {
			return ledger, err
		}
		if admission.Result != "admitted" || admission.Input != string(mustDungeonJSON(entry)) {
			return ledger, ErrConflict
		}
		suffix := strings.ReplaceAll(entry.RunId, "-", "") + ":"
		rows, err := tx.QueryContext(ctx, `SELECT event_key,payload::text
			FROM s9a1.reward_receipts WHERE character_id=$1 AND (starts_with(event_key,$2) OR starts_with(event_key,$3))`,
			c.ID, "death:"+suffix, "revive:"+suffix)
		if err != nil {
			return ledger, err
		}
		defer rows.Close()
		ledger = OculusPlayerLedger{AdmissionRevision: admission.Revision, CharacterRevision: current.Revision, Receipts: map[string]DungeonSceneReceipt{}}
		for rows.Next() {
			var key, raw string
			var saved deathReceipt
			if err = rows.Scan(&key, &raw); err != nil {
				return OculusPlayerLedger{}, err
			}
			if json.Unmarshal([]byte(raw), &saved) != nil || saved.Key != key ||
				saved.Revision <= admission.Revision || saved.Revision > current.Revision || saved.Grants == nil || len(saved.Grants) != 0 {
				return OculusPlayerLedger{}, ErrConflict
			}
			ledger.Receipts[key] = DungeonSceneReceipt{Key: key, Input: saved.Input, Result: saved.Result, Revision: saved.Revision}
		}
		return ledger, rows.Err()
	})
}
