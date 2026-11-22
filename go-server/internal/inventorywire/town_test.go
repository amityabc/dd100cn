package inventorywire

import (
	"bytes"
	"compress/gzip"
	"encoding/hex"
	"encoding/json"
	"os"
	"testing"
	"time"

	"s9a1/server/internal/protocol"
	"s9a1/server/internal/storage"
)

func TestCapturedTownAssets(t *testing.T) {
	file, err := os.Open("../../testdata/protocol/legacy-town.json.gz")
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	z, err := gzip.NewReader(file)
	if err != nil {
		t.Fatal(err)
	}
	defer z.Close()
	type step struct {
		Action    string
		Responses []string
	}
	var fixture struct{ Opening, Town []step }
	if err = json.NewDecoder(z).Decode(&fixture); err != nil {
		t.Fatal(err)
	}
	character := storage.Character{ID: 1, Gameplay: []byte("{}")}
	in := storage.Inventory{CharacterID: 1, PersonalCapacity: 152, Coins: 10}
	for _, sample := range []struct {
		steps       []step
		action      string
		first, last int
	}{{fixture.Opening, "loadOpening", 7, 32}, {fixture.Town, "loadTown", 11, 36}} {
		var frames []string
		for _, s := range sample.steps {
			if s.Action == sample.action {
				frames = s.Responses
			}
		}
		if len(frames) <= sample.last+1 {
			t.Fatalf("%s recording missing", sample.action)
		}
		fromRecording := func(n int) Packet {
			raw, err := hex.DecodeString(frames[n])
			if err != nil {
				t.Fatal(err)
			}
			f, err := protocol.DecodeFrame(raw, protocol.Game, protocol.ServerToClient)
			if err != nil {
				t.Fatal(err)
			}
			return Packet{f.Opcode, f.Body}
		}
		cash := fromRecording(sample.first + 3)
		if cash.Opcode != 851 || le.Uint32(cash.Body) != 19 {
			t.Fatal("unexpected recorded cash catalog")
		}
		regular, special, err := Town(in, character, Options{CashLimitsBody: cash.Body})
		if err != nil {
			t.Fatal(err)
		}
		fate, err := FateContainers(in, character)
		if err != nil {
			t.Fatal(err)
		}
		if len(regular) != 5 || len(special) != 6 || len(fate) != 2 {
			t.Fatal("unexpected number of native inventory notifications")
		}
		for i, got := range append(regular, special...) {
			want := fromRecording(sample.first + i)
			if want.Opcode != got.Opcode || !bytes.Equal(want.Body, got.Body) {
				t.Fatalf("%s frame %d opcode %d", sample.action, sample.first+i, want.Opcode)
			}
		}
		for i, got := range fate {
			want := fromRecording(sample.last + i)
			if want.Opcode != got.Opcode || !bytes.Equal(want.Body, got.Body) {
				t.Fatalf("%s Fate frame %d", sample.action, sample.last+i)
			}
		}
	}
}

func TestOrdinaryMoveDoesNotReadUnrelatedContainers(t *testing.T) {
	in := storage.Inventory{CharacterID: 7, Coins: 10, Items: []storage.Asset{{Location: 7}}}
	packets, err := BagAndWear(in, storage.Character{ID: 7})
	if err != nil || len(packets) != 2 || packets[0].Opcode != 13 || packets[1].Opcode != 14 {
		t.Fatalf("C19 ordinary bag and wear projection: %v", err)
	}
}

func TestCashNotificationTriggers(t *testing.T) {
	now := time.Unix(1754812800, 0)
	o := Options{Now: now, AccountPurchases: map[uint32]Purchase{0x80000001: {}}}
	if b, err := cashBody(o, map[uint32]Purchase{10: {}}); err != nil || b != nil {
		t.Fatal("NPC and personal counters alone acquired a cash-limits notice")
	}
	o.AccountPurchases[99] = Purchase{PeriodStart: now.Unix(), Count: 1}
	b, err := cashBody(o, nil)
	if err != nil || len(b) != 16 || le.Uint32(b[4:]) != 310 {
		t.Fatal("missing C# no-catalog schema-9 limit")
	}
	o.CashPolicies = map[uint32]CashPolicy{}
	if b, err = cashBody(o, nil); err != nil || !bytes.Equal(b, []byte{0, 0, 0, 0}) {
		t.Fatal("explicit empty catalog did not produce zero rows")
	}
}

