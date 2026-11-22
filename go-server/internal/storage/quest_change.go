package storage

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strings"
	"time"
)

// TownQuestMutation stores only the character's quest state. A quest-bag grant
// shares its transaction with acceptance and the character revision.
type TownQuestMutation struct {
	Quests   json.RawMessage
	NewItem  *Asset
	Reward   *QuestRewardMutation
	Receipts map[string]json.RawMessage
	Teleport *QuestTeleport
}

type QuestTeleport struct {
	Position     Position
	Gameplay     json.RawMessage
	ConsumeUID   string
	RecordOrigin bool
}

type QuestRewardMutation struct {
	Character  Character
	Inventory  Inventory
	Receipts   map[string]json.RawMessage
	ClearScene bool
}

type TownQuestResult struct {
	Character Character
	Inventory Inventory
	Receipts  map[string]json.RawMessage
}

func (s *Store) ChangeTownQuest(ctx context.Context, l Lease, weights map[uint32]int64, plan func(Character, Inventory, map[string]json.RawMessage) (TownQuestMutation, error)) (TownQuestResult, error) {
	return s.ChangeQuest(ctx, l, TownReady, weights, plan)
}

func (s *Store) ChangeQuest(ctx context.Context, l Lease, phase Phase, weights map[uint32]int64, plan func(Character, Inventory, map[string]json.RawMessage) (TownQuestMutation, error)) (TownQuestResult, error) {
	return s.changeQuest(ctx, l, phase, weights, readAllQuestReceipts, plan)
}

type questReceiptReader func(context.Context, *sql.Tx, Character) (map[string]json.RawMessage, error)

func (s *Store) changeQuest(ctx context.Context, l Lease, phase Phase, weights map[uint32]int64, read questReceiptReader,
	plan func(Character, Inventory, map[string]json.RawMessage) (TownQuestMutation, error)) (TownQuestResult, error) {
	out, err := writeTx(ctx, s.db, func(tx *sql.Tx) (TownQuestResult, error) {
		var out TownQuestResult
		r, err := lockAccount(ctx, tx, l, true)
		if err != nil {
			return out, err
		}
		if r.SelectedID == 0 || r.Phase != phase {
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
		out.Receipts, err = read(ctx, tx, c)
		if err != nil {
			return out, err
		}
		m, err := plan(c, in, out.Receipts)
		if err != nil {
			return out, err
		}
		if move := m.Teleport; move != nil {
			p := move.Position
			if !json.Valid(move.Gameplay) || p.Town > 255 || p.Area > 255 || p.X > 32767 || p.Y > 32767 || p.Direction > 7 || p.State != 0 {
				return out, ErrInvalid
			}
			if move.ConsumeUID != "" {
				remaining, err := QuestConsumeInstance(in, move.ConsumeUID)
				if err != nil {
					return out, err
				}
				remaining.AssetRevision, err = writeQuestInventory(ctx, tx, l, c, in, remaining)
				if err != nil {
					return out, err
				}
				in = remaining
			}
			c, err = scanCharacter(tx.QueryRowContext(ctx, `UPDATE s9a1.characters SET gameplay_json=$1::json,town=$2,area=$3,x=$4,y=$5,direction=$6,state=0,revision=revision+1,
				return_town=CASE WHEN $10 THEN town ELSE return_town END,return_area=CASE WHEN $10 THEN area ELSE return_area END,
				return_x=CASE WHEN $10 THEN x ELSE return_x END,return_y=CASE WHEN $10 THEN y ELSE return_y END,
				return_direction=CASE WHEN $10 THEN direction ELSE return_direction END
				WHERE account=$7 AND wire_id=$8 AND revision=$9 RETURNING `+characterColumns,
				string(move.Gameplay), p.Town, p.Area, p.X, p.Y, p.Direction, l.Account, c.ID, c.Revision, move.RecordOrigin))
			if errors.Is(err, sql.ErrNoRows) {
				return out, ErrConflict
			}
			if err != nil {
				return out, err
			}
			if _, err = tx.ExecContext(ctx, `DELETE FROM s9a1.town_transition WHERE account=$1`, l.Account); err != nil {
				return out, err
			}
			if err = writeQuestReceipts(ctx, tx, c.ID, out.Receipts, m.Receipts); err != nil {
				return out, err
			}
			in.Revision = c.Revision
			out.Character, out.Inventory = c, in
			return out, nil
		}
		if reward := m.Reward; reward != nil {
			next, assets := reward.Character, reward.Inventory
			assets.AssetRevision, err = updateQuestInventory(ctx, tx, l, next, in, assets, weights)
			if err != nil {
				return out, err
			}
			c, err = scanCharacter(tx.QueryRowContext(ctx, `UPDATE s9a1.characters SET gameplay_json=$1::json, stats_json=$2::json, level=$3, gold=$4, revision=revision+1,state=CASE WHEN $8 THEN 0 ELSE state END
				WHERE account=$5 AND wire_id=$6 AND revision=$7 RETURNING `+characterColumns,
				string(next.Gameplay), string(next.Stats), next.Level, assets.Gold, l.Account, c.ID, c.Revision, reward.ClearScene))
			if errors.Is(err, sql.ErrNoRows) {
				return out, ErrConflict
			}
			if err != nil {
				return out, err
			}
			if err = writeQuestReceipts(ctx, tx, c.ID, out.Receipts, reward.Receipts); err != nil {
				return out, err
			}
			completed := 0
			for key := range reward.Receipts {
				if strings.HasPrefix(key, "quest:") {
					completed++
				}
			}
			if completed > 0 {
				assets.AssetRevision, err = s.recordBattlePass(ctx, tx, l.Account, assets.AssetRevision, assets.AssetRevision != r.AssetRevision, time.Now(), BattlePassEvent{13, completed, -1})
				if err != nil {
					return out, err
				}
			}
			assets.Revision = c.Revision
			out.Character, out.Inventory = c, assets
			return out, nil
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
			if err = writeQuestReceipts(ctx, tx, c.ID, out.Receipts, m.Receipts); err != nil {
				return out, err
			}
		}
		out.Character, out.Inventory = c, in
		return out, nil
	})
	if err == nil {
		observeInventoryProjection(ctx, out.Inventory)
	}
	return out, err
}

func readAllQuestReceipts(ctx context.Context, tx *sql.Tx, c Character) (map[string]json.RawMessage, error) {
	return queryQuestReceipts(ctx, tx, "SELECT event_key,payload::text FROM s9a1.reward_receipts WHERE character_id=$1", c.ID)
}

func queryQuestReceipts(ctx context.Context, tx *sql.Tx, query string, args ...any) (map[string]json.RawMessage, error) {
	rows, err := tx.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]json.RawMessage{}
	for rows.Next() {
		var key, raw string
		if err = rows.Scan(&key, &raw); err != nil {
			return nil, err
		}
		out[key] = json.RawMessage(raw)
	}
	return out, rows.Err()
}

func writeQuestReceipts(ctx context.Context, tx *sql.Tx, id uint16, prior, added map[string]json.RawMessage) error {
	for key, raw := range added {
		if _, err := tx.ExecContext(ctx, `INSERT INTO s9a1.reward_receipts(character_id,event_key,payload) VALUES($1,$2,$3::json)`, id, key, string(raw)); err != nil {
			return err
		}
		prior[key] = raw
	}
	return nil
}
