package storage

import (
	"context"
	"database/sql"
	"encoding/json"
	"strings"
)

func (s *Store) ChangeFateActivity(ctx context.Context, l Lease, weights map[uint32]int64, withReceipts bool,
	plan func(Character, Inventory, map[string]json.RawMessage) (TownQuestMutation, error)) (TownQuestResult, error) {
	return s.changeQuest(ctx, l, TownReady, weights, func(ctx context.Context, tx *sql.Tx, c Character) (map[string]json.RawMessage, error) {
		if !withReceipts {
			return map[string]json.RawMessage{}, nil
		}
		return readFateReceipts(ctx, tx, c)
	}, plan)
}

func readFateReceipts(ctx context.Context, tx *sql.Tx, c Character) (map[string]json.RawMessage, error) {
	var p struct {
		Fate struct{ Run *struct{ RunId string } }
	}
	if err := json.Unmarshal(c.Gameplay, &p); err != nil {
		return nil, err
	}
	if p.Fate.Run == nil {
		return map[string]json.RawMessage{}, nil
	}
	if !matchesUUID(p.Fate.Run.RunId) {
		return nil, ErrInvalid
	}
	run := strings.ReplaceAll(p.Fate.Run.RunId, "-", "")
	return queryQuestReceipts(ctx, tx, `SELECT event_key,payload::text FROM s9a1.reward_receipts WHERE character_id=$1
		AND (event_key=$2 OR starts_with(event_key,$3) OR starts_with(event_key,$4))`,
		c.ID, "fate-result:"+run, "fate-node:"+run+":", "fate-pickup:"+run+":")
}
