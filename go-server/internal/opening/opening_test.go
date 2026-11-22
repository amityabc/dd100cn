package opening

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"s9a1/server/internal/protocol"
	"s9a1/server/internal/storage"
)

type captureEvent struct {
	Action, Request string
	Responses       []string
	State           []struct {
		ID                                       uint16
		Revision                                 int64
		Name                                     string
		Job, Level, Town, Area, Direction, State byte
		X, Y                                     uint16
		Stats                                    string `json:"stats_json"`
		Game                                     string `json:"gameplay_json"`
	}
}

type recordedReceipt struct {
	Key, Input, Result string
	Revision           int64
}
type goldenStore struct {
	t        *testing.T
	c        storage.Character
	receipts map[string]recordedReceipt
	applied  map[string]bool
}

func (db *goldenStore) CommitTutorialDeath(_ context.Context, _ storage.Lease, p storage.TutorialDeath) (storage.TutorialDeathResult, error) {
	db.t.Helper()
	old, ok := db.receipts[p.ReceiptKey]
	if !ok || old.Input != p.Input || old.Result != p.Result {
		db.t.Fatalf("C39 receipt differs from original for %s\ninput %s\nexpected %s\nresult %s\nexpected %s", p.ReceiptKey, p.Input, old.Input, p.Result, old.Result)
	}
	if db.applied[p.ReceiptKey] {
		return storage.TutorialDeathResult{Character: db.c, Replayed: true}, nil
	}
	if db.c.Revision != p.Revision || p.Revision+1 != old.Revision {
		db.t.Fatalf("incorrect C39 revision %d/%d", p.Revision, old.Revision)
	}
	db.applied[p.ReceiptKey] = true
	db.c.Revision++
	db.c.Level, db.c.Stats, db.c.Gameplay = p.Level, p.Stats, p.Gameplay
	return storage.TutorialDeathResult{Character: db.c}, nil
}

func (db *goldenStore) CompleteTutorial(_ context.Context, _ storage.Lease, id uint16, revision int64, def storage.TutorialDefinition) (storage.Character, error) {
	if len(db.applied) != len(db.receipts) || db.c.Revision != revision || db.c.ID != id {
		db.t.Fatal("incomplete tutorial completion")
	}
	db.c.Revision++
	db.c.Position = def.ReturnPoint
	return db.c, nil
}

func TestOriginalFourRoomsAndDeathReceipts(t *testing.T) {
	var v struct {
		Definition                               Definition
		CombatRules                              CombatCatalog
		NativeTownProfile140, NativePartyNotices bool
		Combat                                   struct {
			Entry, Path []captureEvent
			Receipts    []struct {
				Data string `json:"receipts_json"`
			}
		}
	}
	capture(t, "legacy-tutorial-combat.json.gz", &v)
	db := &goldenStore{t: t, applied: map[string]bool{}}
	if err := json.Unmarshal([]byte(v.Combat.Receipts[0].Data), &db.receipts); err != nil {
		t.Fatal(err)
	}
	var lease storage.Lease
	var runID string
	for _, item := range db.receipts {
		var i receiptInput
		if err := json.Unmarshal([]byte(item.Input), &i); err != nil {
			t.Fatal(err)
		}
		lease = storage.Lease{Account: string(i.Account), Owner: i.OwnerGuid, Epoch: i.Epoch}
		runID = i.RunId
		break
	}
	var run *Run
	for _, e := range v.Combat.Entry {
		if e.Action == "loadOpening" {
			var seed uint32
			for _, s := range e.Responses {
				f := decoded(t, s, protocol.ServerToClient)
				if f.Opcode == 29 {
					seed = binary.LittleEndian.Uint32(f.Body[3:])
				}
			}
			a, _, _ := e.actor(t)
			var err error
			run, _, err = Start(v.Definition, a, seed, 402, v.NativeTownProfile140, v.NativePartyNotices)
			if err != nil {
				t.Fatal(err)
			}
			run.runID = runID
			s := e.State[0]
			db.c = storage.Character{ID: s.ID, Name: s.Name, Job: s.Job, Level: s.Level, Revision: s.Revision, Stats: json.RawMessage(s.Stats), Gameplay: json.RawMessage(s.Game)}
		}
		if e.Action == "loaded" {
			a, sp, tp := e.actor(t)
			if _, err := run.Handle(37, make([]byte, 8), a, sp, tp); err != nil {
				t.Fatal(err)
			}
		}
	}
	for ix, e := range v.Combat.Path {
		req := decoded(t, e.Request, protocol.ClientToServer)
		a, sp, tp := e.actor(t)
		var got []Packet
		var err error
		completed := false
		if req.Opcode == 39 {
			got, completed, err = run.Death(context.Background(), db, lease, db.c, req.Body, &v.CombatRules, func(storage.Character) (protocol.Actor, error) { return a, nil })
		} else {
			got, err = run.Handle(req.Opcode, req.Body, a, sp, tp)
		}
		if err != nil {
			t.Fatalf("%s: %v", e.Action, err)
		}
		want := e.Responses
		if req.Opcode == 37 && len(want) == 7 && decoded(t, want[6], protocol.ServerToClient).Opcode == 1734 {
			want = want[:6]
		}
		compare(t, e.Action, got, want)
		if completed != (ix == len(v.Combat.Path)-1) || db.c.Revision != e.State[0].Revision {
			t.Fatalf("%s completion/revision: %v/%d", e.Action, completed, db.c.Revision)
		}
	}
	if len(db.applied) != 16 {
		t.Fatalf("%d committed tutorial monster receipts", len(db.applied))
	}
}

