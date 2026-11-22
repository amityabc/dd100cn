package fate

import (
	"encoding/json"
	"testing"
	"time"

	"s9a1/server/internal/opening"
	"s9a1/server/internal/protocol"
)

func TestFateNodeReplayAndDailySettlement(t *testing.T) {
	d := Definition{EventId: Event, DungeonId: 84, ResourceHash: "resource", PointLimit: 10, SinPointLimit: 10, StoryCapacity: 6,
		Blessings: make([]RankRule, 7), Sins: make([]RankRule, 7), Rewards: make([]Reward, 5), Points: map[int][]int{3: {2, 2, 2, 2, 2}},
		Layouts: []Layout{{Nodes: map[int]Node{0: {Id: 0, Type: 3}}}}}
	for i := range d.Rewards {
		d.Rewards[i] = Reward{json.RawMessage(`{"TemplateId":123}`), 1}
	}
	p, err := Read(json.RawMessage(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	p.Run, err = d.Start("1b85d7db-a2ac-415b-aeda-95c7eb1a40d0", make([]int, 10))
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 11, 23, 1, 0, 0, 0, time.UTC)
	w := make([]byte, 19)
	le.PutUint32(w[15:], 1234567)
	v, err := ReadVisit(w)
	if err != nil {
		t.Fatal(err)
	}
	p, _, again, err := d.Visit(p, v, now)
	if err != nil || again || p.ArchangelPoints != 2 || p.StoryCount != 1 || len(p.Run.PendingRewards) != 1 {
		t.Fatalf("first node %+v %v", p, err)
	}
	if _, _, again, err = d.Visit(p, v, now); err != nil || !again {
		t.Fatalf("replay %v %v", again, err)
	}
	w[0] = 1
	other, _ := ReadVisit(w)
	if _, _, _, err = d.Visit(p, other, now); err == nil {
		t.Fatal("accepted changed node body")
	}
	p, owed, err := d.Settle(p, now, false, "live")
	if err != nil || p.Run.Settled || len(owed) != 0 {
		t.Fatal("early result settled")
	}
	p, owed, err = d.Settle(p, now, true, "return-forfeit")
	if err != nil || !p.Run.Settled || len(owed) != 1 || len(p.DailyClaims) != 1 {
		t.Fatal(p, owed, err)
	}
	_, owed, err = d.Settle(p, now, true, "return-forfeit")
	if err != nil || len(owed) != 0 {
		t.Fatal("duplicate activity mail")
	}
}

func TestLayeredActivityEntryBytes(t *testing.T) {
	d := Definition{DungeonId: 5, LoadingDungeon: opening.Dungeon{ID: 5, MazeIndex: 4, BossX: 2, BossY: 3, Start: opening.Room{MapID: 9, Monsters: []opening.Monster{{Code: 22, Level: 10, Type: 3}}}}, InstanceBuff: 100, BuffMaximumLevel: 100}
	p, err := d.EntryPackets(protocol.Actor{RosterCharacter: protocol.RosterCharacter{ID: 4, Level: 10}}, 42, 402)
	if err != nil {
		t.Fatal(err)
	}
	ops := []uint16{27, 16, 3, 28, 1774, 473, 475, 29}
	for i, op := range ops {
		if p[i].Opcode != op {
			t.Fatal("entry order", p[i])
		}
	}
	if p[3].Body[18] != 11 || len(p[7].Body) != 44 || le.Uint16(p[7].Body[25:]) != 402 || le.Uint32(p[7].Body[35:]) != 100 {
		t.Fatal("layered wire fields", p[7].Body)
	}
}
