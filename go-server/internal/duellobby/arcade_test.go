package duellobby

import (
	"bytes"
	"testing"

	"s9a1/server/internal/protocol"
)

func TestPracticeWire(t *testing.T) {
	// S9A1 DuelTrainingArcadeTests.PracticeWire, including the seven empty seats.
	body := []byte{0, 2, 0, 0, 0, 65, 73, 0, 0, 0, 1, 0}
	p, err := protocol.ReadArcadeCreate(body)
	if err != nil || !p.Supported || p.Kind != 1 || p.Difficulty != 0 {
		t.Fatal(p, err)
	}
	if _, err := protocol.ReadArcadeCreate(body[:11]); err == nil {
		t.Fatal("truncated title accepted")
	}
	want := make([]byte, 43)
	want[0], want[2], want[9], want[11], want[12], want[13], want[14], want[15] = 1, 1, 1, 0x74, 0xc3, 6, 1, 7
	for n := 17; n < 38; n++ {
		want[n] = 255
	}
	got, err := protocol.ArcadeRoom(1, 7, 6)
	if err != nil || !bytes.Equal(got, want) || !bytes.Equal(protocol.ArcadeLeave(1, 6), []byte{1, 0, 6, 1, 0, 255, 255, 255}) ||
		!bytes.Equal(protocol.ArcadeRoomState(1, 6, 1), []byte{1, 0, 1, 0, 0x74, 0xc3, 6, 0, 0, 0, 0}) {
		t.Fatalf("room %x, %v", got, err)
	}
}

func TestArcadeRoundReviveAndReplay(t *testing.T) {
	r := Room{Mode: protocol.ArcadeMode, Definition: &ArcadeDefinition{Rounds: 2, MaximumRevives: 1, Opponents: []uint32{41, 42}, FixedOpponents: map[byte]uint32{1: 41}, RandomOpponents: []uint32{42}},
		Training: TrainingStage{ID: 2673}}
	ready := func() bool { return true }
	start := protocol.ArcadeRound{Opponent: 41, Total: 2, Round: 1, Elapsed: 2}
	end := start
	end.Phase, end.Elapsed = 1, 3
	r.Report(start, ready)
	r.Report(end, ready)
	r.Report(end, ready)
	r.Report(start, ready)
	if r.Start != nil || !r.finished() || r.Revives != 0 {
		t.Fatal("stale start revived a lost round")
	}
	start.Elapsed = 4
	r.Report(start, ready)
	end.Won, end.Elapsed = true, 5
	r.Report(end, ready)
	start.Round = 2
	r.Report(start, ready)
	if r.Start != nil || r.Revives != 1 || r.Completed != 1 {
		t.Fatal("random pool or revival drift")
	}
	start.Opponent = 42
	r.Report(start, ready)
	end.Round, end.Opponent = 2, 42
	r.Report(end, ready)
	r.Report(end, ready)
	if !r.Qualified || !r.finished() || r.Completed != 2 {
		t.Fatal("two admitted wins did not qualify")
	}
}
