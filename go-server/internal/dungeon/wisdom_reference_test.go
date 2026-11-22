package dungeon

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"testing"
)

// Reference plans come from CatalogExport --wisdom-vectors and the pinned C# production catalog.
func TestWisdomOriginalRewardPlans(t *testing.T) {
	path := os.Getenv("S9A1_CREATION_CATALOG")
	if path == "" {
		t.Skip("set S9A1_CREATION_CATALOG to the prepared S9A1 resource catalog")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var catalog struct {
		Dungeons struct {
			Wisdom        WisdomCatalog
			BossEquipment BossEquipmentCatalog
		}
	}
	if err := json.Unmarshal(data, &catalog); err != nil {
		t.Fatal(err)
	}
	wisdom := &catalog.Dungeons.Wisdom
	if wisdom.Hash != "877830429C7A24B7A27590DED857FA484F10FB1BB4AEE8DE7E7BE704EB37C600" {
		t.Skipf("the C# vectors belong to another resource version, supplied hash %q", wisdom.Hash)
	}
	for _, ref := range []struct {
		seed uint32
		pool byte
		hash string
	}{
		{0, 0, "650CD9E20AFAFE479C95538ED6B4483EE8F6885030F3B89050038FBB56305185"},
		{34, 1, "7018F7017678035C5FCAB9747026B8DAE24047AD9CA6CFBCEED25A19B14C4B33"},
		{14, 2, "CEB567157D65C8F988C63D2752250F8EDCBE4EE4D513D4571625F8094133F989"},
		{2334, 3, "F21E9B292CECE47504E6F0C1F7AA3146B1DF84852102A2E6CFEF99D3F63759D6"},
	} {
		plan, err := wisdom.Plan(ref.seed, &catalog.Dungeons.BossEquipment)
		if err != nil {
			t.Fatalf("seed %d: %v", ref.seed, err)
		}
		raw, err := json.Marshal(plan)
		if err != nil {
			t.Fatal(err)
		}
		if hash := fmt.Sprintf("%X", sha256.Sum256(raw)); plan.Pool != ref.pool || hash != ref.hash {
			t.Errorf("seed %d: got pool %d and plan digest %s", ref.seed, plan.Pool, hash)
		}
	}
}
