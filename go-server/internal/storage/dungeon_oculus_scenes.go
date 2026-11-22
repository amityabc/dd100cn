package storage

import (
	"context"
	"database/sql"
	"errors"
	"strings"
)

type DungeonSceneReceipt struct {
	Key, Input, Result string
	Revision           int64
}

func (s *Store) OculusSceneReceipts(ctx context.Context, lease Lease, c Character, entry OculusEntry) (map[string]DungeonSceneReceipt, error) {
	return writeTx(ctx, s.db, func(tx *sql.Tx) (map[string]DungeonSceneReceipt, error) {
		current, err := dungeonCharacter(ctx, tx, lease, c)
		if err != nil {
			return nil, err
		}
		return readOculusSceneReceipts(ctx, tx, current, lease, entry)
	})
}

func (x *CommerceTx) OculusSceneReceipts(entry OculusEntry) (map[string]DungeonSceneReceipt, error) {
	return readOculusSceneReceipts(x.ctx, x.tx, x.Character, x.lease, entry)
}

func readOculusSceneReceipts(ctx context.Context, tx *sql.Tx, current Character, lease Lease, entry OculusEntry) (map[string]DungeonSceneReceipt, error) {
	if entry.Owner != lease.Owner || entry.Epoch != lease.Epoch || entry.CharacterId != current.ID || !matchesUUID(entry.RunId) {
		return nil, ErrConflict
	}
	admission, err := readDungeonReceipt(ctx, tx, current, entry.Key())
	if err != nil {
		return nil, err
	}
	if admission.Input != string(mustDungeonJSON(entry)) || admission.Result != "admitted" {
		return nil, ErrConflict
	}
	run := strings.ReplaceAll(entry.RunId, "-", "")
	if _, err = readDungeonReceipt(ctx, tx, current, "oculus-entry:end:"+run); !errors.Is(err, sql.ErrNoRows) {
		if err == nil {
			return nil, ErrConflict
		}
		return nil, err
	}
	rows, err := tx.QueryContext(ctx, `SELECT event_key,payload->>'Key',payload->>'Input',payload->>'Result',(payload->>'Revision')::bigint
			FROM s9a1.reward_receipts WHERE character_id=$1 AND
			(starts_with(event_key,$2) OR starts_with(event_key,$3))`,
		current.ID, "dungeon-death:"+run+":", "dungeon-passive-death:"+run+":")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]DungeonSceneReceipt{}
	for rows.Next() {
		var row DungeonSceneReceipt
		var identity string
		if err = rows.Scan(&identity, &row.Key, &row.Input, &row.Result, &row.Revision); err != nil {
			return nil, err
		}
		if row.Key != identity || row.Revision < admission.Revision || row.Revision > current.Revision || row.Input == "" || row.Result == "" {
			return nil, ErrConflict
		}
		out[row.Key] = row
	}
	return out, rows.Err()
}
