package duellobby

import (
	"bytes"
	"compress/gzip"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"s9a1/server/internal/opening"
	"s9a1/server/internal/protocol"
	"s9a1/server/internal/storage"
)

type saved struct {
	ID               uint16
	Name             string
	Job, Level       byte
	Created          uint32
	Revision         int64
	Town, Area, X, Y uint16
	Gameplay         string `json:"gameplay_json"`
	Stats            string `json:"stats_json"`
}

type exchange struct {
	Action, Request string
	Responses       []string
	State           []saved
	Closed          bool
}

func getFrame(t *testing.T, wire string, dir protocol.Direction) protocol.Frame {
	t.Helper()
	b, err := hex.DecodeString(wire)
	if err != nil {
		t.Fatal(err)
	}
	p, err := protocol.DecodeFrame(b, protocol.Game, dir)
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func comparePackets(t *testing.T, s exchange, offset int, packets ...opening.Packet) {
	t.Helper()
	request := getFrame(t, s.Request, protocol.ClientToServer)
	for i, p := range packets {
		if offset+i >= len(s.Responses) {
			t.Fatalf("%s missing packet %d", s.Action, offset+i)
		}
		wire, err := protocol.EncodeGame(protocol.ServerToClient, p.Class, p.Opcode, request.Sequence, p.Body, false)
		if err != nil {
			t.Fatal(err)
		}
		want := getFrame(t, s.Responses[offset+i], protocol.ServerToClient)
		if !bytes.Equal(wire, want.Raw) {
			t.Fatalf("%s packet %d expected class %d opcode %d, generated %d/%d", s.Action, offset+i, want.Class, want.Opcode, p.Class, p.Opcode)
		}
	}
}

func actor(t *testing.T, row saved, ch byte) protocol.Actor {
	t.Helper()
	var stat struct{ Values map[string]int64 }
	if err := json.Unmarshal([]byte(row.Stats), &stat); err != nil {
		t.Fatal(err)
	}
	b, err := protocol.Stats(stat.Values)
	if err != nil {
		t.Fatal(err)
	}
	c, err := Display(storage.Character{Gameplay: []byte(row.Gameplay)})
	if err != nil {
		t.Fatal(err)
	}
	var p struct {
		Skills      []protocol.ActorSkill
		Progression struct{ Fatigue, FatigueLimit uint16 }
	}
	if err := json.Unmarshal(c.Gameplay, &p); err != nil {
		t.Fatal(err)
	}
	at := Spawn()
	return protocol.Actor{RosterCharacter: protocol.RosterCharacter{ID: row.ID, Name: row.Name, Level: row.Level, Job: row.Job},
		Stats: b, SkillStyle: 255, Skills: p.Skills, Channel: ch, Town: byte(at.Town), Area: byte(at.Area), X: at.X, Y: at.Y, Direction: at.Direction,
		FatigueLimit: p.Progression.FatigueLimit, FatigueUsed: p.Progression.FatigueLimit - p.Progression.Fatigue}
}

func TestOldDuelLobbies(t *testing.T) {
	f, err := os.Open(filepath.Join("..", "..", "testdata", "protocol", "legacy-duel-lobby.json.gz"))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	z, err := gzip.NewReader(f)
	if err != nil {
		t.Fatal(err)
	}
	defer z.Close()
	var fixture map[string]json.RawMessage
	if err := json.NewDecoder(z).Decode(&fixture); err != nil {
		t.Fatal(err)
	}
	var native, party bool
	if err := json.Unmarshal(fixture["nativeTownProfile140"], &native); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(fixture["nativePartyNotices"], &party); err != nil {
		t.Fatal(err)
	}
	for _, ch := range []byte{251, 252} {
		for _, suffix := range []string{"", "LowLevel", "Roads"} {
			key := strconv.Itoa(int(ch)) + suffix
			t.Run(key, func(t *testing.T) {
				var session struct{ Steps []exchange }
				if err := json.Unmarshal(fixture[key], &session); err != nil {
					t.Fatal(err)
				}
				if len(session.Steps) < 2 {
					t.Fatal("missing two-role sample")
				}
				initial := session.Steps[0].State[0]
				for _, s := range session.Steps {
					if len(s.State) != 2 {
						t.Fatal("wrong roster size")
					}
					row := s.State[0]
					a := actor(t, row, ch)
					expectedRevision := int64(1)
					if ch == 251 && suffix == "" && (s.Action == "preflight" || s.Action == "roster" || s.Action == "select" || s.Action == "preflightBeforeSync") {
						expectedRevision = 0
					}
					if row.Revision != expectedRevision || row.Town != 38 || row.Area != 0 || row.X != 1677 || row.Y != 222 {
						t.Fatalf("%s saved revision or coordinates diverged", s.Action)
					}
					req := getFrame(t, s.Request, protocol.ClientToServer)
					switch s.Action {
					case "roster", "return":
						var rows []protocol.RosterCharacter
						for _, r := range s.State {
							rows = append(rows, protocol.RosterCharacter{ID: r.ID, Name: r.Name, Job: r.Job, Level: r.Level})
						}
						body, err := protocol.Roster(rows, 32)
						if err != nil {
							t.Fatal(err)
						}
						p := opening.Packet{Opcode: 2, Body: body}
						if s.Action == "return" {
							comparePackets(t, s, 0, opening.Packet{Class: 1, Opcode: 7, Body: protocol.Ack()}, p)
						} else {
							comparePackets(t, s, 0, p)
						}
					case "select":
						body, err := protocol.Select(protocol.SelectCharacter{ID: row.ID, Created: row.Created, FatigueLimit: a.FatigueLimit, TutorialDone: true}, 10, 0, 0, 0, true)
						if err != nil {
							t.Fatal(err)
						}
						comparePackets(t, s, 0, opening.Packet{Class: 1, Opcode: 4, Body: body}, opening.Packet{Opcode: 1378, Body: protocol.Level(row.Level)})
						if len(s.Responses) != 2 {
							t.Fatal("unexpected duel selection packet")
						}
					case "lowSelect":
						if s.State[1].Level >= MinimumLevel || !s.Closed || len(s.Responses) != 0 {
							t.Fatal("low-level selection did not close silently")
						}
					case "preflight", "preflightBeforeSync", "preflightReady", "preflightAfterReturn":
						comparePackets(t, s, 0, opening.Packet{Class: 1, Opcode: 195, Body: ChannelInfo(s.Action == "preflightReady")})
					case "bootstrap", "enterWith120", "sync", "syncAfterMove", "syncNative143":
						ready := strings.HasPrefix(s.Action, "sync")
						packets, err := Sync(ready, req.Opcode, a, 0, native, party)
						if err != nil {
							t.Fatal(err)
						}
						offset := 0
						if req.Opcode == 120 {
							offset = 1
							n := getFrame(t, s.Responses[0], protocol.ServerToClient)
							if n.Class != 0 || n.Opcode != 108 {
								t.Fatal("missing C120 event notice")
							}
						}
						comparePackets(t, s, offset, packets...)
						tail := s.Responses[offset+len(packets):]
						if ready {
							if len(tail) != 0 {
								t.Fatal("unexpected ready-time tail")
							}
						} else {
							original := []uint16{561, 21, 356, 376, 19}
							if len(original) != len(tail) {
								t.Fatal("incomplete duel loading notifications")
							}
							for i, w := range tail {
								n := getFrame(t, w, protocol.ServerToClient)
								if n.Class != 0 || n.Opcode != original[i] || n.Compressed != (n.Opcode == 356) {
									t.Fatal("duel quest/skill packet order or marker changed")
								}
							}
						}
						if s.Action == "bootstrap" && ch == 251 {
							build, err := InitialSkills(storage.Character{Gameplay: []byte(initial.Gameplay)})
							if err != nil {
								t.Fatal(err)
							}
							var p struct{ DuelSkills json.RawMessage }
							if err := json.Unmarshal([]byte(row.Gameplay), &p); err != nil {
								t.Fatal(err)
							}
							if !bytes.Equal(build, p.DuelSkills) {
								t.Fatalf("first saved duel skill page %s differs from old host %s", build, p.DuelSkills)
							}
						}
					case "move", "motion":
						if len(s.Responses) != 0 || s.Closed {
							t.Fatal("duel movement produced a packet or closed")
						}
					case "earlyButton", "button", "repeatButton", "exitRoad", "repeatExit", "toMain", "staleExit":
						tr, err := protocol.ReadTownChange(req.Body, false, 0, 0)
						if err != nil {
							t.Fatal(err)
						}
						if ch == 251 || s.Action == "earlyButton" || s.Action == "staleExit" {
							comparePackets(t, s, 0, opening.Packet{Class: 1, Opcode: 36, Body: protocol.TownChangeRejected(tr.Town, tr.Area)})
						} else {
							a.Town, a.Area, a.X, a.Y, a.Direction = byte(tr.Town), tr.Area, tr.X, tr.Y, tr.Direction
							comparePackets(t, s, 0, opening.Packet{Opcode: 23, Body: protocol.Area(a)}, opening.Packet{Opcode: 24, Body: protocol.Town(a)})
						}
					default:
						t.Fatalf("unexpected action %q", s.Action)
					}
				}
			})
		}
	}
}

func TestDuelBuildKeepsOrdinarySkills(t *testing.T) {
	base := storage.Character{Gameplay: []byte(`{"Initialized":true,"Skills":[{"Id":5,"Level":1,"Slot":0}],"Progression":{"Sp":7,"Tp":8,"Fatigue":99},"SkillStyles":{},"SkillCommands":{"5":"BA=="},"DuelSkills":{"Skills":[{"Id":8,"Level":2,"Slot":3}],"Sp":9,"Tp":10}}`)}
	before := bytes.Clone(base.Gameplay)
	got, err := Display(base)
	if err != nil {
		t.Fatal(err)
	}
	var p struct {
		Skills                                 []protocol.ActorSkill
		Progression                            struct{ Sp, Tp, Fatigue int }
		SkillStyles, SkillCommands, DuelSkills json.RawMessage
	}
	if err := json.Unmarshal(got.Gameplay, &p); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(before, base.Gameplay) || len(p.Skills) != 1 || p.Skills[0].ID != 8 || p.Progression.Sp != 9 || p.Progression.Tp != 10 || p.Progression.Fatigue != 99 || p.SkillStyles != nil || p.SkillCommands != nil || p.DuelSkills != nil {
		t.Fatal("duel display failed to preserve the active saved builds or ordinary progression")
	}
	for _, raw := range []string{`null`, `{"Initialized":false,"Skills":[],"Progression":{}}`, `{"Initialized":true,"Skills":[{"Id":1,"Level":1,"Slot":255}],"Progression":{}}`, `{"Initialized":true,"Skills":[],"CommandLocks":[1],"Progression":{}}`} {
		if _, err := InitialSkills(storage.Character{Gameplay: []byte(raw)}); err == nil {
			t.Fatalf("accepted invalid skill page %s", raw)
		}
	}
}
