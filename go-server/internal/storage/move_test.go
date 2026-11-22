package storage

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"
)

func item(in Inventory, uid string) *Asset {
	for i := range in.Items {
		if in.Items[i].UID == uid {
			return &in.Items[i]
		}
	}
	return nil
}

func TestFrozenOffhandQualification(t *testing.T) {
	w := &WeaponFacts{QualitySeed: 25, Durability: 23, MaximumDurability: 30, MinimumLevel: 5, UsableJobs: 1 << 11, ResourceHash: strings.Repeat("A", 64), Qualification: json.RawMessage(`null`)}
	a := Asset{Source: "weapon", TemplateID: 101, Count: 1, Location: 3, Slot: 24, Weapon: w, Core: make([]byte, 119), Attributes: json.RawMessage(`null`)}
	a.Template = json.RawMessage(`{"TemplateId":101,"Kind":0,"StackLimit":1,"FirstBagSlot":9,"LastBagSlot":64,"WornSlot":12,"MaximumDurability":30,"MinimumLevel":5,"UsableJobs":2048,"ResourceHash":"` + w.ResourceHash + `","WeaponQualification":{"Verified":true,"Jobs":[{"Job":11,"Skill":95}]}}`)
	c := Character{Job: 11, Gameplay: json.RawMessage(`{"Progression":{"GrowType":4},"Skills":[{"Id":95,"Level":1},{"Id":251,"Level":1}]}`)}
	if e, err := a.WornEquipment(c); err != nil || e.Slot != 24 || e.Durability != 23 {
		t.Fatalf("frozen offhand: %v", err)
	}
	a.Attributes = json.RawMessage(`{"QualitySeed":25,"Durability":24}`)
	if _, err := a.WornEquipment(c); !errors.Is(err, ErrInvalid) {
		t.Fatal("accepted different typed weapon durability")
	}
}

