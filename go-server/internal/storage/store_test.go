package storage

import (
	"bytes"
	"compress/gzip"
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"
	"testing"
	"time"
)

func testStore(t *testing.T) (context.Context, *Store, string) {
	t.Helper()
	dsn := os.Getenv("S9A1_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("set S9A1_TEST_DATABASE_URL to a dedicated PostgreSQL test database")
	}
	ctx := t.Context()
	db, err := Open(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	if err = db.Initialize(ctx); err != nil {
		t.Fatal(err)
	}
	var random [8]byte
	if _, err = rand.Read(random[:]); err != nil {
		t.Fatal(err)
	}
	account := "test" + hex.EncodeToString(random[:])
	t.Cleanup(func() {
		clean, stop := context.WithTimeout(context.Background(), 5*time.Second)
		defer stop()
		_, err := writeTx(clean, db.db, func(tx *sql.Tx) (struct{}, error) {
			for _, q := range []string{
				"UPDATE s9a1.accounts SET selected_id=NULL WHERE account=$1",
				"DELETE FROM s9a1.town_transition WHERE account=$1",
				"DELETE FROM s9a1.opening_entry WHERE account=$1",
				"DELETE FROM s9a1.last_inventory_move WHERE account=$1",
				"DELETE FROM s9a1.assets WHERE account=$1",
				"DELETE FROM s9a1.gold_receipts WHERE character_id IN (SELECT wire_id FROM s9a1.characters WHERE account=$1)",
				"DELETE FROM s9a1.reward_receipts WHERE character_id IN (SELECT wire_id FROM s9a1.characters WHERE account=$1)",
				"DELETE FROM s9a1.characters WHERE account=$1",
				"DELETE FROM s9a1.accounts WHERE account=$1",
			} {
				if _, err := tx.ExecContext(clean, q, account); err != nil {
					return struct{}{}, err
				}
			}
			return struct{}{}, nil
		})
		if err != nil {
			t.Errorf("cleaning this test's account: %v", err)
		}
	})
	return ctx, db, account
}

func TestOpeningAdmission(t *testing.T) {
	ctx, s, account := testStore(t)
	l, err := s.Acquire(ctx, account)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.List(ctx, l); err != nil {
		t.Fatal(err)
	}
	proof := TutorialDefinition{Dungeon: 7115, ResourceHash: strings.Repeat("B", 64), ReturnPoint: Position{Town: 38, Direction: 5}}
	p, err := json.Marshal(map[string]any{
		"Initialized": true, "Tutorial": map[string]any{"Dungeon": 7115, "Completed": false, "ResourceHash": proof.ResourceHash, "ReturnPoint": proof.ReturnPoint},
		"Progression": map[string]any{"Hp": 100, "Fatigue": 0, "FatigueLimit": 156, "FatigueDay": 20260120, "LuckPoints": 8}})
	if err != nil {
		t.Fatal(err)
	}
	c, err := s.Create(ctx, l, CreateCharacter{Name: account, Job: 0, Stats: []byte("{}"), Gameplay: p, ResourceHash: proof.ResourceHash})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.Select(ctx, l, 0); err != nil {
		t.Fatal(err)
	}
	if _, err = s.AdvanceTown(ctx, l, true); err != nil {
		t.Fatal(err)
	}
	// 2026-01-20 22:00 UTC is 2026-01-21 06:00 in the old server's day policy.
	boundary := time.Date(2026, 1, 20, 22, 0, 0, 0, time.UTC)
	if _, err = s.EnterOpening(ctx, l, c.ID, c.Revision, proof, boundary.Add(-time.Second), 1); !errors.Is(err, ErrFunds) {
		t.Fatalf("exhausted same day: %v", err)
	}
	entered, err := s.EnterOpening(ctx, l, c.ID, c.Revision, proof, boundary, 1)
	if err != nil || entered.Revision != c.Revision+1 {
		t.Fatalf("admission %v", err)
	}
	progress, err := readOpening(entered, proof)
	if err != nil || progress.Progression.FatigueDay != 20260121 || progress.Progression.Fatigue != 155 || progress.Progression.LuckPoints != 8 {
		t.Fatalf("fatigue %+v %v", progress.Progression, err)
	}
	again, err := s.EnterOpening(ctx, l, c.ID, c.Revision, proof, boundary, 1)
	if err != nil || again.Revision != entered.Revision {
		t.Fatalf("duplicate admission %v", err)
	}
	r, err := s.Snapshot(ctx, l)
	if err != nil || r.Phase != TownReady || r.AssetRevision != 0 {
		t.Fatalf("admission phase %v %v", r.Phase, err)
	}
	l, err = s.Acquire(ctx, account)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.Select(ctx, l, 0); err != nil {
		t.Fatal(err)
	}
	if _, err = s.AdvanceTown(ctx, l, true); err != nil {
		t.Fatal(err)
	}
	fresh, err := s.EnterOpening(ctx, l, c.ID, entered.Revision, proof, boundary, 0)
	if err != nil || fresh.Revision != entered.Revision+1 {
		t.Fatalf("new run at zero cost %v", err)
	}
	// Rebind the real first monster receipt to this disposable session. Its
	// layout, XP catalog, reported actor, reward and complete result stay intact.
	testOpeningDeath(t, s, l, fresh, proof)
}

func testOpeningDeath(t *testing.T, s *Store, l Lease, c Character, definition TutorialDefinition) {
	t.Helper()
	f, err := os.Open("../../testdata/protocol/legacy-tutorial-combat.json.gz")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	gz, err := gzip.NewReader(f)
	if err != nil {
		t.Fatal(err)
	}
	defer gz.Close()
	var fixture struct {
		Combat struct {
			Receipts []struct {
				JSON string `json:"receipts_json"`
			}
		}
	}
	if err = json.NewDecoder(gz).Decode(&fixture); err != nil || len(fixture.Combat.Receipts) == 0 {
		t.Fatal(err)
	}
	var ledger map[string]deathReceipt
	if err = json.Unmarshal([]byte(fixture.Combat.Receipts[0].JSON), &ledger); err != nil {
		t.Fatal(err)
	}
	var rec deathReceipt
	for k, v := range ledger {
		if strings.HasSuffix(k, ":0:0:0:402") {
			rec = v
			break
		}
	}
	var i map[string]json.RawMessage
	if err = json.Unmarshal([]byte(rec.Input), &i); err != nil {
		t.Fatal(err)
	}
	var in openingDeathInput
	if err = json.Unmarshal([]byte(rec.Input), &in); err != nil {
		t.Fatal(err)
	}
	body, err := base64.StdEncoding.DecodeString(in.Body)
	if err != nil {
		t.Fatal(err)
	}
	binary.LittleEndian.PutUint16(body[4:], c.ID)
	for k, v := range map[string]any{"Account": l.Account, "OwnerGuid": l.Owner, "Epoch": l.Epoch, "Character": c.ID, "Body": base64.StdEncoding.EncodeToString(body)} {
		i[k], err = json.Marshal(v)
		if err != nil {
			t.Fatal(err)
		}
	}
	request, err := json.Marshal(i)
	if err != nil {
		t.Fatal(err)
	}
	var game map[string]json.RawMessage
	if err = json.Unmarshal(c.Gameplay, &game); err != nil {
		t.Fatal(err)
	}
	var progress map[string]json.RawMessage
	if err = json.Unmarshal(game["Progression"], &progress); err != nil {
		t.Fatal(err)
	}
	progress["Experience"] = json.RawMessage(fmt.Sprint(in.ExperienceAward))
	game["Progression"], err = json.Marshal(progress)
	if err != nil {
		t.Fatal(err)
	}
	resultGame, err := json.Marshal(game)
	if err != nil {
		t.Fatal(err)
	}
	p := TutorialDeath{CharacterID: c.ID, Revision: c.Revision, Opening: definition, Level: c.Level, Stats: c.Stats, Gameplay: resultGame, ReceiptKey: rec.Key, Input: string(request), Result: rec.Result, DungeonResourceHash: in.ResourceHash, ExperienceHash: in.ExperienceHash}
	ctx := t.Context()
	out, err := s.CommitTutorialDeath(ctx, l, p)
	if err != nil || out.Replayed || out.Character.Revision != c.Revision+1 {
		t.Fatalf("original death receipt: %+v %v", out, err)
	}
	replay, err := s.CommitTutorialDeath(ctx, l, p)
	if err != nil || !replay.Replayed || replay.Character.Revision != out.Character.Revision {
		t.Fatalf("replayed death: %v", err)
	}
	p.Result = strings.Replace(p.Result, `"RoomCleared":false`, `"RoomCleared":true`, 1)
	if _, err = s.CommitTutorialDeath(ctx, l, p); !errors.Is(err, ErrConflict) {
		t.Fatalf("different complete result reused original key: %v", err)
	}
	saved, err := s.Receipts(ctx, l)
	var persisted deathReceipt
	if err != nil || len(saved) != 1 || json.Unmarshal(saved[rec.Key], &persisted) != nil || persisted.Input != string(request) || persisted.Result != rec.Result || persisted.Revision != c.Revision+1 {
		t.Fatalf("stored complete receipt: %+v %v", persisted, err)
	}
	raw, err := os.ReadFile("../rewardcount/testdata/legacy-ghent.json")
	if err != nil {
		t.Fatal(err)
	}
	var daily map[string]json.RawMessage
	if err = json.Unmarshal(raw, &daily); err != nil {
		t.Fatal(err)
	}
	for key, row := range daily {
		if _, err = s.db.ExecContext(ctx, "INSERT INTO s9a1.reward_receipts(character_id,event_key,payload) VALUES($1,$2,$3::json)", c.ID, key, string(row)); err != nil {
			t.Fatal(err)
		}
	}
	list, err := s.DailyRewardReceipts(ctx, l, time.Date(2026, 10, 16, 13, 0, 0, 0, time.UTC))
	if err != nil || len(list) != len(daily) {
		t.Fatalf("current credit, count, matching free card: %d %v", len(list), err)
	}
	for k := range daily {
		if _, ok := list[k]; !ok {
			t.Fatal("missing matching receipt", k)
		}
	}
	list, err = s.DailyRewardReceipts(ctx, l, time.Date(2026, 10, 16, 22, 0, 0, 0, time.UTC))
	if err != nil || len(list) != 0 {
		t.Fatalf("expired count snapshot: %d %v", len(list), err)
	}
}

func TestPostgreSQLSessionAndReward(t *testing.T) {
	ctx, s, id := testStore(t)
	l, err := s.Acquire(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.List(ctx, l); err != nil {
		t.Fatal(err)
	}
	opts := bytes.Repeat([]byte{3}, 372)
	if err = s.SaveGameOptions(ctx, l, opts); err != nil {
		t.Fatal(err)
	}
	newChar := CreateCharacter{Name: id, Job: 1, Created: 100, Stats: []byte(`{"Values":{"v":1}}`), Gameplay: []byte(`{"unused":18446744073709551615}`), ResourceHash: strings.Repeat("a", 64)}
	c, err := s.Create(ctx, l, newChar)
	if err != nil {
		t.Fatal(err)
	}
	newChar.Name = strings.ToUpper(id)
	if _, err = s.Create(ctx, l, newChar); !errors.Is(err, ErrNameTaken) {
		t.Fatalf("duplicate name: %v", err)
	}
	if _, err = s.Select(ctx, l, 0); err != nil {
		t.Fatal(err)
	}
	if _, err = s.AdvanceTown(ctx, l, true); err != nil {
		t.Fatal(err)
	}
	if _, err = s.AdvanceTown(ctx, l, false); err != nil {
		t.Fatal(err)
	}
	p := c.Position
	p.X++
	c, err = s.SavePosition(ctx, l, c.ID, c.Revision, p)
	if err != nil {
		t.Fatal(err)
	}
	m := GoldMutation{CharacterID: c.ID, Operation: "once", Delta: -3, Limit: 100}
	if _, err = s.ApplyGold(ctx, l, m); !errors.Is(err, ErrFunds) {
		t.Fatalf("overspend: %v", err)
	}
	m.Delta = 3
	result, err := s.ApplyGold(ctx, l, m)
	if err != nil || result.Gold != 3 || !result.Applied {
		t.Fatalf("credit: %+v %v", result, err)
	}
	newLease, err := s.Acquire(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.SavePosition(ctx, l, c.ID, c.Revision, p); !errors.Is(err, ErrLease) {
		t.Fatalf("old lease: %v", err)
	}
	if err = s.Release(ctx, l); err != nil {
		t.Fatal(err)
	}
	roster, err := s.List(ctx, newLease)
	if err != nil {
		t.Fatal(err)
	}
	if len(roster.Characters) != 1 || roster.Characters[0].ID != c.ID || roster.Characters[0].Position != p || !bytes.Equal(roster.GameOptions, opts) || roster.AssetRevision != 0 {
		t.Fatalf("wrong restored roster: %+v", roster)
	}
	if _, err = s.Select(ctx, newLease, 0); err != nil {
		t.Fatal(err)
	}
	result, err = s.ApplyGold(ctx, newLease, m)
	if err != nil || result.Applied || result.Gold != 3 {
		t.Fatalf("duplicate: %+v %v", result, err)
	}
	m.Delta++
	if _, err = s.ApplyGold(ctx, newLease, m); !errors.Is(err, ErrConflict) {
		t.Fatalf("changed replay: %v", err)
	}
	if err = s.Release(ctx, newLease); err != nil {
		t.Fatal(err)
	}
}

func TestPostgreSQLTutorialAndLoginRoom(t *testing.T) {
	ctx, s, account := testStore(t)
	l, err := s.Acquire(ctx, account)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.List(ctx, l); err != nil {
		t.Fatal(err)
	}
	// This profile and layout hash are from the original C5 baseline.
	def := TutorialDefinition{Job: 0, Dungeon: 7115, ResourceHash: "455D0BAD65D768C4FC881A98CAE29B024383D5520C7997FD652A8586813FC41E",
		ReturnPoint: Position{Town: 38, X: 1677, Y: 222, Direction: 5}}
	gameplay := json.RawMessage(`{"Tutorial":{"Dungeon":7115,"ResourceHash":"455D0BAD65D768C4FC881A98CAE29B024383D5520C7997FD652A8586813FC41E","ReturnPoint":{"Town":38,"Area":0,"X":1677,"Y":222,"Direction":5},"Completed":false},"Future":18446744073709551615}`)
	in := CreateCharacter{Name: account, Created: 50, Job: 0, Stats: json.RawMessage("{}"), Gameplay: gameplay, ResourceHash: strings.Repeat("F", 64)}
	c, err := s.Create(ctx, l, in)
	if err != nil {
		t.Fatal(err)
	}
	home := Position{Town: 38, Area: 1, X: 450, Y: 234, Direction: 5}
	exit := Position{Town: 38, Area: 0, X: 433, Y: 236, Direction: 5}
	plan := SelectionPlacement{CharacterID: c.ID, Revision: c.Revision, Home: home, ReturnPoint: &exit}
	if _, err = s.SelectAt(ctx, l, 0, &plan); !errors.Is(err, ErrPhase) {
		t.Fatalf("pending opening admitted to login room: %v", err)
	}
	if _, err = s.Select(ctx, l, 0); err != nil {
		t.Fatal(err)
	}
	wrong := def
	wrong.ResourceHash = strings.ToLower(wrong.ResourceHash)
	if _, err = s.CompleteTutorial(ctx, l, c.ID, c.Revision, wrong); !errors.Is(err, ErrInvalid) {
		t.Fatalf("unmatched layout: %v", err)
	}
	completed, err := s.CompleteTutorial(ctx, l, c.ID, c.Revision, def)
	if err != nil {
		t.Fatal(err)
	}
	var profile struct {
		Tutorial struct{ Completed bool }
		Future   uint64
	}
	if err = json.Unmarshal(completed.Gameplay, &profile); err != nil {
		t.Fatal(err)
	}
	if completed.Revision != c.Revision+1 || !profile.Tutorial.Completed || profile.Future != ^uint64(0) ||
		pointOnly(completed.Position) != def.ReturnPoint || completed.Position.State != 0 || completed.Position.WireTail != c.Position.WireTail || completed.RoomReturn != nil {
		t.Fatalf("wrong committed completion: %+v", completed)
	}
	if _, err = s.CompleteTutorial(ctx, l, c.ID, c.Revision, def); !errors.Is(err, ErrConflict) {
		t.Fatalf("stale tutorial revision: %v", err)
	}
	if _, err = s.CompleteTutorial(ctx, l, c.ID, completed.Revision, def); !errors.Is(err, ErrPhase) {
		t.Fatalf("repeated tutorial: %v", err)
	}
	before, err := s.Snapshot(ctx, l)
	if err != nil || before.Phase != Selected || before.AssetRevision != 0 {
		t.Fatalf("phase or shared revision changed: %v %v", before.Phase, err)
	}
	next, err := s.Acquire(ctx, account)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.SelectAt(ctx, l, 0, &plan); !errors.Is(err, ErrLease) {
		t.Fatalf("previous connection entered room: %v", err)
	}
	if _, err = s.SelectAt(ctx, next, 0, &plan); !errors.Is(err, ErrConflict) {
		t.Fatalf("stale visible revision: %v", err)
	}
	plan.Revision = completed.Revision
	arrived, err := s.SelectAt(ctx, next, 0, &plan)
	if err != nil || arrived.Revision != completed.Revision+1 || pointOnly(arrived.Position) != home || !sameReturn(arrived.RoomReturn, &exit) {
		t.Fatalf("room transition: %+v %v", arrived, err)
	}
	next, err = s.Acquire(ctx, account)
	if err != nil {
		t.Fatal(err)
	}
	plan.InRoom, plan.Revision = true, arrived.Revision
	staying, err := s.SelectAt(ctx, next, 0, &plan)
	if err != nil || staying.Revision != arrived.Revision || staying.Position != arrived.Position {
		t.Fatalf("reconnected room: %+v %v", staying, err)
	}
	if _, err = s.AdvanceTown(ctx, next, true); err != nil {
		t.Fatal(err)
	}
	if _, err = s.AdvanceTown(ctx, next, false); err != nil {
		t.Fatal(err)
	}
	left, err := s.TransitionTown(ctx, next, c.ID, staying.Revision, TownTransition{RoomReturn: true, To: exit})
	if err != nil || left.Replayed || pointOnly(left.Character.Position) != exit || !sameReturn(left.Character.RoomReturn, &exit) {
		t.Fatalf("C1418 real exit: %+v %v", left, err)
	}
	p := left.Character.Position
	p.X++
	moved, err := s.SavePosition(ctx, next, c.ID, left.Character.Revision, p)
	if err != nil {
		t.Fatal(err)
	}
	replay, err := s.TransitionTown(ctx, next, c.ID, staying.Revision, TownTransition{RoomReturn: true, ReplayOnly: true})
	if err != nil || !replay.Replayed || replay.Character.Revision != moved.Revision || replay.Character.Position != p {
		t.Fatalf("C1418 after walking: %+v %v", replay, err)
	}
	entry := TownTransition{From: pointOnly(p), To: home, RecordOrigin: true}
	type result struct {
		out TownTransitionResult
		err error
	}
	var wg sync.WaitGroup
	results := make(chan result, 2)
	for j := 0; j < 2; j++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			out, err := s.TransitionTown(ctx, next, c.ID, moved.Revision, entry)
			results <- result{out, err}
		}()
	}
	wg.Wait()
	close(results)
	changes, replays := 0, 0
	var inside Character
	for v := range results {
		if v.err != nil || v.out.From != pointOnly(p) || !sameReturn(v.out.Character.RoomReturn, &p) || v.out.Character.Revision != moved.Revision+1 {
			t.Fatalf("C36 race: %+v %v", v.out, v.err)
		}
		if v.out.Replayed {
			replays++
		} else {
			changes++
		}
		inside = v.out.Character
	}
	if changes != 1 || replays != 1 {
		t.Fatal("double C36 write", changes, replays)
	}
	roomWalk := inside.Position
	roomWalk.X++
	inside, err = s.SavePosition(ctx, next, c.ID, inside.Revision, roomWalk)
	if err != nil {
		t.Fatal(err)
	}
	entry.ReplayOnly = true
	replay, err = s.TransitionTown(ctx, next, c.ID, 0, entry)
	if err != nil || !replay.Replayed || replay.Character.Position != inside.Position || replay.From != pointOnly(p) {
		t.Fatalf("C36 replay after walking: %+v %v", replay, err)
	}
	entry.To.X++
	if _, err = s.TransitionTown(ctx, next, c.ID, inside.Revision, entry); !errors.Is(err, ErrInvalid) {
		t.Fatalf("wrong target replay: %v", err)
	}
	old := next
	next, err = s.Acquire(ctx, account)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.TransitionTown(ctx, old, c.ID, inside.Revision, entry); !errors.Is(err, ErrLease) {
		t.Fatalf("retired transition: %v", err)
	}
	if _, err = s.Select(ctx, next, 0); err != nil {
		t.Fatal(err)
	}
	if _, err = s.AdvanceTown(ctx, next, true); err != nil {
		t.Fatal(err)
	}
	if _, err = s.AdvanceTown(ctx, next, false); err != nil {
		t.Fatal(err)
	}
	entry.To.X--
	if _, err = s.TransitionTown(ctx, next, c.ID, inside.Revision, entry); !errors.Is(err, ErrInvalid) {
		t.Fatalf("previous connection replay: %v", err)
	}
	if err = s.Release(ctx, next); err != nil {
		t.Fatal(err)
	}
}
