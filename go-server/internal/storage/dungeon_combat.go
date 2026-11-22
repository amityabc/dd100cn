package storage

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strings"
	"time"
)

type DungeonChange struct {
	Key, Input, Result string
	Next               Character
	ExperienceAward    uint32
	Replay             bool
	Proof              []string
	Luck               *DungeonLuckChange
	FirstHunt          *DungeonFirstHunt
	ImmediateGrants    func(Inventory) ([]QuestGrant, error)
	GrantTime          time.Time
	Abaddon            *AbaddonMutation
	Ruby               *RubyMutation
	OculusBossMarker   string
	Summon             *PotionSummonKill
	Wisdom             *WisdomAdmission
	PetGrowth          *DungeonPetGrowth
	BattlePassEligible bool
}

type DungeonLuckChange struct{ Before, After int64 }

func (s *Store) CommitDungeonCombat(ctx context.Context, l Lease, c Character, change DungeonChange) error {
	var petGrowth *PetGrowthNotice
	_, err := writeTx(ctx, s.db, func(tx *sql.Tx) (struct{}, error) {
		current, err := dungeonCharacter(ctx, tx, l, c)
		if err != nil {
			return struct{}{}, err
		}
		if change.Wisdom != nil {
			if err := requireWisdomAdmission(ctx, tx, l, current, *change.Wisdom); err != nil {
				return struct{}{}, err
			}
		}
		for _, key := range change.Proof {
			if err := requireDungeonReceipt(ctx, tx, current, key); err != nil {
				return struct{}{}, err
			}
		}
		replay, err := dungeonReceiptExists(ctx, tx, current, change.Key, change.Input, change.Result, change.ImmediateGrants != nil)
		if err != nil {
			return struct{}{}, err
		}
		if replay != change.Replay {
			return struct{}{}, ErrConflict
		}
		if replay {
			return struct{}{}, nil
		}
		if err = validateSummonedDeath(ctx, tx, l, current, change.Summon); err != nil {
			return struct{}{}, err
		}
		next := change.Next
		next, err = applyAbaddonMutation(ctx, tx, current, next, change.Key, change.Abaddon)
		if err != nil {
			return struct{}{}, err
		}
		var petKey, petInput string
		next, petGrowth, petKey, petInput, err = planDungeonPetGrowth(ctx, tx, l, current, next, change)
		if err != nil {
			return struct{}{}, err
		}
		var prev, updated dungeonProgression
		timeout := change.Abaddon != nil && strings.HasPrefix(change.Key, "abaddon-timeout:")
		if !json.Valid(next.Stats) || json.Unmarshal(current.Gameplay, &prev) != nil ||
			json.Unmarshal(next.Gameplay, &updated) != nil || !prev.Initialized || prev.Progression.Hp == 0 && !timeout ||
			next.ID != current.ID || next.Level < current.Level || next.Level > 100 ||
			!strings.HasPrefix(change.Key, "dungeon-") && !timeout {
			return struct{}{}, ErrInvalid
		}
		if luck := change.Luck; luck != nil {
			if luck.Before != int64(prev.Progression.LuckPoints) || luck.After != int64(updated.Progression.LuckPoints) ||
				luck.After < 0 || luck.After > luck.Before || luck.Before > 100000 {
				return struct{}{}, ErrInvalid
			}
			prev.Progression.LuckPoints = updated.Progression.LuckPoints
		}
		if prev.Progression != updated.Progression {
			return struct{}{}, ErrInvalid
		}
		var oldXP, newXP struct{ Progression struct{ Experience uint64 } }
		if json.Unmarshal(current.Gameplay, &oldXP) != nil || json.Unmarshal(next.Gameplay, &newXP) != nil ||
			uint64(change.ExperienceAward) > math.MaxUint64-oldXP.Progression.Experience ||
			oldXP.Progression.Experience+uint64(change.ExperienceAward) != newXP.Progression.Experience {
			return struct{}{}, ErrInvalid
		}
		grants := []json.RawMessage{}
		if change.ImmediateGrants != nil {
			settlement := strings.HasPrefix(change.Key, "dungeon-settlement:v1:") && strings.HasSuffix(change.Key, ":result")
			death := strings.HasPrefix(change.Key, "dungeon-death:") || strings.HasPrefix(change.Key, "dungeon-passive-death:")
			if !settlement && !death || change.GrantTime.IsZero() {
				return struct{}{}, ErrInvalid
			}
			r, err := lockAccount(ctx, tx, l, false)
			if err != nil {
				return struct{}{}, err
			}
			in, err := readInventory(ctx, tx, l.Account, r)
			if err != nil {
				return struct{}{}, err
			}
			items, err := change.ImmediateGrants(in)
			if err != nil {
				return struct{}{}, err
			}
			after, err := QuestLoot(in, nil, items, in.Gold, change.GrantTime)
			if err != nil {
				return struct{}{}, err
			}
			if _, err := writeQuestInventory(ctx, tx, l, current, in, after); err != nil {
				return struct{}{}, err
			}
			if err := json.Unmarshal(QuestGranted(in, after), &grants); err != nil {
				return struct{}{}, err
			}
		}
		if _, err = tx.ExecContext(ctx, `UPDATE s9a1.characters
			SET level=$4,stats_json=$5::json,gameplay_json=$6::json,revision=revision+1
			WHERE account=$1 AND wire_id=$2 AND revision=$3`, l.Account, current.ID, current.Revision,
			next.Level, string(next.Stats), string(next.Gameplay)); err != nil {
			return struct{}{}, err
		}
		if err = s.recordDungeonBattlePass(ctx, tx, l, current, change); err != nil {
			return struct{}{}, err
		}
		if err := putDungeonReceiptWithGrants(ctx, tx, current, change.Key, change.Input, change.Result, grants); err != nil {
			return struct{}{}, err
		}
		if petGrowth != nil {
			result, err := json.Marshal(petGrowth)
			if err != nil {
				return struct{}{}, err
			}
			if err = putDungeonReceipt(ctx, tx, current, petKey, petInput, string(result)); err != nil {
				return struct{}{}, err
			}
		}
		if err := attachRubyClear(ctx, tx, current, change); err != nil {
			return struct{}{}, err
		}
		if err := attachOculusBossMarker(ctx, tx, current, change, grants); err != nil {
			return struct{}{}, err
		}
		return struct{}{}, attachDungeonFirstHunt(ctx, tx, l, current, change)
	})
	if err == nil && change.PetGrowth != nil {
		change.PetGrowth.Applied = petGrowth
	}
	return err
}

