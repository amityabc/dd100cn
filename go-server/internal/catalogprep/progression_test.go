package catalogprep

import (
	"encoding/json"
	"os"
	"reflect"
	"testing"

	"s9a1/server/internal/catalogprep/dungeons"
	"s9a1/server/internal/pvf"
	"s9a1/server/internal/questwire"
	"s9a1/server/internal/resources"
)

func TestOriginalCharacterAndProgressionTables(t *testing.T) {
	ref, pvfPath, profile := os.Getenv("S9A1_CREATION_CATALOG"), os.Getenv("S9A1_TEST_PVF"), os.Getenv("S9A1_TEST_PROFILE")
	if ref == "" || pvfPath == "" || profile == "" {
		t.Skip("set S9A1_CREATION_CATALOG, S9A1_TEST_PVF and S9A1_TEST_PROFILE to compare the original resource tables")
	}
	src, err := pvf.Open(pvfPath, profile)
	if err != nil {
		t.Fatal(err)
	}
	defer src.Close()
	data, err := os.Open(ref)
	if err != nil {
		t.Fatal(err)
	}
	defer data.Close()
	var expected struct {
		GoldCarryLimits map[byte]int64
		DailyLimits     map[uint32]byte
		OpeningCombat   struct {
			Levels      []Level
			Replacement ClassReplacement
			Jobs        map[byte]struct {
				Initial map[string]json.Number
				Growth  []GrowthCurve
				Grants  map[byte][]AdvancementSkill
			}
		}
		Characters []resources.CharacterTemplate
		Quests     struct {
			Ordinary         []questwire.Quest
			Tickets          []questwire.Ticket
			SkillDefinitions []SkillRow
			InitialSkills    map[byte][]LearnedSkill
		}
	}
	if err = json.NewDecoder(data).Decode(&expected); err != nil {
		t.Fatal(err)
	}
	gold, err := GoldCarryLimits(src)
	if err != nil || !reflect.DeepEqual(gold, expected.GoldCarryLimits) {
		t.Fatalf("gold limits: %v, %v", err, gold)
	}
	daily, err := DailyLimits(src)
	if err != nil || !reflect.DeepEqual(daily, expected.DailyLimits) {
		t.Fatalf("daily limits: %v, %v", err, daily)
	}
	levels, replacement, err := Progression(src)
	if err != nil || !reflect.DeepEqual(levels, expected.OpeningCombat.Levels) || !reflect.DeepEqual(replacement, expected.OpeningCombat.Replacement) {
		t.Fatalf("level tables: %v, levels %d, replacement %v", err, len(levels), replacement)
	}
	jobs, err := CharacterGrowthTables(src)
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range expected.Characters {
		j, ok := jobs[c.Job]
		var stats struct{ Values map[string]int64 }
		if err := json.Unmarshal(c.Stats, &stats); err != nil {
			t.Fatal(err)
		}
		if !ok || j.SourceHash != c.SourceHash || !reflect.DeepEqual(j.Stats, stats.Values) {
			t.Fatalf("creation template job %d, branch %d differs: hash %s vs %s", c.Job, c.GrowType, j.SourceHash, c.SourceHash)
		}
	}
	for job, w := range expected.OpeningCombat.Jobs {
		if !reflect.DeepEqual(jobs[job].Initial, w.Initial) || !reflect.DeepEqual(jobs[job].Growth, w.Growth) {
			t.Fatalf("job %d growth differs", job)
		}
	}
	skills, err := Skills(src)
	if err != nil {
		t.Fatal(err)
	}
	if len(skills.SkillDefinitions) != len(expected.Quests.SkillDefinitions) {
		t.Fatalf("skill definitions %d vs %d", len(skills.SkillDefinitions), len(expected.Quests.SkillDefinitions))
	}
	for i, s := range skills.SkillDefinitions {
		w := expected.Quests.SkillDefinitions[i]
		if !reflect.DeepEqual(s, w) {
			t.Fatalf("skill job %d id %d differs:\n%+v\n%+v", s.Job, s.Definition.Id, s, w)
		}
	}
	if !reflect.DeepEqual(skills.InitialSkills, expected.Quests.InitialSkills) {
		t.Fatal("job initial shortcuts differ")
	}
	for job, want := range expected.OpeningCombat.Jobs {
		if !reflect.DeepEqual(skills.Grants[job], want.Grants) {
			t.Fatalf("job %d profession/awakening grants differ", job)
		}
	}
	// Isolate creation from the independent quest compiler using its legacy reference rows.
	quests, err := questwire.New(questwire.Data{Ordinary: expected.Quests.Ordinary, Tickets: expected.Quests.Tickets})
	if err != nil {
		t.Fatal(err)
	}
	tutorials, err := dungeons.BuildOpenings(src)
	if err != nil {
		t.Fatal(err)
	}
	templates, err := CharacterTemplates(jobs, skills, levels, tutorials, quests)
	if err != nil || len(templates) != len(expected.Characters) {
		t.Fatalf("creation template count %d: %v", len(templates), err)
	}
	for i, got := range templates {
		want := expected.Characters[i]
		var g, w map[string]any
		if json.Unmarshal(got.Gameplay, &g) != nil || json.Unmarshal(want.Gameplay, &w) != nil {
			t.Fatal("invalid creation profile JSON")
		}
		if !reflect.DeepEqual(g, w) || got.Job != want.Job || got.GrowType != want.GrowType || !reflect.DeepEqual(got.OutdoorQuests, want.OutdoorQuests) || !reflect.DeepEqual(got.RoomQuests, want.RoomQuests) {
			t.Fatalf("job %d branch %d differs, outdoor %v vs %v, room %v vs %v", got.Job, got.GrowType, got.OutdoorQuests, want.OutdoorQuests, got.RoomQuests, want.RoomQuests)
		}
	}
}
