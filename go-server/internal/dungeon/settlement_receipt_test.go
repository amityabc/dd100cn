package dungeon

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestSettlementEntryAndClearExperienceReceipt(t *testing.T) {
	run := &Run{Elapsed: 135, Totals: [4]uint32{2, 3, 5, 7}}
	z := ClearResult{Body: []byte{0xfb}, ElapsedMilliseconds: 135, MonsterExperienceTotals: [4]uint32{2, 3, 5, 7}}
	got, err := run.SettlementResultJSON(z)
	const entry = `{"ElapsedMilliseconds":135,"RankPoint":0,"MonsterExperienceTotals":[2,3,5,7],"ClearExperienceAward":0}`
	if err != nil || string(got) != entry {
		t.Fatalf("entry receipt %s, %v", got, err)
	}
	report := make([]byte, 42)
	frozen, err := run.ReadSettlementResult(got, report)
	if err != nil || frozen.Body == nil || frozen.ElapsedMilliseconds != 135 {
		t.Fatalf("read committed result %v", err)
	}
	report[10] = 1
	if _, err := run.ReadSettlementResult(got, report); err == nil {
		t.Fatal("changed rank accepted on replay")
	}
	z.RankPoint, z.ClearExperienceAward, z.RankExperienceAward = 99, 8, 2
	z.NamedExperience = []NamedExperience{{Code: 50123, Experience: 7}}
	z.ClearExperience = &ClearExperienceProof{Policy: "CommittedMonsterTotalClear/v1", Rate: ClearExperienceData{ResourceHash: "rate", Multiplier: json.Number("0.50")}, Before: 10, After: 20,
		Rank: &RankExperienceData{ResourceHash: "rank", Grades: []byte{99, 90, 80, 70, 60, 50, 30, 10}, Rates: []json.Number{"0", "0.1", "0.2", "0.3", "0.4"}}}
	got, err = run.SettlementResultJSON(z)
	const fields = `"RankExperienceAward":2,"NamedExperience":[{"Code":50123,"Experience":7}],"ClearExperience":{"Policy":"CommittedMonsterTotalClear/v1","Rate":{"ResourceHash":"rate","Multiplier":0.50},"Before":10,"After":20,"Rank":{"ResourceHash":"rank","Grades":[99,90,80,70,60,50,30,10],"Rates":[0,0.1,0.2,0.3,0.4]}}`
	if err != nil || !strings.Contains(string(got), fields) {
		t.Fatalf("result receipt %s, %v", got, err)
	}
}
