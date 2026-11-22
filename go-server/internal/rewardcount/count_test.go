package rewardcount

import (
	"bytes"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"os"
	"strings"
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

func TestFrozenCardInventoryCore(t *testing.T) {
	source := strings.Repeat("A", 64)
	item := specialItem{
		Template: &material{TemplateId: 20021, Kind: 1, FirstBagSlot: 9, LastBagSlot: 64, WornSlot: 13,
			StackLimit: 1, MaximumDurability: 31, MinimumLevel: 100, UsableJobs: 1, ResourceHash: source, WornSlotIsNative: true},
		Count: 1, Attributes: &attributes{QualitySeed: 92, Durability: 31}, Raw119: make([]uint16, 119), Detail: []uint16{},
	}
	if err := json.Unmarshal([]byte(`{"Verified":false,"Jobs":[],"Valid":true}`), &item.Template.WeaponQualification); err != nil {
		t.Fatal(err)
	}
	item.Template.PvfGiftItem, item.Template.RelativeLifetimeSeconds = true, 3600
	deadline := int64(1792780611)
	item.Attributes.ExpiresAt = &deadline
	var rawDeadline [4]byte
	binary.LittleEndian.PutUint32(rawDeadline[:], uint32(deadline))
	for idx, value := range rawDeadline {
		item.Raw119[56+idx] = uint16(value)
	}
	if err := validFrozenItem(item, false); err != nil {
		t.Fatalf("ordinary timed item: %v", err)
	}
	item.Raw119[59]++
	if err := validFrozenItem(item, false); err == nil {
		t.Fatal("mismatched expiry accepted")
	}
	item.Raw119[59]--
	item.Attributes.Reinforcement, item.Raw119[10] = 10, 10
	if err := validFrozenItem(item, true); err != nil {
		t.Fatalf("supported reinforced reward: %v", err)
	}
	if err := validFrozenItem(item, false); err == nil {
		t.Fatal("reinforced item accepted under OrdinaryNew")
	}
	item.Attributes.Reinforcement, item.Raw119[10] = 0, 0
	item.Template.MythologyHash, item.Template.MythologyOptions = &source, 3
	item.Attributes.Mythology, _ = json.Marshal(map[string]any{
		"SourceHash": source, "Count": 3, "First": 3, "Second": 5, "Third": 7, "Fourth": 0, "Fifth": 0, "Valid": true,
	})
	copy(item.Raw119[114:], []uint16{3, 5, 7})
	item.Attributes.InitialAmplification, item.Attributes.AmplificationType, item.Attributes.AmplificationValue = true, 0x82, 17
	item.Raw119[19] = 0x80
	if err := validFrozenItem(item, true); err != nil {
		t.Fatalf("supported hidden amplification and mythology: %v", err)
	}
	item.Raw119[20] = 17
	if err := validFrozenItem(item, true); err == nil {
		t.Fatal("visible raw core leaked the hidden amplification roll")
	}
}
