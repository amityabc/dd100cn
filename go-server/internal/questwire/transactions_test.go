package questwire

import (
	"bytes"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"s9a1/server/internal/storage"
)

func TestQuestionTriggerCompletionAndReplay(t *testing.T) {
	id := uint16(3868)
	xp := uint32(733290)
	quest := Quest{Criterion: Criterion{Id: id, MinimumLevel: 85, MaximumLevel: 255, Job: -1, Grow: -1, Supported: true}, Hash: "question-source",
		Kind: question, QuestionChoices: 3, AcceptanceSupported: true, NpcRangeTarget: &NpcTarget{NpcId: 331, Town: 22, Area: 3, X: 1369, Y: 223},
		Operations: &operationRules{Available: true}, ExperienceByLevel: make([]*uint32, 100)}
	quest.ExperienceByLevel[84] = &xp
	cat, err := New(Data{Ordinary: []Quest{quest}})
	if err != nil {
		t.Fatal(err)
	}
	c := storage.Character{ID: 1, Level: 85, Revision: 10, Position: storage.Position{Town: 22, Area: 3}, Gameplay: json.RawMessage(`{"Initialized":true,"Progression":{"Sp":2,"Tp":3,"GrowType":0},"Quests":{"Active":{"3868":{"ObjectiveHash":"question-source","Remaining":[1],"Receipts":[]}},"Cleared":{}}}`)}
	in := storage.Inventory{CharacterID: 1, Revision: 10}
	confirmed := false
	opts := ChangeContext{Now: time.Unix(1700000000, 0), HasNPC: func(c storage.Character, n NpcTarget) (bool, error) {
		confirmed = c.Position.Town == 22 && c.Position.Area == 3 && n.NpcId == 331
		return confirmed, nil
	}}
	r, answer, err := cat.ReadTrigger([]byte{33, 0, byte(id & 255), byte(id >> 8), 0, 2})
	if err != nil || !answer {
		t.Fatalf("native question body: %v", err)
	}
	a, fact, err := cat.Trigger(c, in, nil, r, opts)
	if err != nil || !confirmed || a.Full || fact.Revision != 11 || !bytes.Equal(a.Ack, []byte{1, byte(id & 255), byte(id >> 8), 0, 0, 0, 0}) {
		t.Fatalf("question confirmation: %+v %v %v", a, fact, err)
	}
	var state skillPage
	_ = json.Unmarshal(c.Gameplay, &state)
	state["Quests"] = a.Mutation.Quests
	c.Gameplay, _ = json.Marshal(state)
	c.Revision, in.Revision = 11, 11
	changed := r
	changed.Trigger = 1
	if _, _, err = cat.Trigger(c, in, nil, changed, opts); !errors.Is(err, storage.ErrInvalid) {
		t.Fatalf("changing a committed choice: %v", err)
	}
	called := 0
	completion := CompleteContext{ChangeContext: opts, GoldCarryLimits: map[byte]int64{85: 1000000}, Growth: func(c storage.Character, a uint32, _ uint16, _ byte, _ bool) (storage.Character, uint32, error) {
		called++
		return c, a, nil
	}}
	req := CompleteRequest{id, 65535, 1, 65535}
	claim, err := cat.Complete(c, in, nil, req, completion)
	if err != nil || claim.Experience != xp || claim.Mutation.Reward == nil || called != 1 {
		t.Fatalf("completion: %+v %v", claim, err)
	}
	p, err := playerOf(claim.Mutation.Reward.Character)
	if err != nil || p.hist.Cleared[id] != 3 || len(p.hist.Active) != 0 {
		t.Fatalf("committed choice: %+v %v", p, err)
	}
	c = claim.Mutation.Reward.Character
	c.Revision, in.Revision = 12, 12
	replayed, err := cat.Complete(c, in, claim.Mutation.Reward.Receipts, req, completion)
	if err != nil || replayed.Mutation.Reward != nil || called != 1 || !bytes.Equal(replayed.Ack, claim.Ack) {
		t.Fatalf("receipt replay: %+v %v", replayed, err)
	}
}

func TestNativeSkillPurchaseThenResetUsesSavedSP(t *testing.T) {
	d := skillDefinition{Id: 80, RequiredLevel: 1, Active: true, OrdinarySp: true, Maximum: []int{15}, Costs: []int{5}}
	cat, err := New(Data{Ordinary: []Quest{{Criterion: Criterion{Id: 1}, Hash: "source"}}, SkillDefinitions: []effectSkill{{0, d}}, InitialSkills: map[byte][]questSkill{0: {}}})
	if err != nil {
		t.Fatal(err)
	}
	c := storage.Character{ID: 1, Level: 20, Gameplay: json.RawMessage(`{"Initialized":true,"Progression":{"Sp":31,"Tp":2,"GrowType":0},"Skills":[],"Quests":{"Active":{},"Cleared":{}}}`)}
	r, err := ReadSkill(29, []byte{0, 1, 80, 0, 0, 3, 0})
	if err != nil {
		t.Fatal(err)
	}
	learned, err := cat.PlanSkill(c, r, SkillContext{})
	if err != nil || !learned.ProjectionAfter || !bytes.Equal(learned.Ack, []byte{1, 0, 16, 0, 2, 0, 1, 0, 80, 0, 3, 0, 0}) {
		t.Fatalf("training: %x %v", learned.Ack, err)
	}
	c.Gameplay = learned.Mutation
	r, err = ReadSkill(483, []byte{0, 0})
	if err != nil {
		t.Fatal(err)
	}
	reset, err := cat.PlanSkill(c, r, SkillContext{})
	if err != nil || !reset.ProjectionBefore || !bytes.Equal(reset.Ack, []byte{1, 0, 0}) {
		t.Fatalf("reset: %x %v", reset.Ack, err)
	}
	var saved struct {
		Progression skillProgress
		Skills      []questSkill
	}
	if err = json.Unmarshal(reset.Mutation, &saved); err != nil || len(saved.Skills) != 0 || saved.Progression.Sp != 31 || saved.Progression.Tp != 2 {
		t.Fatalf("reset saved state: %+v %v", saved, err)
	}
}
