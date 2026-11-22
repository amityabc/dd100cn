package storage

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
)

// TownQuestMutation stores only the character's quest state. A quest-bag grant
// shares its transaction with acceptance and the character revision.
type TownQuestMutation struct {
	Quests  json.RawMessage
	NewItem *Asset
}

type TownQuestResult struct {
	Character Character
	Inventory Inventory
	Receipts  map[string]json.RawMessage
}

func (s *Store) ChangeTownQuest(ctx context.Context, l Lease, weights map[uint32]int64, plan func(Character, Inventory, map[string]json.RawMessage) (TownQuestMutation, error)) (TownQuestResult, error) {
	return writeTx(ctx, s.db, func(tx *sql.Tx) (TownQuestResult, error) {
		var out TownQuestResult
		r, err := lockAccount(ctx, tx, l, true)
		if err != nil {
			return out, err
		}
		if r.SelectedID == 0 || r.Phase != TownReady {
			return out, ErrPhase
		}
		c, err := scanCharacter(tx.QueryRowContext(ctx, "SELECT "+characterColumns+" FROM s9a1.characters WHERE account=$1 AND wire_id=$2", l.Account, r.SelectedID))
		if err != nil {
			return out, err
		}
		in, err := readInventory(ctx, tx, l.Account, r)
		if err != nil {
			return out, err
		}
		rows, err := tx.QueryContext(ctx, "SELECT event_key,payload::text FROM s9a1.reward_receipts WHERE character_id=$1", c.ID)
		if err != nil {
			return out, err
		}
		out.Receipts = map[string]json.RawMessage{}
		for rows.Next() {
			var key string
			var raw []byte
			if err = rows.Scan(&key, &raw); err != nil {
				rows.Close()
				return out, err
			}
			out.Receipts[key] = json.RawMessage(raw)
		}
		if err = rows.Err(); err != nil {
			rows.Close()
			return out, err
		}
		rows.Close()
		m, err := plan(c, in, out.Receipts)
		if err != nil {
			return out, err
		}
		if m.NewItem != nil {
			a := m.NewItem
			if len(m.Quests) == 0 || a.Source != "item" || a.Location != 0 || a.Slot < 177 || a.Slot > 232 || a.Count != 1 || a.CharacterID != c.ID || len(a.Core) != 119 || !json.Valid(a.Template) || !json.Valid(a.Attributes) {
				return out, ErrInvalid
			}
			if err = checkBagWeight(c, in, map[uint32]int64{a.TemplateID: 1}, weights); err != nil {
				return out, err
			}
			err = tx.QueryRowContext(ctx, `INSERT INTO s9a1.assets(instance_id,account,character_id,source,template_id,template_json,location,slot,count,attributes_json,core,detail)
				VALUES(replace(gen_random_uuid()::text,'-',''),$1,$2,'item',$3,$4::json,0,$5,1,$6::json,$7,$8) RETURNING instance_id`, l.Account, c.ID, a.TemplateID, string(a.Template), a.Slot, string(a.Attributes), a.Core, a.Detail).Scan(&a.UID)
			if err != nil {
				return out, err
			}
			in.Items = append(in.Items, *a)
		}
		if len(m.Quests) > 0 {
			if !json.Valid(m.Quests) {
				return out, ErrInvalid
			}
			c, err = scanCharacter(tx.QueryRowContext(ctx, `UPDATE s9a1.characters SET gameplay_json=jsonb_set(gameplay_json::jsonb,'{Quests}',$1::jsonb)::json, revision=revision+1
				WHERE account=$2 AND wire_id=$3 AND revision=$4 RETURNING `+characterColumns, string(m.Quests), l.Account, c.ID, c.Revision))
			if errors.Is(err, sql.ErrNoRows) {
				return out, ErrConflict
			}
			if err != nil {
				return out, err
			}
			in.Revision = c.Revision
		}
		out.Character, out.Inventory = c, in
		return out, nil
	})
}