func readDungeonReceipt(ctx context.Context, tx *sql.Tx, c Character, key string) (deathReceipt, error) {
	var payload string
	var out deathReceipt
	if err := tx.QueryRowContext(ctx, `SELECT payload::text FROM s9a1.reward_receipts
		WHERE character_id=$1 AND event_key=$2`, c.ID, key).Scan(&payload); err != nil {
		return out, err
	}
	if json.Unmarshal([]byte(payload), &out) != nil || out.Key != key || out.Revision <= 0 || out.Revision > c.Revision ||
		out.Input == "" || out.Result == "" {
		return out, ErrConflict
	}
	return out, nil
}

func requireDungeonReceipt(ctx context.Context, tx *sql.Tx, c Character, key string) error {
	_, err := readDungeonReceipt(ctx, tx, c, key)
	return err
}

func (s *Store) ValidateDungeonProof(ctx context.Context, l Lease, c Character, keys []string) error {
	_, err := writeTx(ctx, s.db, func(tx *sql.Tx) (struct{}, error) {
		current, err := dungeonCharacter(ctx, tx, l, c)
		if err != nil {
			return struct{}{}, err
		}
		for _, key := range keys {
			if err := requireDungeonReceipt(ctx, tx, current, key); err != nil {
				return struct{}{}, err
			}
		}
		return struct{}{}, nil
	})
	return err
}

func dungeonReceiptExists(ctx context.Context, tx *sql.Tx, c Character, key, input, result string, withGrants bool) (bool, error) {
	if key == "" || len(key) > 256 || !json.Valid([]byte(input)) || result == "" {
		return false, ErrInvalid
	}
	var old string
	err := tx.QueryRowContext(ctx, `SELECT payload::text FROM s9a1.reward_receipts WHERE character_id=$1 AND event_key=$2`, c.ID, key).Scan(&old)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	var previous deathReceipt
	if json.Unmarshal([]byte(old), &previous) != nil || previous.Key != key || previous.Input != input ||
		previous.Result != result || previous.Revision <= 0 || previous.Revision > c.Revision ||
		previous.Grants == nil || !withGrants && len(previous.Grants) != 0 {
		return false, ErrConflict
	}
	return true, nil
}

func putDungeonReceipt(ctx context.Context, tx *sql.Tx, c Character, key, input, result string) error {
	return putDungeonReceiptWithGrants(ctx, tx, c, key, input, result, []json.RawMessage{})
}

