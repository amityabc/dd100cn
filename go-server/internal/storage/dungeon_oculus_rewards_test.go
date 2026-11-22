package storage

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

func TestOculusRewardLedgerRequiresExactPairedClaim(t *testing.T) {
	const owner = "3d737f2f-1db2-455f-9bb0-577a721e6f50"
	const run = "3d737f2f-1db2-455f-9bb0-577a721e6f51"
	const item = "3d737f2f-1db2-455f-9bb0-577a721e6f52"
	const week = 20261119
	const later = 20261126
	const dungeon = 100000007
	const character = 1
	hash := strings.Repeat("A", 64)
	entry := OculusEntry{Policy: OculusEntryPolicy, Owner: owner, RunId: run, Epoch: 2, CharacterId: character,
		Mode: 0, Dungeon: dungeon, Week: week, ActorFirst: 402, ModeHash: hash, DungeonHash: hash}
	clear := OculusClearQualification{Policy: OculusClearPolicy, RunId: run, Mode: 0, Dungeon: dungeon,
		ModeHash: hash, SceneHash: hash, SceneGeneration: 1, ActorFirst: 402, AdmissionInput: string(mustDungeonJSON(entry)),
		Ritual: OculusRitualProof{Policy: "OculusCommittedRitualClock/v1", RunId: run, Mode: 0, Dungeon: dungeon, ResourceHash: hash,
			StartedAt: 1, ObservedAt: 5000001, Frequency: 10000000, LimitSeconds: 120, Reductions: []OculusRitualReduction{}, RemainingMilliseconds: 119500},
		Coins: OculusClearCoins{4, 1}}
	identity := oculusSettlementIdentity{owner, 2, character, run}
	entered := oculusSettlementInput{oculusSettlementIdentity: identity, Version: OculusClearPolicy, Dungeon: dungeon,
		BossMap: 9110, BossActor: 402, ResourceHash: hash, ElapsedMilliseconds: 500, MonsterExperienceTotals: []uint32{1, 2, 3, 4},
		Body: base64.StdEncoding.EncodeToString(make([]byte, 39)), Oculus: &clear}
	var rawItem [119]uint8
	reward := map[string]any{"Policy": oculusResourcePolicy, "ResourceHash": hash, "DungeonId": dungeon, "CounterDungeonId": dungeon,
		"Limit": 1, "CycleRaw": 1, "Pool": map[string]any{"Kind": 0, "Difficulty": 4, "PartyKindRaw": 0}, "RoundCount": 1,
		"Rounds": []any{map[string]any{"SelectedRow": 0, "Container": 123, "Items": []any{map[string]any{
			"Template": ordinaryTemplate{Kind: 2, TemplateID: 90001, StackLimit: 100, FirstBagSlot: 121, LastBagSlot: 176,
				MinimumLevel: 1, UsableJobs: 1, ResourceHash: hash},
			"Count": 5, "Attributes": map[string]any{}, "Raw119": rawItem, "Detail": []uint16{},
		}}}}}
	plan := mustDungeonJSON(map[string]any{"Policy": oculusRewardPolicy, "Week": week, "Limit": 1, "Ordinal": 1, "DungeonId": dungeon, "Reward": reward})
	key := identity.key(settlementReceiptPrefix)
	resultKey := key + ":result"
	freeKey := identity.key(freeCardReceiptPrefix)
	counter := oculusRewardCounter(week)
	accepted := mustDungeonJSON(entered)
	entered.Body = base64.StdEncoding.EncodeToString(make([]byte, 42))
	fact := oculusSettlementResult{ElapsedMilliseconds: 500, MonsterExperienceTotals: []uint32{1, 2, 3, 4}}
	acceptedResult := mustDungeonJSON(fact)
	fact.OculusReward = plan
	input := mustDungeonJSON(oculusClaimInput{identity, oculusClaimVersion, resultKey, 0, plan})
	grant := []json.RawMessage{mustDungeonJSON(questGrantResult{item, 90001, 0, 121, 5})}
	rows := map[string]deathReceipt{
		entry.Key(): {Key: entry.Key(), Input: clear.AdmissionInput, Result: "admitted", Revision: 2, Grants: []json.RawMessage{}},
		key:         {Key: key, Input: string(accepted), Result: string(acceptedResult), Revision: 3, Grants: []json.RawMessage{}},
		resultKey:   {Key: resultKey, Input: string(mustDungeonJSON(entered)), Result: string(mustDungeonJSON(fact)), Revision: 4, Grants: []json.RawMessage{}},
		freeKey:     {Key: freeKey, Input: string(input), Result: string(oculusClaimResult()), Revision: 5, Grants: grant},
		counter:     {Key: counter, Input: string(input), Result: string(oculusClaimResult()), Revision: 5, Grants: grant},
	}
	for _, start := range []int64{week, later} {
		ledger, err := validateOculusRewardLedger(character, start, rows)
		if err != nil || ledger.latestWeek != start || ledger.claimRevisions[week] != 5 || (ledger.claimRevisions[ledger.latestWeek] != 0) != (start == week) {
			t.Fatalf("week %d ledger = %+v, %v", start, ledger, err)
		}
	}
	delete(rows, counter)
	if _, err := validateOculusRewardLedger(character, week, rows); !errors.Is(err, ErrConflict) {
		t.Fatalf("orphan free card = %v", err)
	}
	tooFew := []json.RawMessage{mustDungeonJSON(questGrantResult{item, 90001, 0, 121, 4})}
	for k, v := range rows {
		if k == freeKey {
			v.Grants = tooFew
			rows[k] = v
			v.Key = counter
			rows[counter] = v
		}
	}
	if _, err := validateOculusRewardLedger(character, week, rows); !errors.Is(err, ErrConflict) {
		t.Fatalf("wrong actual quantity = %v", err)
	}
	for _, k := range []string{freeKey, counter} {
		v := rows[k]
		v.Grants = grant
		rows[k] = v
	}
	delete(rows, key)
	if _, err := validateOculusRewardLedger(character, week, rows); !errors.Is(err, ErrConflict) {
		t.Fatalf("claim without committed confirmation = %v", err)
	}
}
