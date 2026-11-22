package storage

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"s9a1/server/internal/savejson"
	"strings"
	"time"
)

type DungeonPickupInput struct {
	Version                       string
	Account                       savejson.String
	OwnerGuid                     string
	Epoch                         int64
	Character                     uint16
	RunId                         string
	Dungeon                       uint32
	Maze                          byte
	MapId                         uint32
	Drop                          json.RawMessage
	DropRuleHash                  string
	DeathInput, DeathResult, Body savejson.String
}

type DungeonPickup struct {
	Key, OriginKey string
	Input          DungeonPickupInput
	Gold           int64
	Replay         bool
	Now            time.Time
	Create         func(Inventory) ([]QuestGrant, error)
	Wisdom         *WisdomAdmission
}

type DungeonLootResult struct {
	Inventory Inventory
	Character Character
	Grants    []Asset
	Slot      uint16
}

func (s *Store) PickupDungeon(ctx context.Context, l Lease, c Character, p DungeonPickup) (DungeonLootResult, error) {
	return writeTx(ctx, s.db, func(tx *sql.Tx) (DungeonLootResult, error) {
		var out DungeonLootResult
		current, err := dungeonCharacter(ctx, tx, l, c)
		if err != nil {
			return out, err
		}
		if p.Wisdom != nil {
			if err := requireWisdomAdmission(ctx, tx, l, current, *p.Wisdom); err != nil {
				return out, err
			}
		}
		if !strings.HasPrefix(p.Key, "dungeon-pickup:") ||
			!strings.HasPrefix(p.OriginKey, "dungeon-death:") && !strings.HasPrefix(p.OriginKey, "dungeon-passive-death:") ||
			!json.Valid(p.Input.Drop) || p.Input.Account != savejson.String(l.Account) || p.Input.OwnerGuid != l.Owner ||
			p.Input.Epoch != l.Epoch || p.Input.Character != c.ID || p.Gold < 0 || p.Create == nil {
			return out, ErrInvalid
		}
		var origin deathReceipt
		var stored string
		if err := tx.QueryRowContext(ctx, `SELECT payload::text FROM s9a1.reward_receipts
			WHERE character_id=$1 AND event_key=$2`, current.ID, p.OriginKey).Scan(&stored); err != nil {
			return out, err
		}
		var fact struct{ Drops []json.RawMessage }
		if json.Unmarshal([]byte(stored), &origin) != nil || origin.Key != p.OriginKey || origin.Revision <= 0 || origin.Revision > current.Revision ||
			json.Unmarshal([]byte(origin.Result), &fact) != nil {
			return out, ErrConflict
		}
		qualified := false
		for _, f := range fact.Drops {
			if equalJSON(f, p.Input.Drop) {
				qualified = true
				break
			}
		}
		if !qualified {
			return out, ErrConflict
		}
		var frozen struct {
			Reward struct {
				Artifact          json.RawMessage
				Gold, QualitySeed uint32
				Durability        uint16
			}
		}
		if json.Unmarshal(p.Input.Drop, &frozen) != nil || int64(frozen.Reward.Gold) != p.Gold {
			return out, ErrConflict
		}
		var artifact *DungeonArtifact
		if len(frozen.Reward.Artifact) > 0 {
			artifact = &DungeonArtifact{frozen.Reward.Artifact, frozen.Reward.QualitySeed, frozen.Reward.Durability}
		}
		r, err := lockAccount(ctx, tx, l, false)
		if err != nil {
			return out, err
		}
		in, err := readInventory(ctx, tx, l.Account, r)
		if err != nil {
			return out, err
		}
		type pickupResult struct {
			Drop            json.RawMessage
			DestinationSlot uint16
		}
		request := p.Input
		request.DeathInput, request.DeathResult = savejson.String(origin.Input), savejson.String(origin.Result)
		body, err := json.Marshal(request)
		if err != nil {
			return out, err
		}
		var previous deathReceipt
		var grants []questGrantResult
		err = tx.QueryRowContext(ctx, "SELECT payload::text FROM s9a1.reward_receipts WHERE character_id=$1 AND event_key=$2",
			current.ID, p.Key).Scan(&stored)
		if err == nil {
			var result pickupResult
			if !p.Replay || json.Unmarshal([]byte(stored), &previous) != nil || previous.Key != p.Key || previous.Input != string(body) ||
				previous.Revision <= 0 || previous.Revision > c.Revision || json.Unmarshal([]byte(previous.Result), &result) != nil ||
				!equalJSON(result.Drop, p.Input.Drop) || previous.Grants == nil || json.Unmarshal(mustDungeonJSON(previous.Grants), &grants) != nil {
				return out, ErrConflict
			}
			if artifact != nil {
				var special []json.RawMessage
				if len(grants) != 0 || json.Unmarshal(previous.SpecialGrants, &special) != nil ||
					len(special) != 1 || artifact.verifyGrant(special[0], result.DestinationSlot) != nil {
					return out, ErrConflict
				}
			} else if len(previous.SpecialGrants) != 0 && string(previous.SpecialGrants) != "null" {
				return out, ErrConflict
			}
			out = dungeonPickupView(in, grants, result.DestinationSlot)
			out.Character = current
			return out, nil
		}
		if p.Replay || !errors.Is(err, sql.ErrNoRows) {
			return out, ErrConflict
		}
		admitted, err := p.Create(in)
		if err != nil {
			return out, err
		}
		if artifact != nil && (len(admitted) != 0 || p.Gold != 0) ||
			artifact == nil && ((len(admitted) == 0) != (p.Gold != 0)) {
			return out, ErrInvalid
		}
		after, err := QuestLoot(in, nil, admitted, in.Gold+p.Gold, p.Now)
		if err != nil {
			return out, err
		}
		// Standard ground pickup uses the original asset admission and slot
		// bounds. The native movement weight guard belongs to explicit moves.
		after.AssetRevision, err = writeQuestInventory(ctx, tx, l, current, in, after)
		if err != nil {
			return out, err
		}
		after.Revision++
		grantJSON := QuestGranted(in, after)
		if err := json.Unmarshal(grantJSON, &grants); err != nil {
			return out, err
		}
		var slot uint16
		var special []json.RawMessage
		next := current
		if len(grants) > 0 {
			slot = grants[0].Slot
		} else if artifact != nil {
			var raw json.RawMessage
			next, raw, slot, err = artifact.grant(current, p.Now)
			if err != nil {
				return out, err
			}
			special = []json.RawMessage{raw}
		}
		updated, err := tx.ExecContext(ctx, `UPDATE s9a1.characters SET gold=$4,gameplay_json=$5::json,revision=revision+1
			WHERE account=$1 AND wire_id=$2 AND revision=$3`, l.Account, current.ID, current.Revision, after.Gold, string(next.Gameplay))
		if err != nil {
			return out, err
		}
		if n, err := updated.RowsAffected(); err != nil || n != 1 {
			return out, ErrConflict
		}
		next.Revision++
		z, err := json.Marshal(pickupResult{p.Input.Drop, slot})
		if err != nil {
			return out, err
		}
		record := struct {
			Key, Input, Result string
			Revision           int64
			Grants             json.RawMessage
			SpecialGrants      []json.RawMessage `json:",omitempty"`
		}{p.Key, string(body), string(z), current.Revision + 1, grantJSON, special}
		raw, err := json.Marshal(record)
		if err != nil {
			return out, err
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO s9a1.reward_receipts(character_id,event_key,payload)
			VALUES($1,$2,$3::json)`, current.ID, p.Key, string(raw)); err != nil {
			return out, err
		}
		out = dungeonPickupView(after, grants, slot)
		out.Character = next
		return out, nil
	})
}

func mustDungeonJSON(v any) []byte {
	b, _ := json.Marshal(v)
	return b
}

func dungeonPickupView(in Inventory, grants []questGrantResult, slot uint16) DungeonLootResult {
	out := DungeonLootResult{Inventory: in, Slot: slot}
	for _, g := range grants {
		for _, item := range in.Items {
			if strings.ReplaceAll(item.UID, "-", "") == strings.ReplaceAll(g.InstanceId, "-", "") {
				out.Grants = append(out.Grants, item)
				break
			}
		}
	}
	return out
}
