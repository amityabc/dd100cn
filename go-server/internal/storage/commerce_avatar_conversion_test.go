package storage

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

func TestAvatarConversionRetainsIdentityAndDetails(t *testing.T) {
	marshal := func(v any) json.RawMessage {
		t.Helper()
		b, err := json.Marshal(v)
		if err != nil {
			t.Fatal(err)
		}
		return b
	}
	const inputID, outputID = 5001, 5002
	input := marshal(specialPurchaseTemplate{TemplateID: inputID, Kind: 0, WornSlot: 4, MinimumLevel: 1, UsableJobs: 1, ResourceHash: strings.Repeat("1", 64)})
	output := marshal(specialPurchaseTemplate{TemplateID: outputID, Kind: 0, WornSlot: 4, MinimumLevel: 1, UsableJobs: 1, ResourceHash: strings.Repeat("2", 64)})
	raw := map[string]json.RawMessage{
		"InstanceId": marshal("3d737f2f-1db2-455f-9bb0-577a721e6f50"),
		"Template":   input,
		"Core153":    marshal(make([]uint16, 153)),
		"Attributes": marshal(materialAttributes{QualitySeed: 804, Durability: 7}),
		"Avatar":     json.RawMessage(`{"DetailId":"3d737f2f-1db2-455f-9bb0-577a721e6f51","Sockets":[1,2,3],"Colors":[4,5],"Option":40031}`),
	}
	def := AvatarConversionSource{Replacements: map[uint32]uint32{inputID: outputID}}
	catalog := map[uint32]AvatarConversionTarget{inputID: {Template: input, Grade: 3, IsClone: true}, outputID: {Template: output, Grade: 3}}
	result, err := planAvatarConversion(Character{Level: 100, Job: 0}, marshal(raw), def, catalog)
	if err != nil {
		t.Fatal(err)
	}
	raw["Template"] = output
	raw["Avatar"] = json.RawMessage(`{"DetailId":"3d737f2f-1db2-455f-9bb0-577a721e6f51","Sockets":[1,2,3],"Colors":[4,5],"Option":0}`)
	if !equalJSON(result, marshal(raw)) {
		t.Fatalf("changed unrelated conversion facts: %s", result)
	}
	if _, err := planAvatarConversion(Character{Level: 100, Job: 0}, result, def, catalog); !errors.Is(err, ErrAvatarConversionInput) {
		t.Fatalf("unmapped item conversion = %v", err)
	}
}
