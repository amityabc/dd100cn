package dungeon

import (
	"testing"

	"s9a1/server/internal/opening"
	"s9a1/server/internal/storage"
)

func TestOculusRescuedMerchantPlacement(t *testing.T) {
	const rescue uint32 = 100
	const key = "dungeon-death:9c0c3f0927e74c55b86587edb92c6f663:0:0:0:402"
	mode := OculusMode{SourceHash: "qualified mode"}
	mode.Rules.ShopOpenMonsterId = rescue
	mode.Rules.Kaiju = []OculusValue{{Id: rescue}, {Id: rescue + 1}}
	rescueCell := oculusCell{Position: OculusPosition{0, 0, 0}, Kind: 3, Monster: rescue}
	rooms := map[cell]*opening.Room{
		{0, 0}: {Monsters: []opening.Monster{{Code: rescue}}},
		{1, 0}: {X: 1},
		{5, 0}: {X: 5},
	}
	run := &Run{Ready: true, Room: rooms[cell{0, 0}], rooms: rooms, bases: map[cell]uint16{{0, 0}: 402},
		visited: map[cell]bool{{0, 0}: true, {1, 0}: true, {5, 0}: true}, Dead: map[uint16]deathRecord{},
		OculusEntry: &storage.OculusEntry{}, OculusGeneration: 1,
		OculusLayout: &oculusLayout{mode: mode, cells: []oculusCell{rescueCell}}}
	if _, _, ok := run.OculusRescue(mode); ok {
		t.Fatal("admitted before the rescue death")
	}
	run.Dead[402] = deathRecord{Key: key}
	for _, tc := range []struct {
		position cell
		allowed  bool
	}{{cell{0, 0}, true}, {cell{1, 0}, false}, {cell{5, 0}, true}} {
		run.Room = rooms[tc.position]
		p, got, ok := run.OculusRescue(mode)
		if ok != tc.allowed || ok && (p != rescueCell.Position || got != key) {
			t.Fatalf("at %v got %v %q %t", tc.position, p, got, ok)
		}
	}
	delete(run.Dead, 402)
	run.OculusLayout.retired = map[oculusCell]string{rescueCell: key}
	run.OculusGeneration++
	if _, got, ok := run.OculusRescue(mode); !ok || got != key {
		t.Fatal("resumed scene discarded the original rescue proof")
	}
	run.Ready = false
	if _, _, ok := run.OculusRescue(mode); ok {
		t.Fatal("admitted before scene load")
	}
}
