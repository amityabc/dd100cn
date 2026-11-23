package host

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"errors"
	"net"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"s9a1/server/internal/dungeon"
	"s9a1/server/internal/opening"
	"s9a1/server/internal/protocol"
	"s9a1/server/internal/resources"
	"s9a1/server/internal/storage"
)

type dungeonReturnConn struct {
	net.Conn
	sent bytes.Buffer
}

func (c *dungeonReturnConn) Write(p []byte) (int, error) { return c.sent.Write(p) }

func TestOpeningDungeonReturn(t *testing.T) {
	dsn := os.Getenv("S9A1_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("set S9A1_TEST_DATABASE_URL to a disposable PostgreSQL test database")
	}
	ctx := t.Context()
	db, err := storage.Open(ctx, dsn)
	check := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	check(err)
	defer db.Close()
	check(db.Initialize(ctx))
	account := "c42" + uuid.NewString()[:8]
	lease, err := db.Acquire(ctx, account)
	check(err)
	_, err = db.List(ctx, lease)
	check(err)
	def := opening.Definition{Hash: strings.Repeat("A", 64), ReturnPoint: opening.Point{Town: 38, X: 1677, Y: 222, Direction: 5},
		Dungeon: opening.Dungeon{ID: 7115, MinimumLevel: 1, EntryFatigueCost: 4, Start: opening.Room{MapID: 1}}}
	proof := definitionProof(def)
	gameplay, err := json.Marshal(map[string]any{
		"Initialized": true, "Tutorial": proof,
		"Progression": map[string]int{"Hp": 10, "Mp": 8, "Fatigue": 100, "FatigueLimit": 156},
	})
	check(err)
	stats := json.RawMessage(`{"Values":{"hp_max":100,"mp_max":60,
		"strength":5,"vitality":5,"intelligence":5,"spirit":5,
		"fire_resistance":0,"water_resistance":0,"dark_resistance":0,"light_resistance":0,
		"inventory_limit":80000,"hp_regen_speed":0,"mp_regen_speed":0,"move_speed":100,
		"attack_speed":100,"cast_speed":100,"hit_recovery":100,"jump_power":100,"weight":100}}`)
	_, err = db.Create(ctx, lease, storage.CreateCharacter{Name: account, Job: def.Job, Opening: &proof.ReturnPoint,
		ResourceHash: def.Hash, Stats: stats, Gameplay: gameplay})
	check(err)
	character, err := db.Select(ctx, lease, 0)
	check(err)
	_, err = db.AdvanceTown(ctx, lease, false)
	check(err)
	character, err = db.EnterOpening(ctx, lease, character.ID, character.Revision, proof, time.Now(), def.Dungeon.EntryFatigueCost)
	check(err)
	actor, _, err := readActor(character, 19)
	check(err)
	conn := &dungeonReturnConn{}
	server := &Server{DB: db}
	server.itemsCatalog.Store(&resources.CharacterCatalog{Dungeons: &dungeon.Catalog{}})
	g := &game{server: server, conn: conn, lease: lease, channel: 19}
	request := protocol.Frame{Class: 1, Opcode: 42}
	checkRejected := func() {
		t.Helper()
		if err := g.handle(ctx, request); !errors.Is(err, storage.ErrPhase) || conn.sent.Len() != 0 || g.ordinary.Returning() {
			t.Fatalf("C42 phase rejection = %v, bytes = %d, recovering = %v", err, conn.sent.Len(), g.ordinary.Returning())
		}
	}
	checkRejected()
	g.opening, _, err = opening.Start(def, actor, 0, 402, false, true)
	check(err)
	checkRejected()
	_, err = g.opening.Handle(37, make([]byte, 8), actor, 0, 0)
	check(err)
	for _, sequence := range []uint16{39, 40} {
		request.Sequence = sequence
		check(g.handle(ctx, request))
		for _, want := range []struct {
			op   uint16
			body []byte
		}{{23, protocol.Area(actor)}, {24, protocol.Town(actor)}} {
			frame, err := protocol.ReadFrame(&conn.sent, protocol.Game, protocol.ServerToClient)
			check(err)
			if frame.Class != 0 || frame.Opcode != want.op || frame.Sequence != sequence || !bytes.Equal(frame.Body, want.body) {
				t.Fatalf("C42 sequence %d generated %+v, expected N%d", sequence, frame, want.op)
			}
		}
		if conn.sent.Len() != 0 || g.opening != nil || !g.ordinary.Returning() {
			t.Fatalf("C42 sequence %d did not await native town confirmation", sequence)
		}
		roster, err := db.Current(ctx, lease)
		check(err)
		saved, err := selected(roster)
		check(err)
		var p struct {
			Tutorial    struct{ Completed bool }
			Progression struct{ Hp, Mp, Fatigue int }
		}
		check(json.Unmarshal(saved.Gameplay, &p))
		if roster.Phase != storage.TownReady || saved.Revision != character.Revision+1 || saved.Position != character.Position ||
			p.Tutorial.Completed || p.Progression.Hp != 100 || p.Progression.Mp != 60 || p.Progression.Fatigue != 96 {
			t.Fatalf("C42 sequence %d persisted phase %d, revision %d, position %+v, gameplay %+v", sequence, roster.Phase, saved.Revision, saved.Position, p)
		}
	}
	t.Run("C35 recovery mailbox", func(t *testing.T) {
		catalogPath, pvfPath, profilePath := os.Getenv("S9A1_CREATION_CATALOG"), os.Getenv("S9A1_TEST_PVF"), os.Getenv("S9A1_TEST_PROFILE")
		if catalogPath == "" || pvfPath == "" || profilePath == "" {
			t.Skip("set S9A1_CREATION_CATALOG, S9A1_TEST_PVF and S9A1_TEST_PROFILE to validate town recovery")
		}
		catalog, err := resources.LoadCharacters(catalogPath, pvfPath, profilePath, 1)
		if err != nil {
			t.Fatal(err)
		}
		server.itemsCatalog.Store(catalog)
		position := binary.LittleEndian.AppendUint16(nil, proof.ReturnPoint.X)
		position = binary.LittleEndian.AppendUint16(position, proof.ReturnPoint.Y)
		position = binary.LittleEndian.AppendUint16(append(position, 5), 100)
		if err = g.handle(ctx, protocol.Frame{Class: 1, Opcode: 35, Sequence: 41, Body: position}); err != nil {
			t.Fatal(err)
		}
		var mailbox, previous uint16
		var skills bool
		for conn.sent.Len() > 0 {
			frame, err := protocol.ReadFrame(&conn.sent, protocol.Game, protocol.ServerToClient)
			if err != nil {
				t.Fatal(err)
			}
			switch frame.Opcode {
			case 97:
				if frame.Class != 0 || !bytes.Equal(frame.Body, make([]byte, 6)) {
					t.Fatalf("C35 empty mailbox page = %+v", frame)
				}
				mailbox++
			case 99:
				t.Fatal("C35 recovery sent an unread mail notification")
			case 19:
				if previous != 97 {
					t.Fatalf("C35 recovery received N%d before N19, expected N97", previous)
				}
				skills = true
			}
			previous = frame.Opcode
		}
		if mailbox != 1 || !skills || previous != 120 || g.ordinary.Returning() {
			t.Fatalf("C35 recovery mailbox pages = %d, projected skills = %v, last frame = N%d, recovering = %v", mailbox, skills, previous, g.ordinary.Returning())
		}
	})
}
