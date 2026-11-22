package dungeon

import (
	"bytes"
	"testing"

	"s9a1/server/internal/opening"
)

func TestNativeDeathAfterGmKeepsCommittedReward(t *testing.T) {
	room := &opening.Room{Monsters: []opening.Monster{{Code: 321, Level: 1, Type: 3, BlocksRoom: true}}}
	r := &Run{ID: "70ee3bdd-c30c-4911-af58-927052538694", Character: 2, Ready: true, Room: room, bases: map[cell]uint16{{}: 402}, Dead: map[uint16]deathRecord{}}
	r.Definition.Dungeon.DisableExperience = true
	body := make([]byte, 74)
	le.PutUint32(body, 402)
	le.PutUint16(body[4:], 2)
	gm, err := r.PlanDeath(body, true)
	if err != nil || gm.Actor != 402 {
		t.Fatalf("GM report: %+v %v", gm, err)
	}
	input := `{"Source":"GameMaster","Version":"AuthorizedLocalGmKill/v1","Class":0,"Opcode":0}`
	r.ApplyDeath(gm, DeathResult{Actor: 402}, input)
	report := bytes.Clone(body)
	report[68] = 7
	replay, err := r.PlanDeath(report, false)
	if err != nil || !replay.ClientAfterGM || replay.Prior == nil || replay.PriorInput != input {
		t.Fatalf("native replay: %+v %v", replay, err)
	}
	r.ApplyDeath(replay, DeathResult{}, "changed")
	if r.Dead[402].Input != input || !bytes.Equal(r.Dead[402].Body, body) {
		t.Fatal("replay replaced GM receipt")
	}
	le.PutUint16(report[4:], 3)
	if d, _ := r.PlanDeath(report, false); d.Actor != 0 {
		t.Fatal("unrelated reporter accepted")
	}
}
