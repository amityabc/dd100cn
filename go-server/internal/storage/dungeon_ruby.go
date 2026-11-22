package storage

import (
	"context"
	"database/sql"
	"encoding/json"
	"time"

	"s9a1/server/internal/ruby"
)

type RubyMutation struct {
	Entry     ruby.Entry
	Clear     bool
	Resources *ruby.Resources
	Now       time.Time
}

func readRubyLedger(ctx context.Context, tx *sql.Tx, c Character, now time.Time) (ruby.Ledger, error) {
	receipts, err := readRubyReceipts(ctx, tx, c.ID, []string{})
	if err != nil {
		return ruby.Ledger{}, err
	}
	return ruby.Read(receipts, c.ID, c.Revision, now)
}

func (s *Store) RubyLedger(ctx context.Context, l Lease, c Character, now time.Time) (ruby.Ledger, error) {
	return writeTx(ctx, s.db, func(tx *sql.Tx) (ruby.Ledger, error) {
		r, err := lockAccount(ctx, tx, l, false)
		if err != nil {
			return ruby.Ledger{}, err
		}
		if c.ID == 0 || r.SelectedID != c.ID {
			return ruby.Ledger{}, ErrPhase
		}
		var revision int64
		err = tx.QueryRowContext(ctx, "SELECT revision FROM s9a1.characters WHERE wire_id=$1 AND account=$2", c.ID, l.Account).Scan(&revision)
		if err != nil {
			return ruby.Ledger{}, err
		}
		if revision != c.Revision {
			return ruby.Ledger{}, ErrConflict
		}
		return readRubyLedger(ctx, tx, c, now)
	})
}

func (s *Store) ChangeRubyTreasury(ctx context.Context, l Lease, weights map[uint32]int64, bosses []string,
	plan func(Character, Inventory, map[string]json.RawMessage) (TownQuestMutation, error)) (TownQuestResult, error) {
	return s.changeQuest(ctx, l, TownReady, weights, func(ctx context.Context, tx *sql.Tx, c Character) (map[string]json.RawMessage, error) {
		return readRubyReceipts(ctx, tx, c.ID, bosses)
	}, plan)
}

func readRubyReceipts(ctx context.Context, tx *sql.Tx, id uint16, bosses []string) (map[string]json.RawMessage, error) {
	if len(bosses) == 0 {
		bosses = []string{}
	}
	keys, err := json.Marshal(bosses)
	if err != nil {
		return nil, err
	}
	return queryQuestReceipts(ctx, tx, `WITH treasury AS MATERIALIZED (
		SELECT event_key,payload FROM s9a1.reward_receipts WHERE character_id=$1 AND event_key LIKE 'ruby-%'
	), dependencies AS (
		SELECT (payload->>'Input')::json->>'Death' AS event_key FROM treasury
		WHERE event_key LIKE 'ruby-clear:%' OR event_key LIKE 'ruby-hidden-clear:%'
		UNION SELECT json_array_elements_text($2::json)
	)
	SELECT event_key,payload::text FROM treasury
	UNION ALL
	SELECT r.event_key,r.payload::text FROM dependencies k JOIN s9a1.reward_receipts r USING(event_key)
	WHERE r.character_id=$1 AND r.event_key NOT LIKE 'ruby-%'`, id, string(keys))
}

func requireRubyAdmission(ctx context.Context, tx *sql.Tx, c Character, e ruby.Entry, now time.Time) error {
	ledger, err := readRubyLedger(ctx, tx, c, now)
	if err != nil {
		return err
	}
	expected, err := ledger.Admission(c.ID, e.Run, e.Requested, e.Dungeon, e.Seed, e.Hash)
	if err != nil || !equalJSON(mustDungeonJSON(expected), mustDungeonJSON(e)) {
		return ErrConflict
	}
	return nil
}

func attachRubyClear(ctx context.Context, tx *sql.Tx, c Character, change DungeonChange) error {
	m := change.Ruby
	if m == nil {
		return nil
	}
	key := ruby.EntryKey(m.Entry.Run)
	r, err := readDungeonReceipt(ctx, tx, c, key)
	if err != nil {
		return err
	}
	if r.Result != "admitted" || !equalJSON(mustDungeonJSON(m.Entry), json.RawMessage(r.Input)) {
		return ErrConflict
	}
	if !m.Clear {
		return nil
	}
	if m.Resources == nil {
		return ErrInvalid
	}
	var z struct{ RoomCleared, DungeonCleared bool }
	if json.Unmarshal([]byte(change.Result), &z) != nil || !z.RoomCleared || !z.DungeonCleared {
		return ErrInvalid
	}
	l, err := readRubyLedger(ctx, tx, c, m.Now)
	if err != nil {
		return err
	}
	// The death and its floor fact receive the same revision in this transaction.
	next, err := m.Resources.Plan(l, m.Entry)
	if err != nil {
		return err
	}
	clear := ruby.Clear{Policy: ruby.Policy, Character: c.ID, Run: m.Entry.Run, Dungeon: m.Entry.Dungeon, Floor: m.Entry.Floor,
		Week: m.Entry.Week, Hash: m.Entry.Hash, Death: change.Key, Advance: &next}
	raw, err := json.Marshal(clear)
	if err != nil {
		return err
	}
	return putDungeonReceipt(ctx, tx, c, ruby.ClearKey(m.Entry), string(raw), "cleared;special-rewards-disabled")
}
