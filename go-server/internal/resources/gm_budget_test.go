package resources

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"

	"s9a1/server/internal/itemdata"
	"s9a1/server/internal/storage"
)

func ordinaryBudgetGrant(t *testing.T, id uint32) GmGrant {
	t.Helper()
	raw, err := json.MarshalIndent(itemdata.ItemTemplate{
		TemplateId: id, ResourceHash: strings.Repeat("A", 64), Kind: 2, StackLimit: 100,
		WeaponQualification: itemdata.WeaponQualification{Verified: true, Valid: true, Jobs: []itemdata.WeaponJob{}},
	}, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	return GmGrant{Template: raw}
}

func specialBudgetGrant(id uint32) GmGrant {
	return GmGrant{Special: &storage.GiftSpecial{
		Template:  json.RawMessage(fmt.Sprintf(`{"TemplateId":%d,"ResourceHash":"%s","Kind":1}`, id, strings.Repeat("B", 64))),
		Abilities: map[byte]uint16{1: 50}, InitialPetName: "A'猫🐾",
	}}
}

func TestGmPreparedBudgetsKeepFamiliesAndViewsSeparate(t *testing.T) {
	ordinary := ordinaryBudgetGrant(t, 17)
	n, err := gmPreparedGrantSize(ordinary)
	if err != nil || n >= len(ordinary.Template) {
		t.Fatalf("budget must count compact template: %d, %v", n, err)
	}
	base := GmItems{ordinaryUsage: gmPreparedUsage{gmPreparedItemLimit - 1, gmPreparedByteLimit - n}}
	next, err := base.WithGrant(17, ordinary)
	if err != nil || next.ordinaryUsage != (gmPreparedUsage{gmPreparedItemLimit, gmPreparedByteLimit}) {
		t.Fatalf("exact limits should be accepted: %+v, %v", next.ordinaryUsage, err)
	}
	if _, exists := base.PreparedGrant(17); exists || base.ordinaryUsage.items == gmPreparedItemLimit {
		t.Fatal("published row changed prior snapshot")
	}
	if repeated, err := next.WithGrant(17, ordinary); err != nil || repeated.ordinaryUsage != next.ordinaryUsage {
		t.Fatalf("replay charged again: %+v %v", repeated.ordinaryUsage, err)
	}
	if denied, err := next.WithGrant(18, ordinaryBudgetGrant(t, 18)); !errors.Is(err, ErrGmItemBudget) || denied.ordinaryUsage != next.ordinaryUsage {
		t.Fatalf("full ordinary view accepted another grant: %+v, %v", denied.ordinaryUsage, err)
	}
	byteBound := GmItems{ordinaryUsage: gmPreparedUsage{bytes: gmPreparedByteLimit - n + 1}}
	if _, err := byteBound.WithGrant(17, ordinary); !errors.Is(err, ErrGmItemBudget) {
		t.Fatalf("ordinary byte overflow: %v", err)
	}
	if fallback, err := next.WithGrant(18, GmGrant{}); err != nil || fallback.ordinaryUsage != next.ordinaryUsage {
		t.Fatalf("missing acquisition template used budget: %+v, %v", fallback.ordinaryUsage, err)
	}
	special := specialBudgetGrant(19)
	next, err = next.WithGrant(19, special)
	if err != nil || next.specialUsage.items != 1 || next.ordinaryUsage.items != gmPreparedItemLimit {
		t.Fatalf("special grant used ordinary budget: %+v %+v %v", next.specialUsage, next.ordinaryUsage, err)
	}
}

func TestGmPreparedSpecialUsesNativePetNameEncoding(t *testing.T) {
	special := specialBudgetGrant(23)
	encoded, err := json.Marshal(special.Special)
	if err != nil {
		t.Fatal(err)
	}
	// Default System.Text.Json emits each UTF-16 unit and apostrophe as
	// six ASCII bytes. Go's uncustomized JSON uses the original UTF-8.
	expected := len(encoded) - len("'猫🐾") + len(`\u0027\u732B\uD83D\uDC3E`)
	size, err := gmPreparedGrantSize(special)
	if err != nil || size != expected {
		t.Fatalf("special charged %d bytes, expected %d (%v)", size, expected, err)
	}
	base := GmItems{specialUsage: gmPreparedUsage{bytes: gmPreparedByteLimit - size + 1}}
	if _, err := base.WithGrant(23, special); !errors.Is(err, ErrGmSpecialBudget) {
		t.Fatalf("special byte overflow: %v", err)
	}
	base.specialUsage = gmPreparedUsage{items: gmPreparedItemLimit}
	if _, err := base.WithGrant(23, special); !errors.Is(err, ErrGmSpecialBudget) {
		t.Fatalf("special count overflow: %v", err)
	}
	next, err := base.WithGrant(24, ordinaryBudgetGrant(t, 24))
	if err != nil || next.ordinaryUsage.items != 1 || next.specialUsage != base.specialUsage {
		t.Fatalf("ordinary grant consumed special budget: %+v %+v %v", next.ordinaryUsage, next.specialUsage, err)
	}
}
