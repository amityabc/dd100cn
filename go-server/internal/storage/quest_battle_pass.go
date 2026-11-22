package storage

import (
	"context"
	"database/sql"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"time"
)

func (s *Store) recordDungeonBattlePass(ctx context.Context, tx *sql.Tx, l Lease, c Character, change DungeonChange) error {
	if s.battlePass == nil || !change.BattlePassEligible || change.Replay || !strings.HasPrefix(change.Key, "dungeon-death:") {
		return nil
	}
	var input struct {
		Source, Version, Account, OwnerGuid, RunId  string
		Epoch                                       int64
		Character, Actor, Opcode                    uint16
		MazeIndex, RoomX, RoomY, Class, Type        byte
		Body                                        []byte
		Maps                                        []uint32
		ExperienceAward                             uint32
		Dummy                                       bool
		AiScriptIndex                               *byte
		CleanupBossActor, CompanionActor            uint16
		ScriptTerminal, StormCleanup, SummonedActor json.RawMessage
	}
	var result struct {
		Actor           uint16
		DungeonCleared  bool
		RoomCleared     bool
		ExperienceAward uint32
		Maps            []uint32
	}
	if json.Unmarshal([]byte(change.Input), &input) != nil || json.Unmarshal([]byte(change.Result), &result) != nil || input.Source != "ClientDelegated" || input.Version != "TrustedLocalClientCombat/v1" ||
		input.Account != l.Account || input.OwnerGuid != l.Owner || input.Epoch != l.Epoch || input.Character != c.ID || input.Class != 1 || input.Opcode != 39 || input.Actor != result.Actor ||
		!matchesUUID(input.RunId) || len(input.Body) < 74 || len(input.Body) != 74+14*int(input.Body[30]) ||
		binary.LittleEndian.Uint32(input.Body) != uint32(input.Actor) || binary.LittleEndian.Uint16(input.Body[4:]) != c.ID ||
		input.Dummy || input.AiScriptIndex != nil || input.CleanupBossActor != 0 || input.CompanionActor != 0 ||
		hasEffect(input.ScriptTerminal) || hasEffect(input.StormCleanup) || hasEffect(input.SummonedActor) ||
		result.DungeonCleared && !result.RoomCleared || result.ExperienceAward > input.ExperienceAward ||
		len(input.Maps) == 0 || !slices.Equal(input.Maps, result.Maps) {
		return nil
	}
	run := strings.ReplaceAll(input.RunId, "-", "")
	if change.Key != fmt.Sprintf("dungeon-death:%s:%d:%d:%d:%d", run, input.MazeIndex, input.RoomX, input.RoomY, input.Actor) {
		return nil
	}
	events := []BattlePassEvent{{Type: 1, Count: 1, Parameter: -1}}
	if input.Type == 3 {
		events = append(events, BattlePassEvent{Type: 2, Count: 1, Parameter: -1})
	}
	if result.DungeonCleared {
		var revived bool
		if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM s9a1.reward_receipts WHERE character_id=$1 AND starts_with(event_key,$2))`, c.ID, "revive:"+run+":").Scan(&revived); err != nil {
			return err
		}
		if !revived {
			events = append(events, BattlePassEvent{Type: 5, Count: 1, Parameter: -1})
		}
	}
	_, err := s.recordBattlePass(ctx, tx, l.Account, 0, false, time.Now(), events...)
	return err
}

type BattlePassEvent struct{ Type, Count, Parameter int }
type BattlePassUpdater func(json.RawMessage, time.Time, []BattlePassEvent) (json.RawMessage, error)

// BindBattlePass supplies the frozen season before the host starts serving.
func (s *Store) BindBattlePass(update BattlePassUpdater) { s.battlePass = update }

func battlePassState(ctx context.Context, tx *sql.Tx, account string) (json.RawMessage, error) {
	var b []byte
	err := tx.QueryRowContext(ctx, "SELECT battle_pass::text FROM s9a1.accounts WHERE account=$1", account).Scan(&b)
	return json.RawMessage(b), err
}

func writeBattlePassState(ctx context.Context, tx *sql.Tx, account string, raw json.RawMessage, bump bool) (rev int64, err error) {
	if !json.Valid(raw) {
		return 0, ErrInvalid
	}
	add := 0
	if bump {
		add = 1
	}
	err = tx.QueryRowContext(ctx, `UPDATE s9a1.accounts SET battle_pass=$2::json,asset_revision=asset_revision+$3 WHERE account=$1 RETURNING asset_revision`, account, string(raw), add).Scan(&rev)
	return rev, err
}

func (s *Store) recordBattlePass(ctx context.Context, tx *sql.Tx, account string, rev int64, alreadyBumped bool, now time.Time, events ...BattlePassEvent) (int64, error) {
	if s.battlePass == nil {
		return rev, nil
	}
	raw, err := battlePassState(ctx, tx, account)
	if err != nil {
		return 0, err
	}
	next, err := s.battlePass(raw, now, events)
	if err != nil {
		return 0, err
	}
	if next == nil {
		return rev, nil
	}
	return writeBattlePassState(ctx, tx, account, next, !alreadyBumped)
}

func (s *Store) RefreshBattlePass(ctx context.Context, l Lease, now time.Time, login bool) error {
	if s.battlePass == nil {
		return nil
	}
	_, err := writeTx(ctx, s.db, func(tx *sql.Tx) (struct{}, error) {
		r, err := lockAccount(ctx, tx, l, true)
		if err != nil {
			return struct{}{}, err
		}
		if r.SelectedID == 0 || r.Phase != Selected && r.Phase != TownLoading && r.Phase != TownReady || login && r.Phase != Selected {
			return struct{}{}, nil
		}
		var events []BattlePassEvent
		if login {
			events = append(events, BattlePassEvent{30, 1, -1})
		}
		_, err = s.recordBattlePass(ctx, tx, l.Account, r.AssetRevision, false, now, events...)
		return struct{}{}, err
	})
	return err
}

func (s *Store) BattlePass(ctx context.Context, l Lease) (json.RawMessage, error) {
	return writeTx(ctx, s.db, func(tx *sql.Tx) (json.RawMessage, error) {
		if _, err := lockAccount(ctx, tx, l, false); err != nil {
			return nil, err
		}
		return battlePassState(ctx, tx, l.Account)
	})
}

type BattlePassPlan struct {
	State     json.RawMessage
	Inventory *Inventory
}

type BattlePassResult struct {
	Character Character
	Inventory Inventory
	State     json.RawMessage
}

func (s *Store) ChangeBattlePass(ctx context.Context, l Lease, weights map[uint32]int64, plan func(Character, Inventory, json.RawMessage) (BattlePassPlan, error)) (BattlePassResult, error) {
	return writeTx(ctx, s.db, func(tx *sql.Tx) (BattlePassResult, error) {
		var out BattlePassResult
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
		state, err := battlePassState(ctx, tx, l.Account)
		if err != nil {
			return out, err
		}
		p, err := plan(c, in, state)
		if err != nil {
			return out, err
		}
		if p.Inventory != nil {
			prev := make(map[string]uint32, len(in.Items))
			for _, a := range in.Items {
				if a.CharacterID == c.ID {
					prev[a.UID] = a.Count
				}
			}
			changed := false
			for _, a := range p.Inventory.Items {
				if a.CharacterID == c.ID {
					changed = changed || prev[a.UID] != a.Count
					delete(prev, a.UID)
				}
			}
			p.Inventory.AssetRevision, err = updateQuestInventory(ctx, tx, l, c, in, *p.Inventory, weights)
			if err != nil {
				return out, err
			}
			in = *p.Inventory
			if changed || len(prev) != 0 {
				c, err = scanCharacter(tx.QueryRowContext(ctx, "UPDATE s9a1.characters SET revision=revision+1 WHERE account=$1 AND wire_id=$2 AND revision=$3 RETURNING "+characterColumns, l.Account, c.ID, c.Revision))
				if err != nil {
					return out, err
				}
				in.Revision = c.Revision
			}
		}
		if p.State != nil {
			in.AssetRevision, err = writeBattlePassState(ctx, tx, l.Account, p.State, in.AssetRevision == r.AssetRevision)
			if err != nil {
				return out, err
			}
			state = p.State
		}
		return BattlePassResult{c, in, state}, nil
	})
}
