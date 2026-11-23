package quest

import (
	"encoding/json"
	"slices"
	"strings"
	"testing"
)

func TestPreparedOrdinaryQuestShape(t *testing.T) {
	text := "\n[grade]\n\x60[epic]\x60\n[type]\n\x60[meet npc]\x60\n[int data]\n303\n[level]\n10 10000\n[reward type]\n\x60[item]\x60\n[reward int data]\n4365 1\n"
	q, err := parseOrdinary(111, text, hash(text))
	if err != nil || !q.Supported || q.MinimumLevel != 10 || q.MaximumLevel != 255 || q.Kind != meetNPC ||
		len(q.RewardRows) != 1 || q.RewardRows[0].Item.Template != 4365 || q.RewardRows[0].Job != -1 {
		t.Fatalf("prepared ordinary quest differs from its resource: %+v, %v", q, err)
	}
	freezeOrdinary(&q)
	data, err := json.Marshal(q)
	if err != nil {
		t.Fatal(err)
	}
	wire := string(data)
	if strings.Contains(wire, "\"GrowNumber\"") ||
		!strings.Contains(wire, "\"AcceptItems\":[]") ||
		!strings.Contains(wire, "\"Required\":[]") {
		t.Fatalf("ordinary export differs from old schema: %s", wire)
	}
}

func TestQuestTicketNativeQualification(t *testing.T) {
	const prefix = "[stackable type]\n\x60[etc]\x60 1\n[usable job]\n\x60[all]\x60\n[/usable job]\n[minimum level]\n0\n[action type]\n"
	text := prefix + "\x60[first awaken clear]\x60 1 1\n[/action type]\n"
	got, err := parseTicketItem(123, text, hash(text), nil)
	if err != nil || got.Definition.NativeAction != 119 || got.Definition.MinimumLevel != 0 ||
		!slices.Equal(got.Definition.Arguments, []uint32{1, 1}) {
		t.Fatalf("unexpected prepared first awakening ticket: %+v, %v", got, err)
	}
	text = prefix + "\x60[first awaken clear]\x60 1 0\n[/action type]\n"
	if _, err = parseTicketItem(123, text, hash(text), nil); err == nil {
		t.Fatal("unknown first awakening effect qualifiers were admitted")
	}
}