func TestOrdinaryMoveAtomicity(t *testing.T) {
	ctx, s, account := testStore(t)
	l, err := s.Acquire(ctx, account)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.List(ctx, l); err != nil {
		t.Fatal(err)
	}
	profile := CreateCharacter{Name: account, Stats: []byte(`{"Values":{"inventory_limit":200}}`), Gameplay: []byte(`{"Skills":[]}`), ResourceHash: strings.Repeat("A", 64)}
	c, err := s.Create(ctx, l, profile)
	if err != nil {
		t.Fatal(err)
	}
	profile.Name += "b"
	other, err := s.Create(ctx, l, profile)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.Select(ctx, l, 0); err != nil {
		t.Fatal(err)
	}
	if _, err = s.AdvanceTown(ctx, l, true); err != nil {
		t.Fatal(err)
	}
	if _, err = s.AdvanceTown(ctx, l, false); err != nil {
		t.Fatal(err)
	}
	seed := func(char uint16, templateID, count uint32, source string, location, slot int, template, weapon, attr string, tag byte) string {
		var b [16]byte
		if _, err := rand.Read(b[:]); err != nil {
			t.Fatal(err)
		}
		uid := hex.EncodeToString(b[:])
		core := make([]byte, 119)
		core[118] = tag
		_, err := s.db.ExecContext(ctx, `INSERT INTO s9a1.assets(instance_id,account,character_id,source,template_id,template_json,weapon_facts,count,location,slot,attributes_json,core,detail)
			VALUES($1,$2,$3,$4,$5,$6::json,$7::json,$8,$9,$10,$11::json,$12,$13)`, uid, account, char, source, templateID, template, weapon, count, location, slot, attr, core, []byte{tag})
		if err != nil {
			t.Fatal(err)
		}
		return uid
	}
	facts := WeaponFacts{QualitySeed: 23, Durability: 30, MaximumDurability: 30, MinimumLevel: 1, UsableJobs: 1, ResourceHash: profile.ResourceHash, Qualification: []byte(`{"Verified":true,"Jobs":[]}`)}
	weapon, err := json.Marshal(facts)
	if err != nil {
		t.Fatal(err)
	}
	w1 := seed(c.ID, 101, 1, "weapon", 0, 9, "null", string(weapon), "null", 7)
	w2 := seed(c.ID, 102, 1, "weapon", 3, 12, "null", string(weapon), "null", 8)
	stack := `{"TemplateId":501,"Kind":2,"FirstBagSlot":65,"LastBagSlot":120,"StackLimit":10,"MinimumLevel":1,"UsableJobs":1}`
	a1 := seed(c.ID, 501, 8, "item", 0, 65, stack, "null", "{}", 9)
	a2 := seed(c.ID, 501, 4, "item", 0, 66, stack, "null", "{}", 9)
	otherUID := seed(other.ID, 501, 7, "item", 0, 65, stack, "null", "{}", 10)
	weights := map[uint32]int64{101: 4, 102: 3, 501: 1}
	swap := MoveRequest{Sequence: 19, SourceList: 0, SourceSlot: 9, SourceTemplate: 101, DestinationList: 3, DestinationSlot: 12, DestinationTemplate: 102, ActorIndex: ^uint32(0)}
	if _, err = s.Move(ctx, l, c.ID, c.Revision, swap); !errors.Is(err, ErrInvalid) {
		t.Fatalf("missing actual weight: %v", err)
	}
	res, err := s.Move(ctx, l, c.ID, c.Revision, swap, weights)
	if err != nil || res.Replayed || res.Character.Revision != c.Revision+1 || len(res.Character.Worn) != 1 || res.Character.Worn[0].TemplateID != 101 {
		t.Fatalf("wear swap: %+v %v", res, err)
	}
	if len(res.Inventory.Items) != 4 || item(res.Inventory, otherUID) != nil || item(res.Inventory, w1).Slot != 12 || item(res.Inventory, w2).Slot != 9 || !bytes.Equal(item(res.Inventory, w1).Detail, []byte{7}) {
		t.Fatal("swap lost an identity/detail or read another character")
	}
	again, err := s.Move(ctx, l, c.ID, c.Revision, swap, weights)
	if err != nil || !again.Replayed || again.Character.Revision != res.Character.Revision {
		t.Fatalf("replayed %v", err)
	}
	split := MoveRequest{Sequence: 20, SourceSlot: 65, SourceTemplate: 501, Count: 2, DestinationSlot: 67, ActorIndex: ^uint32(0)}
	res, err = s.Move(ctx, l, c.ID, res.Character.Revision, split)
	if err != nil || len(res.Inventory.Items) != 5 || item(res.Inventory, a1).Count != 6 {
		t.Fatalf("split %+v %v", res, err)
	}
	var splitID string
	for _, a := range res.Inventory.Items {
		if a.Location == 0 && a.Slot == 67 {
			splitID = a.UID
			if a.Count != 2 || a.Core[118] != 9 {
				t.Fatal("split lost native facts")
			}
		}
	}
	if splitID == "" || splitID == a1 || splitID == a2 {
		t.Fatal("new split identity missing")
	}
	merge := MoveRequest{Sequence: 21, SourceSlot: 67, SourceTemplate: 501, Count: 2, DestinationSlot: 66, DestinationTemplate: 501, DestinationStack: 4, ActorIndex: ^uint32(0)}
	fail := merge
	fail.DestinationStack = 5
	if _, err = s.Move(ctx, l, c.ID, res.Character.Revision, fail); !errors.Is(err, ErrInvalid) {
		t.Fatalf("invalid target count: %v", err)
	}
	// Same revision, two different sequences. Exactly one transfer may commit.
	var wg sync.WaitGroup
	log := make(chan error, 2)
	for n := 0; n < 2; n++ {
		req := merge
		req.Sequence += uint16(n)
		wg.Add(1)
		go func() { defer wg.Done(); _, err := s.Move(ctx, l, c.ID, res.Character.Revision, req); log <- err }()
	}
	wg.Wait()
	close(log)
	total, conflicts := 0, 0
	for e := range log {
		if e == nil {
			total++
		} else if errors.Is(e, ErrConflict) {
			conflicts++
		} else {
			t.Fatal(e)
		}
	}
	final, err := s.Inventory(ctx, l)
	if err != nil || total != 1 || conflicts != 1 || len(final.Items) != 4 || item(final, splitID) != nil || item(final, a2).Count != 6 || item(final, a1).Count != 6 {
		t.Fatalf("concurrent merge: %+v %v", final, err)
	}
	l, err = s.Acquire(ctx, account)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.Select(ctx, l, 1); err != nil {
		t.Fatal(err)
	}
	otherInventory, err := s.Inventory(ctx, l)
	if err != nil || len(otherInventory.Items) != 1 || otherInventory.Items[0].UID != otherUID || otherInventory.Items[0].Count != 7 {
		t.Fatalf("other character inventory: %+v %v", otherInventory, err)
	}
}
