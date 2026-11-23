package dungeon

import (
	"math"
	"strings"
	"testing"

	"s9a1/server/internal/opening"
)

func TestGMAPCActorSpaceUsesEveryScene(t *testing.T) {
	run := Run{ID: "run", Ready: true, seen: map[scene]bool{{}: true}}
	def := &run.Definition.Dungeon.Dungeon
	def.ID = 1
	def.Start = opening.Room{MapID: 1, PassiveObjectCodes: []uint32{65531}}
	def.Rooms = []opening.Room{{MapID: 2, X: 1, PassiveObjectCodes: []uint32{65532},
		CinematicLayer: &opening.Room{MapID: 3, X: 1, PassiveObjectCodes: []uint32{65533}}}}
	run.Room = &def.Start
	hash := strings.Repeat("A", 64)
	spawn, next, err := run.AddGameMasterAPC(65531, 1, 50, 0, hash)
	if err != nil || spawn.Actor != 65534 || next != math.MaxUint16 {
		t.Fatalf("spawn = %+v, next = %d, error = %v", spawn, next, err)
	}
	if _, _, err := run.AddGameMasterAPC(next, 1, 50, 0, hash); err == nil || err.Error() != "This connection has exhausted its dungeon actor identities" {
		t.Fatalf("exhaustion error = %v", err)
	}
	run.GmApcs = make([]GameMasterAPC, 32)
	if _, _, err := run.AddGameMasterAPC(next, 1, 50, 0, hash); err == nil || err.Error() != "本次副本已达到32个GM召唤上限，请重新进入副本" {
		t.Fatalf("limit error = %v", err)
	}
}
