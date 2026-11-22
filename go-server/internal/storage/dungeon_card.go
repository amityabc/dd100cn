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

type DungeonCardClaim struct {
	Key, ResultKey, Input string
	Paid                  bool
	Reward                json.RawMessage
	Completion            json.RawMessage
	Oculus                json.RawMessage
	Special               json.RawMessage
	PreviousReward        json.RawMessage
	PreviousCompletion    json.RawMessage
	Next                  Character
	Luck                  *DungeonLuckChange
	LuckProof             json.RawMessage
	Now                   time.Time
	Create                func(Inventory) ([]QuestGrant, error)
}

func (s *Store) ClaimDungeonCard(ctx context.Context, l Lease, c Character, p DungeonCardClaim) (DungeonLootResult, error) {
	return writeTx(ctx, s.db, func(tx *sql.Tx) (DungeonLootResult, error) {
		var out DungeonLootResult
		current, err := dungeonCharacter(ctx, tx, l, c)
		if err != nil {
			return out, err
		}
		proof, err := readDungeonReceipt(ctx, tx, current, p.ResultKey)
		if err != nil {
			return out, err
		}
		var clear struct{ FreeCardReward, PaidCardReward, CompletionReward, OculusReward, SpecialReward json.RawMessage }
		prefix := "dungeon-free-card:v1:"
		if p.Paid {
			prefix = "dungeon-empty-paid-card:v1:"
		}
		if !strings.HasPrefix(p.Key, prefix) || !strings.HasPrefix(p.ResultKey, "dungeon-settlement:v1:") ||
			!json.Valid([]byte(p.Input)) || json.Unmarshal([]byte(proof.Result), &clear) != nil || p.Create == nil {
			return out, ErrConflict
		}
		reward := clear.FreeCardReward
		if p.Paid {
			reward = clear.PaidCardReward
		}
		var amount struct{ Gold, Cost uint32 }
		reward = availableCardJSON(reward)
		storedReward := p.Reward
		if len(p.PreviousReward) != 0 {
			storedReward = p.PreviousReward
		}
		if len(reward) != 0 && (json.Unmarshal(reward, &amount) != nil || !equalJSON(reward, storedReward)) {
			return out, ErrConflict
		}
		if len(reward) == 0 && len(p.Reward) > 0 && string(p.Reward) != "null" {
			return out, ErrConflict
		}
		expectedCompletion := availableCardJSON(clear.CompletionReward)
		if p.Paid {
			expectedCompletion = nil
		}
		storedCompletion := p.Completion
		if len(p.PreviousCompletion) != 0 {
			storedCompletion = p.PreviousCompletion
		}
		if !equalJSON(storedCompletion, expectedCompletion) {
			return out, ErrConflict
		}
		frozen, err := readFrozenDungeonCard(p.Reward, amount.Gold, amount.Cost, p.Paid)
		if err != nil {
			return out, err
		}
		artifact := frozen.artifact
		var counter string
		var oculus *oculusRewardPlan
		resultJSON := ""
		if len(clear.OculusReward) != 0 {
			if p.Paid || len(reward) != 0 || !equalJSON(p.Oculus, clear.OculusReward) {
				return out, ErrConflict
			}
			_, ledger, err := readOculusRewardLedger(ctx, tx, current, p.Now)
			if err != nil {
				return out, err
			}
			claim, frozen, err := oculusClaimFromInput(current.ID, p.Input, ledger)
			if err != nil || claim.Owner != l.Owner || claim.Epoch != l.Epoch || p.Key != claim.key(freeCardReceiptPrefix) ||
				p.ResultKey != claim.ResultKey || ledger.claimRevisions[frozen.plan.Week] != 0 && !ledger.claims[p.Key] {
				return out, ErrConflict
			}
			oculus = &frozen.plan
			counter = oculusRewardCounter(oculus.Week)
			resultJSON = string(oculusClaimResult())
		} else if len(p.Oculus) != 0 {
			return out, ErrConflict
		}
		if !p.Paid && len(clear.SpecialReward) != 0 {
			var plan struct {
				Policy, ResourceHash string
				Day                  int64
				Limit, Ordinal       byte
				DungeonId, Container uint32
				Items                []json.RawMessage
			}
			var input struct{ SpecialReward json.RawMessage }
			if counter != "" || len(reward) == 0 || !equalJSON(p.Special, clear.SpecialReward) ||
				json.Unmarshal(p.Special, &plan) != nil || json.Unmarshal([]byte(p.Input), &input) != nil ||
				!equalJSON(input.SpecialReward, p.Special) || !matchesSHA(plan.ResourceHash) ||
				plan.Day < 20000101 || plan.Limit == 0 || plan.Limit > 32 || plan.Ordinal > plan.Limit ||
				plan.Items == nil || len(plan.Items) > 16 || (plan.Ordinal == 0) != (plan.Container == 0) ||
				(plan.Ordinal == 0) != (len(plan.Items) == 0) {
				return out, ErrConflict
			}
			id := plan.DungeonId
			switch id {
			case 0:
				id = 100000002
			case 100000180, 100000181:
				id = 100000179
			}
			policies := map[uint32]string{
				100000002: "LocalGhentResourceSpecial/v1", 100000176: "LocalMineOfAbyssResourceSpecial/v1",
				100000177: "LocalMemoryLandResourceSpecial/v1", 100000178: "LocalCryptOfPainResourceSpecial/v1",
				100000179: "LocalDarkTempleResourceSpecial/v1",
			}
			if plan.Policy == "" || policies[id] != plan.Policy {
				return out, ErrConflict
			}
			if plan.Ordinal != 0 {
				counter = fmt.Sprintf("dungeon-special:%d:%d:%d", id, plan.Day, plan.Ordinal)
			}
		} else if len(p.Special) != 0 {
			return out, ErrConflict
		}
		var expectations map[uint32]dungeonCardGrant
		if oculus == nil {
			if err := validateDungeonCardClaimInput(l, current.ID, p, frozen); err != nil {
				return out, err
			}
			expectations, err = frozen.expectedGrants(p.Completion, p.Special)
			if err != nil {
				return out, err
			}
			resultJSON, err = dungeonCardResult(p.Paid, amount.Gold, amount.Cost, frozen.itemCount(), p.LuckProof)
			if err != nil {
				return out, err
			}
		}
		r, err := lockAccount(ctx, tx, l, false)
		if err != nil {
			return out, err
		}
		in, err := readInventory(ctx, tx, l.Account, r)
		if err != nil {
			return out, err
		}
		previous, err := readDungeonReceipt(ctx, tx, current, p.Key)
		var grants []questGrantResult
		if err == nil {
			previousLegacy := oculus == nil && matchesEarlierDungeonCardInput(p, previous.Input)
			if previous.Revision <= proof.Revision || previous.Grants == nil ||
				!equalJSON(json.RawMessage(previous.Input), json.RawMessage(p.Input)) && !previousLegacy ||
				json.Unmarshal(mustDungeonJSON(previous.Grants), &grants) != nil {
				return out, ErrConflict
			}
			if err := verifyDungeonCardArtifact(artifact, previous.SpecialGrants); err != nil {
				return out, err
			}
			if oculus == nil {
				if err := frozen.validateResult(previous.Result, p.Paid && !previousLegacy, amount.Gold, amount.Cost); err != nil {
					return out, err
				}
				if err := validateDungeonCardGrants(grants, expectations); err != nil {
					return out, err
				}
			}
			if counter != "" {
				marker, err := readDungeonReceipt(ctx, tx, current, counter)
				if err != nil || marker.Input != previous.Input || marker.Result != previous.Result || marker.Revision != previous.Revision ||
					!equalJSON(mustDungeonJSON(marker.Grants), mustDungeonJSON(previous.Grants)) ||
					!equalJSON(marker.SpecialGrants, previous.SpecialGrants) {
					return out, ErrConflict
				}
			}
			out = dungeonPickupView(in, grants, 0)
			out.Character = current
			return out, nil
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return out, err
		}
		if oculus == nil {
			luck, err := frozen.validateLuck(p.LuckProof)
			if err != nil || (luck == nil) != (p.Luck == nil) ||
				luck != nil && (luck.CharacterLevel != current.Level || luck.Before != p.Luck.Before || luck.After != p.Luck.After) {
				return out, ErrConflict
			}
		}
		if counter != "" {
			if _, err := readDungeonReceipt(ctx, tx, current, counter); !errors.Is(err, sql.ErrNoRows) {
				return out, ErrConflict
			}
		}
		if in.Gold < int64(amount.Cost) {
			return out, ErrFunds
		}
		final := in.Gold - int64(amount.Cost) + int64(amount.Gold)
		if final > math.MaxUint32 {
			return out, ErrFunds
		}
		items, err := p.Create(in)
		if err != nil {
			return out, err
		}
		after, err := QuestLoot(in, nil, items, final, p.Now)
		if err != nil {
			return out, err
		}
		if after.AssetRevision, err = writeQuestInventory(ctx, tx, l, current, in, after); err != nil {
			return out, err
		}
		gameplay := current.Gameplay
		if p.Luck != nil {
			var prev, next dungeonProgression
			if json.Unmarshal(current.Gameplay, &prev) != nil || json.Unmarshal(p.Next.Gameplay, &next) != nil ||
				int64(prev.Progression.LuckPoints) != p.Luck.Before || int64(next.Progression.LuckPoints) != p.Luck.After ||
				p.Luck.After < 0 || p.Luck.After > p.Luck.Before || p.Luck.Before > 100000 {
				return out, ErrInvalid
			}
			if next.Progression.LuckPoints != prev.Progression.LuckPoints {
				gameplay = p.Next.Gameplay
			}
		}
		var specialGrants json.RawMessage
		if artifact != nil {
			updated := current
			updated.Gameplay = gameplay
			updated, grant, _, err := artifact.grant(updated, p.Now)
			if err != nil {
				return out, err
			}
			gameplay = updated.Gameplay
			specialGrants = json.RawMessage(mustDungeonJSON([]json.RawMessage{grant}))
		}
		if _, err := tx.ExecContext(ctx, `UPDATE s9a1.characters SET gold=$4,gameplay_json=$5::json,revision=revision+1
			WHERE account=$1 AND wire_id=$2 AND revision=$3`, l.Account, current.ID, current.Revision, after.Gold, string(gameplay)); err != nil {
			return out, err
		}
		after.Revision++
		grantJSON := QuestGranted(in, after)
		if err := json.Unmarshal(grantJSON, &grants); err != nil {
			return out, err
		}
		if oculus == nil {
			if err := validateDungeonCardGrants(grants, expectations); err != nil {
				return out, err
			}
		}
		if oculus != nil {
			var actual []json.RawMessage
			if json.Unmarshal(grantJSON, &actual) != nil || validateOculusRewardGrants(*oculus, actual) != nil {
				return out, ErrConflict
			}
		}
		raw, err := json.Marshal(struct {
			Key, Input, Result string
			Revision           int64
			Grants             json.RawMessage
			SpecialGrants      json.RawMessage `json:",omitempty"`
		}{
			p.Key, p.Input, resultJSON, after.Revision, grantJSON, specialGrants})
		if err != nil {
			return out, err
		}
		if _, err = tx.ExecContext(ctx, `INSERT INTO s9a1.reward_receipts(character_id,event_key,payload) VALUES($1,$2,$3::json)`,
			current.ID, p.Key, string(raw)); err != nil {
			return out, err
		}
		if counter != "" {
			var marker map[string]json.RawMessage
			if err := json.Unmarshal(raw, &marker); err != nil {
				return out, err
			}
			marker["Key"], _ = json.Marshal(counter)
			if _, err := tx.ExecContext(ctx, `INSERT INTO s9a1.reward_receipts(character_id,event_key,payload) VALUES($1,$2,$3::json)`,
				current.ID, counter, mustDungeonJSON(marker)); err != nil {
				return out, err
			}
		}
		out = dungeonPickupView(after, grants, 0)
		out.Character = current
		out.Character.Gameplay, out.Character.Revision, out.Character.Gold = gameplay, after.Revision, after.Gold
		return out, nil
	})
}

func verifyDungeonCardArtifact(artifact *DungeonArtifact, raw json.RawMessage) error {
	if artifact == nil {
		if len(raw) != 0 && string(raw) != "null" {
			return ErrConflict
		}
		return nil
	}
	var rows []json.RawMessage
	var grant struct{ Slot uint16 }
	if json.Unmarshal(raw, &rows) != nil || len(rows) != 1 || json.Unmarshal(rows[0], &grant) != nil {
		return ErrConflict
	}
	return artifact.verifyGrant(rows[0], grant.Slot)
}

func VerifyDungeonCardArtifact(template json.RawMessage, quality uint32, grants json.RawMessage) error {
	var t specialPurchaseTemplate
	if json.Unmarshal(template, &t) != nil {
		return ErrInvalid
	}
	return verifyDungeonCardArtifact(&DungeonArtifact{Template: template, QualitySeed: quality, Durability: t.MaximumDurability}, grants)
}
