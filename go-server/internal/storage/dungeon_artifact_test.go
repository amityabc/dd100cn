package storage

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func TestDungeonArtifactPreservesFrozenSeedAndSlot(t *testing.T) {
	template := json.RawMessage(`{"TemplateId":70002,"Kind":2,"WornSlot":28,"MaximumDurability":24,"MinimumLevel":1,"UsableJobs":65535,"InitialSealed":true,"ResourceHash":"` + strings.Repeat("A", 64) + `"}`)
	artifact := DungeonArtifact{template, 731, 24}
	c := Character{ID: 2, Level: 1, Gameplay: json.RawMessage(`{"SpecialAssets":{"Items":[]}}`)}
	next, row, slot, err := artifact.grant(c, time.Unix(1700000000, 0))
	if err != nil || slot != 160 || artifact.verifyGrant(row, slot) != nil {
		t.Fatalf("first artifact slot=%d err=%v", slot, err)
	}
	if _, _, slot, err = artifact.grant(next, time.Unix(1700000001, 0)); err != nil || slot != 161 {
		t.Fatalf("second artifact slot=%d err=%v", slot, err)
	}
	artifact.QualitySeed++
	if artifact.verifyGrant(row, 160) == nil {
		t.Fatal("different frozen quality accepted")
	}
}
