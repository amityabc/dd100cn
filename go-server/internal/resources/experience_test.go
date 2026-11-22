package resources

import (
	"encoding/json"
	"math"
	"testing"

	"s9a1/server/internal/dungeon"
	"s9a1/server/internal/opening"
	"s9a1/server/internal/questwire"
)

func TestExperienceEventScalesOnlyFinalAwards(t *testing.T) {
	const original = "3A66833D914F0B3035522C2682D68801ADA7C0FE8544DAA4311BD6C53D34C38D"
	const tripled = "8142C7AD93AF5B57EEAC5F8D2530EAF5E8257D9CA496DCF44CFE68277C3825BC"
	var data struct {
		Jobs        map[byte]opening.JobCombat
		Definitions []dungeon.Definition
		Oculus      map[uint16]dungeon.OculusMode
	}
	err := json.Unmarshal([]byte(`{"Jobs":{"0":{"Awards":[{"ByPlayerLevel":[30]}]}},"Definitions":[{"Awards":[{"Monsters":[{"ByPlayerLevel":[67]}]}]}],"Oculus":{"4":{"Awards":[{"Monsters":[{"ByPlayerLevel":[19]}]}]}}}`), &data)
	if err != nil {
		t.Fatal(err)
	}
	xp := uint32(math.MaxUint32 / 2)
	quests := []questwire.Quest{{ExperienceByLevel: []*uint32{&xp, nil}}}
	tutorial := opening.CombatCatalog{ExperienceHash: original, Jobs: data.Jobs}
	dungeons := dungeon.Data{ExperienceHash: original, Definitions: data.Definitions, Oculus: data.Oculus}
	if err := applyExperienceEvent(3, quests, &tutorial, &dungeons); err != nil {
		t.Fatal(err)
	}
	if xp != math.MaxUint32 || quests[0].ExperienceByLevel[1] != nil || tutorial.ExperienceHash != tripled || dungeons.ExperienceHash != tripled ||
		tutorial.Jobs[0].Awards[0].ByPlayerLevel[0] != 90 || dungeons.Definitions[0].Awards[0].Monsters[0].ByPlayerLevel[0] != 201 ||
		dungeons.Oculus[4].Awards[0].Monsters[0].ByPlayerLevel[0] != 57 {
		t.Fatal("experience rate changed a table or receipt differently from the original policy")
	}
}