func putDungeonReceiptWithGrants(ctx context.Context, tx *sql.Tx, c Character, key, input, result string, grants []json.RawMessage) error {
	p, err := json.Marshal(deathReceipt{Key: key, Input: input, Result: result, Revision: c.Revision + 1, Grants: grants})
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO s9a1.reward_receipts(character_id,event_key,payload) VALUES($1,$2,$3::json)`, c.ID, key, string(p))
	return err
}

func (s *Store) DungeonPlayerDeath(ctx context.Context, l Lease, c Character, key, input string, revive bool) (uint32, error) {
	return s.dungeonPlayerDeath(ctx, l, c, key, input, revive, nil)
}

func (s *Store) ReviveAbaddon(ctx context.Context, l Lease, c Character, entry AbaddonEntry, cycle uint32) (uint32, error) {
	key := fmt.Sprintf("abaddon-revive:%s:%d", strings.ReplaceAll(entry.Run, "-", ""), cycle)
	return s.dungeonPlayerDeath(ctx, l, c, key, "", true, &entry)
}

func (s *Store) dungeonPlayerDeath(ctx context.Context, l Lease, c Character, key, input string, revive bool, abaddon *AbaddonEntry) (uint32, error) {
	return writeTx(ctx, s.db, func(tx *sql.Tx) (uint32, error) {
		current, err := dungeonCharacter(ctx, tx, l, c)
		if err != nil {
			return 0, err
		}
		prefix, result := "death:", "self-forfeit-hp0"
		if revive {
			prefix, result = "revive:", "coin:-1;hp/mp:maximum"
			if abaddon != nil {
				if err := checkAbaddonEntry(ctx, tx, current, *abaddon); err != nil {
					return 0, err
				}
				prefix, result = "abaddon-revive:", "automatic;coin:0;hp/mp:maximum"
			}
			suffix, ok := strings.CutPrefix(key, prefix)
			if !ok {
				return 0, ErrConflict
			}
			previous, err := readDungeonReceipt(ctx, tx, current, "death:"+suffix)
			if err != nil {
				return 0, err
			}
			if previous.Result != "self-forfeit-hp0" || previous.Grants == nil || len(previous.Grants) != 0 ||
				(abaddon == nil || input != "") && input != previous.Input {
				return 0, ErrConflict
			}
			input = previous.Input
		}
		var proof struct {
			Version      byte
			RunId        string
			CharacterId  uint16
			Room         *struct{ MapId uint32 }
			DeathCycle   uint32
			RelatedActor uint16
		}
		if json.Unmarshal([]byte(input), &proof) != nil || proof.Version != 1 || !matchesUUID(proof.RunId) ||
			proof.CharacterId != current.ID || proof.DeathCycle == 0 || proof.Room == nil || proof.Room.MapId == 0 ||
			proof.RelatedActor != 0 && proof.RelatedActor != current.ID ||
			key != fmt.Sprintf("%s%s:%d", prefix, strings.ReplaceAll(proof.RunId, "-", ""), proof.DeathCycle) ||
			current.Coins < 0 || current.Coins > math.MaxUint32 {
			return 0, ErrConflict
		}
		old, err := readDungeonReceipt(ctx, tx, current, key)
		if err == nil {
			if old.Input != input || old.Result != result || old.Grants == nil || len(old.Grants) != 0 {
				return 0, ErrConflict
			}
			return uint32(current.Coins), nil
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return 0, err
		}
		var profile dungeonProgression
		if err = json.Unmarshal(current.Gameplay, &profile); err != nil {
			return 0, err
		}
		if !profile.Initialized || revive && profile.Progression.Hp != 0 {
			return 0, ErrPhase
		}
		var hp int64
		mp, coins := int64(profile.Progression.Mp), current.Coins
		if revive {
			hp, mp, err = dungeonVitals(current)
			if err != nil {
				return 0, err
			}
			if abaddon == nil {
				if coins == 0 {
					return 0, ErrFunds
				}
				coins--
			}
		}
		_, err = tx.ExecContext(ctx, `UPDATE s9a1.characters SET coin=$6, revision=revision+1,
			gameplay_json=jsonb_set(gameplay_json::jsonb, '{Progression}', gameplay_json::jsonb->'Progression' ||
				jsonb_build_object('Hp',$4::bigint,'Mp',$5::bigint))::json
			WHERE account=$1 AND wire_id=$2 AND revision=$3`, l.Account, current.ID, current.Revision, hp, mp, coins)
		if err != nil {
			return 0, err
		}
		if revive {
			if _, err := tx.ExecContext(ctx, `UPDATE s9a1.characters SET
				gameplay_json=jsonb_set(gameplay_json::jsonb, '{PotionCooldowns}', '{}'::jsonb)::json
				WHERE account=$1 AND wire_id=$2`, l.Account, current.ID); err != nil {
				return 0, err
			}
		}
		return uint32(coins), putDungeonReceipt(ctx, tx, current, key, input, result)
	})
}