func capture(t *testing.T, filename string, target any) {
	t.Helper()
	f, err := os.Open(filepath.Join("..", "..", "testdata", "protocol", filename))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	z, err := gzip.NewReader(f)
	if err != nil {
		t.Fatal(err)
	}
	defer z.Close()
	if err := json.NewDecoder(z).Decode(target); err != nil {
		t.Fatal(err)
	}
}

func (e captureEvent) actor(t *testing.T) (protocol.Actor, uint16, uint16) {
	t.Helper()
	if len(e.State) != 1 {
		t.Fatal("recorded actor missing")
	}
	s := e.State[0]
	var stats struct{ Values map[string]int64 }
	var game struct {
		Skills      []protocol.ActorSkill
		Progression struct {
			Experience                    uint64
			GrowType                      byte
			Sp, Tp, Fatigue, FatigueLimit uint16
		}
	}
	if err := json.Unmarshal([]byte(s.Stats), &stats); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal([]byte(s.Game), &game); err != nil {
		t.Fatal(err)
	}
	raw, err := protocol.Stats(stats.Values)
	if err != nil {
		t.Fatal(err)
	}
	p := game.Progression
	return protocol.Actor{RosterCharacter: protocol.RosterCharacter{ID: s.ID, Name: s.Name, Job: s.Job, Level: s.Level, GrowType: p.GrowType}, Stats: raw, Skills: game.Skills, SkillStyle: 255,
		Experience: p.Experience, Channel: 19, Town: s.Town, Area: s.Area, X: s.X, Y: s.Y, Direction: s.Direction, AreaState: s.State,
		FatigueUsed: p.FatigueLimit - p.Fatigue, FatigueLimit: p.FatigueLimit}, p.Sp, p.Tp
}

func decoded(t *testing.T, s string, dir protocol.Direction) protocol.Frame {
	t.Helper()
	b, err := hex.DecodeString(s)
	if err != nil {
		t.Fatal(err)
	}
	p, err := protocol.DecodeFrame(b, protocol.Game, dir)
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func compare(t *testing.T, name string, got []Packet, expect []string) {
	t.Helper()
	if len(got) != len(expect) {
		t.Fatalf("%s: %d frames, want %d", name, len(got), len(expect))
	}
	for i, p := range got {
		want := decoded(t, expect[i], protocol.ServerToClient)
		b, err := protocol.EncodeGame(protocol.ServerToClient, p.Class, p.Opcode, want.Sequence, p.Body, false)
		if err != nil || !bytes.Equal(b, want.Raw) {
			t.Fatalf("%s frame %d, got class%d/op%d, want class%d/op%d (%v)", name, i, p.Class, p.Opcode, want.Class, want.Opcode, err)
		}
	}
}

func TestOriginalOpening(t *testing.T) {
	var v struct {
		Definition                               Definition
		NativeTownProfile140, NativePartyNotices bool
		Combat                                   struct{ Entry []captureEvent }
	}
	capture(t, "legacy-tutorial-combat.json.gz", &v)
	base, next, err := v.Definition.Reserve(402)
	if err != nil || base != 402 || next != 418 {
		t.Fatalf("actor allocation = %d..%d: %v", base, next, err)
	}
	var run *Run
	for _, e := range v.Combat.Entry {
		switch e.Action {
		case "loadOpening":
			a, _, _ := e.actor(t)
			begin := -1
			var seed uint32
			for i, s := range e.Responses {
				f := decoded(t, s, protocol.ServerToClient)
				if f.Opcode == 27 {
					begin = i
				}
				if f.Opcode == 29 {
					seed = binary.LittleEndian.Uint32(f.Body[3:])
				}
			}
			if begin < 0 {
				t.Fatal("original N27 missing")
			}
			var got []Packet
			run, got, err = Start(v.Definition, a, seed, base, v.NativeTownProfile140, v.NativePartyNotices)
			if err != nil {
				t.Fatal(err)
			}
			compare(t, "native tutorial entry", got, e.Responses[begin:])
		case "bootstrapOpening", "sceneControl", "loaded", "loadedAgain", "sceneFlip", "unsupportedSceneControl":
			req := decoded(t, e.Request, protocol.ClientToServer)
			a, sp, tp := e.actor(t)
			got, err := run.Handle(req.Opcode, req.Body, a, sp, tp)
			if err != nil {
				t.Fatal(err)
			}
			want := e.Responses
			if e.Action == "loaded" && len(want) == 7 && decoded(t, want[6], protocol.ServerToClient).Opcode == 1734 {
				want = want[:6]
			}
			compare(t, e.Action, got, want)
		}
	}
	if run == nil || !run.Ready() {
		t.Fatal("first room did not complete its load")
	}
}