func TestNonemptyCSharpVectors(t *testing.T) {
	raw, err := os.ReadFile("../../testdata/inventory/nonempty.json")
	if err != nil {
		t.Fatal(err)
	}
	var fixture struct {
		Rows []struct {
			Key    string
			Opcode uint16
			Body   []byte
		}
	}
	if err = json.Unmarshal(raw, &fixture); err != nil {
		t.Fatal(err)
	}
	wants := map[string]Packet{}
	for _, r := range fixture.Rows {
		wants[r.Key] = Packet{r.Opcode, r.Body}
	}
	toJSON := func(v any) []byte {
		b, err := json.Marshal(v)
		if err != nil {
			t.Fatal(err)
		}
		return b
	}
	wcore := make([]byte, 119)
	wcore[10], wcore[13], wcore[19], wcore[20], wcore[87], wcore[100] = 3, 2, 1, 7, 4, 90
	stackCore := make([]byte, 119)
	stackCore[102] = 143
	character := storage.Character{ID: 1}
	in := storage.Inventory{CharacterID: 1, Gold: 2147483651, Coins: 123, Cera: 51, CargoGold: 4321, BagExpansion: 8, PersonalCapacity: 152, CargoCapacity: 16}
	in.Items = append(in.Items, storage.Asset{Source: "weapon", CharacterID: 1, Location: 3, Slot: 12, TemplateID: 0x11223344, Count: 1, Core: wcore, Template: []byte("null"), Attributes: []byte("null"), Weapon: &storage.WeaponFacts{QualitySeed: 0x01234567, Durability: 45, MaximumDurability: 50, UsableJobs: 1}})
	add := func(loc byte, slot uint16, id, count uint32) {
		tpl := template{TemplateID: id, Kind: 2, PetConsumable: loc == 7, FateCollection: loc == 44, AccountCommonInventory: loc == 45}
		a := storage.Asset{Source: "item", CharacterID: 1, TemplateID: id, Count: count, Location: loc, Slot: slot, Core: stackCore}
		if loc == 12 || loc == 44 || loc == 45 {
			a.CharacterID = 0
		}
		if loc == 38 {
			tpl.Kind = 1
			tpl.WornSlot = 32
			tpl.WornSlotIsNative = true
			a.Core = wcore
			a.Attributes = toJSON(attributes{QualitySeed: 8192, Durability: 45, Reinforcement: 3, Forge: 4, AmplificationType: 1, AmplificationValue: 7, Binding: 2})
		}
		a.Template = toJSON(tpl)
		in.Items = append(in.Items, a)
	}
	add(0, 40, 18012, 13)
	add(2, 5, 18012, 17)
	add(12, 9, 18012, 27)
	add(7, 216, 18015, 8)
	add(45, 49, 18016, 3)
	add(38, 55, 18017, 1)
	add(44, 3, 18018, 1)
	core153 := make([]byte, 153)
	core153[100] = 215
	avatarItem := specialItem{Slot: 4, Location: 1, Core153: core153, Avatar: &avatar{Sockets: []byte{65, 128}, Colors: []byte{3}, Option: 0xffffffff}, Attributes: attributes{QualitySeed: 400, Durability: 33, Reinforcement: 2, Forge: 1, AmplificationType: 3, AmplificationValue: 266, Binding: 2}}
	avatarItem.Template.TemplateID, avatarItem.Template.WornSlot = 38001, 4
	artifact := specialItem{Slot: 27, Location: 3, Core153: core153, Artifact: &struct{ Color byte }{27}, Attributes: attributes{QualitySeed: 452, Durability: 22, Reinforcement: 1}}
	artifact.Template.TemplateID, artifact.Template.Kind, artifact.Template.WornSlot = 38003, 2, 27
	creature := specialItem{Slot: 26, Location: 3, Core153: core153, Pet: &pet{NativeSerial: 0x1234, Name: "小熊", Level: 5, Exp: 33, Satiety: 71, ModeFlag: 1, Evolution: 2, EvolutionProgress: 3, TailFlag: 1}}
	creature.Template.TemplateID, creature.Template.Kind, creature.Template.WornSlot = 38002, 1, 26
	character.Gameplay = toJSON(map[string]any{"PetSkinUnlocked": true, "SpecialAssets": specialState{AvatarExpansion: 8, Items: []specialItem{avatarItem, artifact, creature}}})
	regular, special, err := Town(in, character, Options{})
	if err != nil {
		t.Fatal(err)
	}
	fate, err := FateContainers(in, character)
	if err != nil {
		t.Fatal(err)
	}
	check := func(keys []string, packets []Packet) {
		if len(keys) != len(packets) {
			t.Fatalf("packet count %d instead of %d", len(packets), len(keys))
		}
		for i, key := range keys {
			want := wants[key]
			if want.Opcode != packets[i].Opcode || !bytes.Equal(want.Body, packets[i].Body) {
				t.Fatalf("C# vector differs for %s", key)
			}
		}
	}
	check([]string{"bag", "worn", "wallet", "petBag", "personal", "account", "common", "medal"}, regular)
	check([]string{"avatarBag", "petBag", "creatures", "artifact", "pet", "avatarWorn"}, special)
	check([]string{"collection"}, fate)
	in.Items[0].Core = nil
	if got, _, err := Town(in, character, Options{}); err == nil || len(got) > 0 {
		t.Fatal("incomplete nonempty equipment was projected")
	}
}

func TestCashPeriods(t *testing.T) {
	now := time.Date(2026, 11, 23, 5, 59, 59, 0, serverTime)
	legacy := time.Date(2026, 11, 19, 6, 0, 0, 0, serverTime).Unix()
	o := Options{Now: now, CashPolicies: map[uint32]CashPolicy{1: {7, 101, false}, 2: {1, 102, false}, 3: {5, 1, true}, 4: {10, 0, true}}, AccountPurchases: map[uint32]Purchase{1: {legacy, 2}}}
	personal := map[uint32]Purchase{4: {1, 4}}
	b, err := cashBody(o, personal)
	if err != nil {
		t.Fatal(err)
	}
	expect := []struct {
		id, remaining uint32
		reset         int64
	}{{1, 5, legacy + 7*86400}, {2, 1, time.Date(2026, 12, 1, 6, 0, 0, 0, serverTime).Unix()}, {3, 5, now.Add(time.Second).Unix()}, {4, 6, 0x7fff53f0}}
	for i, r := range expect {
		p := b[4+i*12:]
		if le.Uint32(p) != r.id || int64(le.Uint32(p[4:])) != r.reset || le.Uint32(p[8:]) != r.remaining {
			t.Fatalf("purchase restriction %d differs", r.id)
		}
	}
}
