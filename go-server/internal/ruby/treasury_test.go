package ruby

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math"
	"s9a1/server/internal/savejson"
	"slices"
	"strings"
	"testing"
	"time"
)

func TestC1719FrozenSelectionAndReturnProjection(t *testing.T) {
	now, rows, revision := rubyAwardFixture()
	ledger, err := Read(rows, 11, revision, now)
	if err != nil {
		t.Fatal(err)
	}
	frozen := ledger.View
	body := selection(0, false)
	claim, err := ledger.PlanClaim(frozen, body, rubyFixtureHash)
	if err != nil || claim.Replay || !slices.Equal(claim.Proof.Items, []uint32{10312271}) {
		t.Fatalf("claim=%+v error=%v", claim, err)
	}
	const goldenKey = "ruby-treasure-v3:20260212:1FACD233A1605DD7E4FD9A953C656EC456C7E79AFF27319369A94FA42A12DD5B"
	if claim.Key != goldenKey {
		t.Fatalf("C# receipt key: got %q; want %q", claim.Key, goldenKey)
	}
	putTestReceipt(rows, claim.Key, mustTestJSON(claim.Proof), "claimed", revision+1, []Grant{testGrant(10312271)})
	current, err := Read(rows, 11, revision+1, now)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(current.View.Boxes, []uint32{math.MaxUint32, 10312272}) || current.View.Claimed != 1 {
		t.Fatalf("persisted result=%+v", current.View)
	}
	replay, err := current.PlanClaim(frozen, body, rubyFixtureHash)
	if err != nil || !replay.Replay || replay.Key != claim.Key {
		t.Fatalf("replay=%+v error=%v", replay, err)
	}
	if _, err = current.PlanClaim(frozen, selection(1, false), rubyFixtureHash); err != ErrInvalid {
		t.Fatalf("stale changed request: %v", err)
	}
	second, err := current.PlanClaim(current.View, body, rubyFixtureHash)
	if err != nil || second.Replay || second.Key == claim.Key || !slices.Equal(second.Proof.Items, []uint32{10312272}) {
		t.Fatalf("fresh view=%+v error=%v", second, err)
	}
	next, err := current.Next(9700)
	if err != nil {
		t.Fatal(err)
	}
	progress, floor, treasury, err := current.ProgressWire(next)
	if err != nil {
		t.Fatal(err)
	}
	// The matched C# Progress, Floor and Treasury writers issue these on return.
	for i, v := range []struct {
		got  []byte
		want string
	}{
		{progress, "0A0000000000000000000000"},
		{floor, "00EE2500000A"},
		{treasury, "01000000505A9D0000000000000100000000"},
	} {
		if strings.ToUpper(hex.EncodeToString(v.got)) != v.want {
			t.Errorf("N%d got %X, want %s", 1736+i, v.got, v.want)
		}
	}
	if later, err := Read(rows, 11, revision+1, now.AddDate(0, 0, 7)); err != nil || later.View.Claimed != 0 || len(later.View.Boxes) != 0 {
		t.Fatalf("new week = %+v error=%v", later.View, err)
	} else if _, err = later.PlanClaim(frozen, body, rubyFixtureHash); err != ErrInvalid {
		t.Fatalf("prior week replay: %v", err)
	}
}

func TestC1719ReadsOriginalV1AndV2Receipts(t *testing.T) {
	for _, version := range []int{1, 2} {
		t.Run(fmt.Sprint(version), func(t *testing.T) {
			now, rows, revision := rubyAwardFixture()
			body := selection(0, version == 1)
			key := "ruby-treasure"
			if version == 2 {
				key += "-v2"
			}
			key = fmt.Sprintf("%s:%d:%X", key, Week(now), sha256.Sum256(body))
			proof := Claim{Policy: fmt.Sprintf("LocalRubyTreasureSelection/v%d", version), Week: Week(now), Slots: []int{0},
				Items: []uint32{10312271}, Hash: rubyFixtureHash}
			proof.Body = savejson.String(base64.StdEncoding.EncodeToString(body))
			putTestReceipt(rows, key, mustTestJSON(proof), "claimed", revision+1, []Grant{testGrant(10312271)})
			got, err := Read(rows, 11, revision+1, now)
			if err != nil || got.View.Claimed != 1 || !slices.Equal(got.View.Boxes, []uint32{math.MaxUint32, 10312272}) {
				t.Fatalf("original history = %+v error=%v", got.View, err)
			}
		})
	}
}

const rubyFixtureHash = "0123456789ABCDEF0123456789ABCDEF0123456789ABCDEF0123456789ABCDEF"

func rubyAwardFixture() (time.Time, map[string]json.RawMessage, int64) {
	now := time.Date(2026, 2, 12, 1, 0, 0, 0, time.UTC)
	rows := map[string]json.RawMessage{}
	for i, v := range []struct {
		first, last byte
		box         uint32
	}{{1, 4, 0}, {5, 5, 10312271}, {6, 9, 0}, {10, 10, 10312272}} {
		id := fmt.Sprintf("02a08810-0000-4000-8000-%012d", i+1)
		e := Entry{Policy: Policy, Character: 11, Run: id, Requested: 9700, Dungeon: FloorID(v.first), Floor: v.first, Week: Week(now), Hash: rubyFixtureHash}
		deathKey := "dungeon-death:" + strings.ReplaceAll(id, "-", "") + ":0:1:1:402"
		advance := &Advance{Policy: RoutePolicy, First: v.first, Last: v.last, Next: FloorID(v.last + 1), Boxes: []uint32{}}
		if v.box != 0 {
			advance.Boxes = append(advance.Boxes, v.box)
		}
		proof := Clear{Policy: Policy, Character: 11, Run: id, Dungeon: e.Dungeon, Floor: e.Floor, Week: e.Week, Hash: rubyFixtureHash, Death: deathKey, Advance: advance}
		rev := int64(i*2 + 1)
		putTestReceipt(rows, EntryKey(id), mustTestJSON(e), "admitted", rev, []Grant{})
		input := fmt.Sprintf(`{"Source":"ClientDelegated","Version":"TrustedLocalClientCombat/v1","RunId":%q,"ResourceHash":%q,"Dungeon":%d,"Character":11,"Class":1,"Opcode":39}`,
			id, rubyFixtureHash, e.Dungeon)
		putTestReceipt(rows, deathKey, input, `{"RoomCleared":true,"DungeonCleared":true}`, rev+1, []Grant{})
		putTestReceipt(rows, ClearKey(e), mustTestJSON(proof), "cleared;special-rewards-disabled", rev+1, []Grant{})
	}
	return now, rows, 8
}

func selection(slot int, legacy bool) []byte {
	b := []byte{}
	if legacy {
		b = le.AppendUint32(b, 1)
	}
	b = le.AppendUint32(b, uint32(slot))
	for range 4 {
		b = le.AppendUint32(b, math.MaxUint32)
	}
	return b
}

func mustTestJSON(v any) string {
	raw, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	return string(raw)
}

func putTestReceipt(out map[string]json.RawMessage, key, input, result string, revision int64, grants []Grant) {
	out[key] = json.RawMessage(mustTestJSON(Receipt{Key: key, Input: input, Result: result, Revision: revision, Grants: grants}))
}

func testGrant(id uint32) Grant {
	return Grant{"02a08810-0000-4000-8000-000000000015", id, 0, 65, 1}
}
