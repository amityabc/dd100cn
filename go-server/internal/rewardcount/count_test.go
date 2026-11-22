package rewardcount

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"os"
	"testing"
	"time"

	"s9a1/server/internal/storage"
)

func TestNativeDailySnapshot(t *testing.T) {
	c := storage.Character{ID: 1, Revision: 3, Gameplay: []byte(`{"Initialized":true}`)}
	limits := map[uint32]byte{ghent: 6, 100000176: 3, 100000177: 3, 100000178: 2, 100000179: 2}
	now := time.Date(2026, 10, 16, 13, 0, 0, 0, time.UTC)
	empty, err := Body(c, nil, limits, now)
	expected, _ := hex.DecodeString("0502e1f50506b0e1f50503b1e1f50503b2e1f50502b3e1f50502")
	if err != nil || !bytes.Equal(empty, expected) {
		t.Fatalf("unclaimed %x %v", empty, err)
	}
	data, err := os.ReadFile("testdata/legacy-ghent.json")
	if err != nil {
		t.Fatal(err)
	}
	var all map[string]json.RawMessage
	if err = json.Unmarshal(data, &all); err != nil {
		t.Fatal(err)
	}
	got, err := Body(c, all, limits, now)
	expected[5] = 7
	if err != nil || !bytes.Equal(got, expected) {
		t.Fatalf("old C# free card and credit: %x %v", got, err)
	}
	data, err = os.ReadFile("testdata/legacy-compound.json")
	if err != nil {
		t.Fatal(err)
	}
	if err = json.Unmarshal(data, &all); err != nil {
		t.Fatal(err)
	}
	c.Revision = 4
	got, err = Body(c, all, limits, now)
	expected[5] = 6
	if err != nil || !bytes.Equal(got, expected) {
		t.Fatalf("original compound free (gear, luck, ordinary stack), special and completion receipt: %x %v", got, err)
	}
	for k := range all {
		if len(k) > 17 && k[:17] == "dungeon-free-card" {
			delete(all, k)
		}
	}
	if _, err = Body(c, all, limits, now); err == nil {
		t.Fatal("orphaned counter accepted")
	}
	dayChange := time.Date(2026, 10, 16, 22, 0, 0, 0, time.UTC)
	got, err = Body(c, all, limits, dayChange)
	if err != nil || !bytes.Equal(got, empty) {
		t.Fatalf("past day must not change current count: %x %v", got, err)
	}
}
