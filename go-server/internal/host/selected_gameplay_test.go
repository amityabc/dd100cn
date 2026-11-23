package host

import (
	"encoding/json"
	"testing"
	"time"

	"s9a1/server/internal/storage"
)

func TestInitializeSelectedGameplayUsesSavedStats(t *testing.T) {
	at := time.Date(2026, time.November, 22, 22, 0, 0, 0, time.UTC)
	c := storage.Character{Level: 100, Gameplay: json.RawMessage(`{"Skills":[{"Id":42,"Level":2,"Slot":7}],"Other":18446744073709551615}`),
		Stats: json.RawMessage(`{"Values":{"hp_max":100,"mp_max":70,"hp":66,"exp":813,"fatigue":18,"grow_type":3}}`)}
	initial := json.RawMessage(`{"Initialized":true,"Progression":{"Sp":12},"Skills":[{"Id":2,"Level":1,"Slot":1}]}`)
	raw, err := initializeSelectedGameplay(c, initial, at)
	if err != nil {
		t.Fatal(err)
	}
	var result struct {
		actorProfile
		Other uint64
	}
	if err := json.Unmarshal(raw, &result); err != nil {
		t.Fatal(err)
	}
	if !result.Initialized || result.Progression.Sp != 12 || result.Progression.GrowType != 3 ||
		result.Progression.Experience != 813 || result.Progression.Fatigue != 18 || result.Skills[0].ID != 42 || result.Other != ^uint64(0) {
		t.Fatalf("unexpected gameplay: %s", raw)
	}
	if got, err := readGameplay(raw); err != nil || got.Progression.Hp != 66 {
		t.Fatalf("unexpected health: %+v %v", got, err)
	}
	var fields struct {
		Progression struct {
			Mp         uint32
			FatigueDay int64
		}
	}
	if err := json.Unmarshal(raw, &fields); err != nil || fields.Progression.Mp != 70 || fields.Progression.FatigueDay != 20261123 {
		t.Fatalf("unexpected mana or day: %+v %v", fields, err)
	}
}
