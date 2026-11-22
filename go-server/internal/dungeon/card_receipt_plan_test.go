package dungeon

import (
	"encoding/json"
	"os"
	"reflect"
	"strings"
	"testing"

	"s9a1/server/internal/storage"
)

func TestLegacySettlementCardPlansPreserveFrozenGrant(t *testing.T) {
	raw, err := os.ReadFile("../rewardcount/testdata/legacy-compound.json")
	if err != nil {
		t.Fatal(err)
	}
	var receipts map[string]struct{ Input string }
	if err := json.Unmarshal(raw, &receipts); err != nil {
		t.Fatal(err)
	}
	var input struct{ Reward, CompletionReward json.RawMessage }
	for key, receipt := range receipts {
		if strings.HasPrefix(key, "dungeon-free-card:") {
			if err := json.Unmarshal([]byte(receipt.Input), &input); err != nil {
				t.Fatal(err)
			}
			break
		}
	}
	free, err := DecodeFreeReceiptPlan(input.Reward)
	if err != nil || free == nil {
		t.Fatalf("C# free plan: %+v, %v", free, err)
	}
	freePlan, err := free.FreeReceiptPlan()
	if err != nil {
		t.Fatal(err)
	}
	assertPlan := func(want json.RawMessage, got any) {
		t.Helper()
		raw, err := json.Marshal(got)
		if err != nil {
			t.Fatal(err)
		}
		var a, b any
		if json.Unmarshal(want, &a) != nil || json.Unmarshal(raw, &b) != nil || !reflect.DeepEqual(a, b) {
			t.Errorf("frozen plan changed:\n%s\n%s", want, raw)
		}
	}
	assertPlan(input.Reward, freePlan)
	complete, err := DecodeCompletionReceiptPlan(input.CompletionReward)
	if err != nil || complete == nil || complete.completionSnapshot == nil {
		t.Fatalf("C# completion plan: %+v, %v", complete, err)
	}
	assertPlan(input.CompletionReward, complete.completionSnapshot)
	complete.Items[0].frozen.Raw119[8] = 172
	grants, err := complete.Grants(storage.Inventory{})
	if err != nil || len(grants) != 1 || grants[0].Core[8] != 172 ||
		grants[0].Count != complete.completionSnapshot.Items[0].Count {
		t.Fatalf("frozen completion grant: %+v, %v", grants, err)
	}
}

func TestOldGoUnavailablePaidCardKeepsItsOriginalProof(t *testing.T) {
	raw := json.RawMessage(`{"Policy":"LocalNormalPaid/v1","ResourceHash":"` + strings.Repeat("A", 64) +
		`","Gold":0,"UnavailableReason":"paid-card-not-created"}`)
	card, err := DecodePaidReceiptPlan(raw)
	if err != nil || string(card.PreviousReceiptPlan()) != string(raw) {
		t.Fatalf("original paid plan: %+v, %v", card, err)
	}
	plan, err := card.PaidReceiptPlan()
	if err != nil {
		t.Fatal(err)
	}
	native, err := json.Marshal(plan)
	if err != nil {
		t.Fatal(err)
	}
	want := `{"Policy":"LocalNormalPaid/v1","ResourceHash":"` + strings.Repeat("A", 64) +
		`","Cost":0,"Gold":0,"Items":[],"UnavailableReason":"paid-card-not-created"}`
	if string(native) != want {
		t.Fatalf("native paid plan: %s", native)
	}
	frozen, err := DecodePaidReceiptPlan(native)
	if err != nil || frozen.PreviousReceiptPlan() != nil {
		t.Fatalf("native paid plan decoded as older Go: %+v, %v", frozen, err)
	}
}
