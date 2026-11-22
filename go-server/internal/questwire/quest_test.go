package questwire

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"errors"
	"os"
	"slices"
	"strings"
	"testing"
	"time"

	"s9a1/server/internal/storage"
)

func TestRealPvfAndLegacyProjections(t *testing.T) {
	raw, err := os.ReadFile("../../.work/catalog/characters.json")
	if os.IsNotExist(err) {
		t.Skip("requires the owner's exported PVF catalog")
	}
	if err != nil {
		t.Fatal(err)
	}
	var data struct {
		Quests     Data
		Characters []struct {
			Job                       byte
			Gameplay                  json.RawMessage
			OutdoorQuests, RoomQuests []uint16
		}
	}
	if err = json.Unmarshal(raw, &data); err != nil {
		t.Fatal(err)
	}
	cat, err := New(data.Quests)
	if err != nil {
		t.Fatal(err)
	}
	if err = cat.RequireOperations(); err != nil {
		t.Fatal(err)
	}
	for i, r := range data.Characters {
		for area, ids := range [][]uint16{r.OutdoorQuests, r.RoomQuests} {
			c := storage.Character{ID: 1, Job: r.Job, Level: 1, Position: storage.Position{Town: 38, Area: uint16(area)}, Gameplay: r.Gameplay}
			packets, err := cat.Project(c, storage.Inventory{CharacterID: 1}, nil)
			if err != nil {
				t.Fatal(err)
			}
			b := packets[1].Body[8:]
			var got []uint16
			for len(b) > 0 {
				if b[0] != 32 {
					t.Fatal("invalid manual tag")
				}
				n, used := binary.Uvarint(b[1:])
				if used <= 0 {
					t.Fatal("bad quest varint")
				}
				got = append(got, uint16(n))
				b = b[1+used:]
			}
			if !slices.Equal(ids, got) {
				t.Fatalf("template %d, area %d: got %v, expected %v", i, area, got, ids)
			}
		}
	}
	var reference struct {
		Cases []struct {
			Job, Level, Grow byte
			Gameplay         json.RawMessage
			Held             []struct{ TemplateID, Count uint32 }
			Packets          []struct {
				Opcode uint16
				Body   []byte
			}
		}
	}
	raw, err = os.ReadFile("testdata/scenarios.json")
	if err != nil {
		t.Fatal(err)
	}
	if err = json.Unmarshal(raw, &reference); err != nil {
		t.Fatal(err)
	}
	for _, r := range reference.Cases {
		c := storage.Character{ID: 1, Job: r.Job, Level: r.Level, Gameplay: r.Gameplay, Position: storage.Position{Town: 38, Area: 1, X: 450, Y: 234}}
		in := storage.Inventory{CharacterID: 1, Revision: 1}
		for _, a := range r.Held {
			in.Items = append(in.Items, storage.Asset{TemplateID: a.TemplateID, Count: a.Count})
		}
		got, err := cat.Project(c, in, nil)
		if err != nil {
			t.Fatal(err)
		}
		for i, p := range r.Packets {
			if p.Opcode != got[i].Opcode || !bytes.Equal(p.Body, got[i].Body) || got[i].Compressed != (p.Opcode == 356) {
				t.Errorf("level %d job %d grow %d N%d differs", r.Level, r.Job, r.Grow, p.Opcode)
			}
		}
	}
	t.Logf("%d prepared quests, %d fresh job/branch/position comparisons, %d original C# state projections", len(data.Quests.Ordinary), len(data.Characters)*2, len(reference.Cases))
}

func TestRuntimeNeedsFrozenQuestOperations(t *testing.T) {
	cat := &Catalog{quests: []Quest{{Criterion: Criterion{Id: 5654}, Kind: seeking}}}
	check := func(want string) {
		t.Helper()
		err := cat.RequireOperations()
		if !errors.Is(err, ErrCatalogOperations) || !strings.Contains(err.Error(), want) {
			t.Fatalf("missing %q in %v", want, err)
		}
	}
	check("5654 missing Operations")
	rules := operationRules{}
	if err := json.Unmarshal([]byte(`{"Available":true,"Daily":false,"DuelTraining":false,"Grade":"side","AcceptItems":[]}`), &rules); err != nil {
		t.Fatal(err)
	}
	cat.quests[0].Operations = &rules
	check("Operations.CanAbandon")
	cat.quests[0].Operations = &operationRules{AcceptItems: []Material{{4365, 1}}}
	check("AcceptanceGrants template 4365")
}

func TestNativeQuestIdentityBoundaries(t *testing.T) {
	for _, op := range []uint16{31, 32} {
		for _, body := range [][]byte{{0x49, 0x0c}, {byte(op), 0, 0x49, 0x0c}} {
			if id, err := ReadID(op, body); err != nil || id != 3145 {
				t.Fatalf("C%d identity %x", op, body)
			}
		}
		for _, body := range [][]byte{{}, {0x49}, {0x49, 0x0c, 0}, {byte(op + 1), 0, 0x49, 0x0c}} {
			if _, err := ReadID(op, body); err == nil {
				t.Fatalf("C%d accepted malformed %x", op, body)
			}
		}
	}
}

func TestDailyChangeOnlyClearsPriorGameDay(t *testing.T) {
	cat := Catalog{ordinary: map[uint16]*Quest{2683: {Operations: &operationRules{Daily: true}}}}
	p := player{hist: history{Cleared: map[uint16]byte{2683: 1, 3145: 2}}}
	book := map[string]json.RawMessage{}
	now := time.Date(2026, 11, 22, 22, 0, 0, 0, time.UTC)
	if ok, err := cat.refresh(book, &p, false, now); err != nil || !ok || p.hist.Cleared[2683] != 1 {
		t.Fatal("first observed game day lost the imported clear flag")
	}
	if ok, err := cat.refresh(book, &p, false, now.Add(-time.Hour)); err != nil || ok {
		t.Fatal("backward clock reopened the quest")
	}
	if ok, err := cat.refresh(book, &p, false, now.Add(24*time.Hour)); err != nil || !ok || p.hist.Cleared[2683] != 0 || p.hist.Cleared[3145] != 2 {
		t.Fatal("daily roll omitted its own flags or changed another quest")
	}
}

func TestOnlyNewCommittedRangeIsAnnounced(t *testing.T) {
	q := Quest{Criterion: Criterion{Id: 105, Supported: true}, Kind: reach, Subtype: 1, AcceptanceSupported: true, Hash: "A9", Data: []int{38, 1, 400, 200, 100, 70}}
	catalog, err := New(Data{Ordinary: []Quest{q}})
	if err != nil {
		t.Fatal(err)
	}
	base := storage.Character{ID: 7, Level: 1, Gameplay: []byte(`{"Quests":{"Active":{"105":{"ObjectiveHash":"A9","Remaining":[1],"Receipts":[]}}}}`)}
	now := base
	now.Gameplay = []byte(`{"Quests":{"Active":{"105":{"ObjectiveHash":"A9","Remaining":[0],"Receipts":["town-range:105:A9:38:1:450:234"]}}}}`)
	first, err := catalog.Arrivals(base, now)
	if err != nil || len(first) != 1 || first[0].Class != 1 || first[0].Opcode != 33 || !bytes.Equal(first[0].Body, []byte{1, 105, 0, 0, 0, 0, 0}) {
		t.Fatalf("arrival %v: %v", first, err)
	}
	if repeated, err := catalog.Arrivals(now, now); err != nil || len(repeated) != 0 {
		t.Fatal("previously acknowledged arrival repeated")
	}
}
