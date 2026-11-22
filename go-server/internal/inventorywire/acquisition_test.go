package inventorywire

import (
	"fmt"
	"math"
	"testing"

	"s9a1/server/internal/storage"
)

func TestAcquisitionReusesConsumedSlot(t *testing.T) {
	item := func(id uint32, uid string) storage.Asset {
		return storage.Asset{UID: uid, Source: "item", CharacterID: 4, TemplateID: id, Count: 1, Slot: 65, Core: make([]byte, 119),
			Template: []byte(fmt.Sprintf(`{"TemplateId":%d,"Kind":2,"FirstBagSlot":65,"LastBagSlot":120}`, id))}
	}
	before := storage.Inventory{CharacterID: 4, Revision: 10, Items: []storage.Asset{item(100, "a")}}
	after := storage.Inventory{CharacterID: 4, Revision: 11, Gold: 40, Coins: 2, Items: []storage.Asset{item(200, "b")}}
	if !HasBagAcquisition(before, after) {
		t.Fatal("missed acquisition")
	}
	full, err := OrdinaryBag(after, storage.Character{ID: 4})
	if err != nil {
		t.Fatal(err)
	}
	frames, err := AcquisitionDelta(before, after, full)
	if err != nil || len(frames) != 2 {
		t.Fatalf("delta: %v (%d packets)", err, len(frames))
	}
	if b := frames[0].Body; frames[0].Opcode != 14 || le.Uint16(b[1:]) != 1 || le.Uint16(b[3:]) != 65 || le.Uint32(b[5:]) != math.MaxUint32 {
		t.Fatal("replacement did not clear the old slot first")
	}
	if b := frames[1].Body; frames[1].Opcode != 14 || le.Uint16(b[1:]) != 3 || le.Uint32(b[9:]) != 40 || le.Uint32(b[128:]) != 2 || le.Uint32(b[243:]) != 200 {
		t.Fatal("reward update omitted the complete wallet or new item")
	}
	moved := storage.Inventory{CharacterID: 4, Revision: 11, Items: []storage.Asset{item(100, "a")}}
	moved.Items[0].Slot = 66
	if HasBagAcquisition(before, moved) {
		t.Fatal("a conserved bag move was marked as an award")
	}
}
