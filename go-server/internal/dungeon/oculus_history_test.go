package dungeon

import (
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"s9a1/server/internal/opening"
	"s9a1/server/internal/protocol"
	"s9a1/server/internal/storage"
)

func TestOculusHistoryRetainsNamedSourcesAndChecksEveryReceipt(t *testing.T) {
	room := opening.Room{MapID: 8000, Gates: 1, PassiveObjectCodes: []uint32{6000},
		Monsters: []opening.Monster{{Code: 2001, Level: 100, BlocksRoom: true}, {Code: 2002, Level: 100, Type: 3, BlocksRoom: true}}}
	var mode OculusMode
	mode.Mode, mode.Rules.ShopOpenMonsterId = 2, 2002
	mode.SourceCoinLimit, mode.SourceHash = 3, strings.Repeat("C", 64)
	mode.Rules.KillCoinLimitBonuses = []OculusValue{{Id: 2002, RawValue: 2}}
	mode.StartMaps = []OculusPosition{{}}
	var definition Definition
	definition.Dungeon.Dungeon = opening.Dungeon{ID: 400000389, MinimumLevel: 100, BossX: 1,
		Difficulty: 4, ResourceHash: strings.Repeat("A", 64), Start: room, Rooms: []opening.Room{room}}
	var a protocol.Actor
	a.ID, a.Level = 1, 100
	r, next, err := Prepare(definition, Entry{Dungeon: 400000389, Difficulty: 4}, a, 402, 7)
	if err != nil {
		t.Fatal(err)
	}
	r.OculusLayout, r.OculusGeneration = &oculusLayout{mode: mode, admission: definition}, 1
	r.OculusEntry = &storage.OculusEntry{RunId: r.ID, Mode: 2, ModeHash: mode.SourceHash, DungeonHash: definition.Dungeon.ResourceHash,
		ActorFirst: r.FirstActor(), Seed: 7}
	r.Load(a, 0, 0)
	receipts := map[string]storage.DungeonSceneReceipt{}
	record := func(key string, value oculusDeathScope, result any) string {
		in, _ := json.Marshal(value)
		out, _ := json.Marshal(result)
		receipts[key] = storage.DungeonSceneReceipt{Key: key, Input: string(in), Result: string(out), Revision: int64(len(receipts) + 2)}
		return string(in)
	}
	scope := oculusDeathScope{RunId: r.ID, ResourceHash: definition.Dungeon.ResourceHash, Dungeon: definition.Dungeon.ID, MapId: room.MapID}
	for i, monster := range room.Monsters {
		scope.Actor, scope.Code = 402+uint16(i), monster.Code
		death := Death{Actor: scope.Actor, Monster: monster, Key: fmt.Sprintf("dungeon-death:%s:0:0:0:%d", r.idKey(), scope.Actor)}
		result := r.Result(death, 5)
		r.ApplyDeath(death, result, record(death.Key, scope, result))
	}
	scope.Actor, scope.Code = 0, 6000
	passive := PassiveDeath{Code: 6000, Key: "dungeon-passive-death:" + r.idKey() + ":0:0:0:6000"}
	result := PassiveResult{Actor: passive.Code, DropRuleHash: strings.Repeat("B", 64)}
	r.ApplyPassive(passive, nil, record(passive.Key, scope, result), result)
	body := le.AppendUint16(le.AppendUint16(le.AppendUint16(nil, 12), 15), a.ID)
	playerInput, err := r.PlayerDeathInput(body, 1)
	if err != nil || !strings.Contains(playerInput, `"Room":{"X":0,"Y":0,"MapId":8000,"Monsters":`) ||
		!strings.HasSuffix(playerInput, `"DeathCycle":1,"X":12,"Y":15,"RelatedActor":1}`) {
		t.Fatalf("legacy death input was not preserved: %s %v", playerInput, err)
	}
	r.CommitPlayerDeath(body, playerInput, time.Now())
	playerKey := r.PlayerDeathReceiptKey(1)
	reviveKey := r.PlayerDeathKey(true)
	ledger := storage.OculusPlayerLedger{AdmissionRevision: 1, CharacterRevision: 8,
		Receipts: map[string]storage.DungeonSceneReceipt{playerKey: {Key: playerKey, Input: playerInput, Result: "self-forfeit-hp0", Revision: 6}}}
	if budget, err := r.OculusCoins(mode, receipts, ledger); err != nil || budget != (storage.OculusClearCoins{Total: 5}) {
		t.Fatalf("pending first death: %+v %v", budget, err)
	}
	r.Revive()
	ledger.Receipts[reviveKey] = storage.DungeonSceneReceipt{Key: reviveKey, Input: playerInput, Result: "coin:-1;hp/mp:maximum", Revision: 7}
	history, err := r.OculusHistory(receipts)
	if err != nil {
		t.Fatal(err)
	}
	resumed, _, err := r.ResumeOculus(next, a, history)
	if err != nil {
		t.Fatal(err)
	}
	if resumed.OculusGeneration != 2 || resumed.FirstActor() != next || resumed.Definition.Dungeon.ResourceHash == definition.Dungeon.ResourceHash ||
		len(resumed.Room.Monsters) != 1 || resumed.Room.Monsters[0].Code != 2001 ||
		!slices.Contains(resumed.Room.PassiveObjectCodes, uint32(6000)) || len(resumed.ground) != 0 || resumed.Totals != r.Totals {
		t.Fatal("retained scene changed the admission or carried forward transient objects")
	}
	after, err := resumed.OculusHistory(receipts)
	if err != nil || len(after.Archived) != 1 || len(after.Archived[0].Deaths) != 2 || len(after.Archived[0].Passive) != 1 {
		t.Fatalf("archive: %+v %v", after, err)
	}
	if budget, err := resumed.OculusCoins(mode, receipts, ledger); err != nil || budget != (storage.OculusClearCoins{Total: 5, Used: 1}) {
		t.Fatalf("retained revival budget: %+v %v", budget, err)
	}
	revival := ledger.Receipts[reviveKey]
	revival.Input = strings.ReplaceAll(revival.Input, `"RelatedActor":1`, `"RelatedActor":0`)
	ledger.Receipts[reviveKey] = revival
	if _, err := resumed.OculusCoins(mode, receipts, ledger); err == nil {
		t.Fatal("accepted a revival with a changed death input")
	}
	proof := receipts[passive.Key]
	proof.Result = "{}"
	receipts[passive.Key] = proof
	if _, err := resumed.OculusHistory(receipts); err == nil {
		t.Fatal("accepted a changed archived passive receipt")
	}
}
