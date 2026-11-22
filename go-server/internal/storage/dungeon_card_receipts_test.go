package storage

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestDungeonCardNativeReceiptIsBoundToExactPlanAndResult(t *testing.T) {
	owner, run := "acbad992-e3c5-4c26-aaf6-a957b2008004", "2d78aba2-2f8b-4d9e-b64b-b7343127ff1f"
	suffix := ":v1:acbad992e3c54c26aaf6a957b2008004:3:1:2d78aba22f8b4d9eb64bb7343127ff1f"
	paid := json.RawMessage(`{"Policy":"PVFNormalPaidGroupCard/v1","Cost":7,"Gold":0,"Items":[{"Template":{"TemplateId":3033,"Kind":2,"FirstBagSlot":121,"LastBagSlot":176},"StackCount":2}]}`)
	raw, err := readFrozenDungeonCard(paid, 0, 7, true)
	if err != nil {
		t.Fatal(err)
	}
	lease := Lease{Owner: owner, Epoch: 3}
	req := DungeonCardClaim{Key: "dungeon-empty-paid-card" + suffix, ResultKey: "dungeon-settlement" + suffix + ":result", Paid: true, Reward: paid}
	req.Input = `{"Version":"local100-paid-card-v2","Owner":"` + owner + `","Epoch":3,"CharacterId":1,"RunId":"` + run +
		`","Member":0,"ResultKey":"` + req.ResultKey + `","Body":"AQA=","Reward":` + string(paid) + `}`
	if err := validateDungeonCardClaimInput(lease, 1, req, raw); err != nil {
		t.Fatal(err)
	}
	req.Input = strings.Replace(req.Input, "AQA=", "AAA=", 1)
	if err := validateDungeonCardClaimInput(lease, 1, req, raw); err == nil {
		t.Fatal("accepted a different selector")
	}
	result, err := dungeonCardResult(true, 0, 7, raw.itemCount(), nil)
	if err != nil || result != `{"Cost":7,"Gold":0,"ItemCount":1}` || raw.validateResult(result, true, 0, 7) != nil {
		t.Fatalf("paid receipt result %q, %v", result, err)
	}
	planned, err := raw.expectedGrants(nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	granted := []questGrantResult{{InstanceId: owner, TemplateId: 3033, Location: 0, Slot: 363, Granted: 2}}
	if err := validateDungeonCardGrants(granted, planned); err != nil {
		t.Fatal(err)
	}
	granted[0].Granted--
	if err := validateDungeonCardGrants(granted, planned); err == nil {
		t.Fatal("accepted incomplete paid item")
	}
}
