package questwire

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"s9a1/server/internal/storage"
)

func TestDPLUpperTicketPreservesAwakeningAndAwardsScroll(t *testing.T) {
	const scroll = 920
	const uid = "eea722fe88ae46259485fb20609f5b02"
	h := strings.Repeat("A", 64)
	resource := func(id uint32) json.RawMessage {
		return json.RawMessage(fmt.Sprintf(`{"TemplateId":%d,"Kind":2,"MinimumLevel":1,"UsableJobs":65535,"FirstBagSlot":65,"LastBagSlot":120,"WornSlot":65535,"StackLimit":1000,"ResourceHash":%q}`, id, h))
	}
	q := Ticket{Criterion: Criterion{Id: 6413, MinimumLevel: 100, MaximumLevel: 100, Job: -1, Grow: -1, GrowNumber: 2,
		LegacyUpperAwakening: true, Supported: true, Required: [][]uint16{{6412}}}, Grade: "epic", Rewards: []Material{{scroll, 1}}}
	catalog := Catalog{extra: []Ticket{q}, tickets: map[uint16]*Ticket{6413: &q}, rewards: map[uint32]json.RawMessage{scroll: resource(scroll)}}
	catalog.growth.QuestTickets = map[uint32]questTicketItem{upperAwakeningTicket: {Template: resource(upperAwakeningTicket),
		Definition: questTicketDefinition{Kind: ticketExplicit, TemplateId: upperAwakeningTicket, NativeAction: 130, MinimumLevel: 100, MaximumLevel: 255,
			UsableJobs: 65535, ResourceHash: h, Arguments: []uint32{6410, 6411, 6412, 6413}, CooldownMilliseconds: 1000}}}
	c := storage.Character{ID: 1, Level: 100, Revision: 5, Gameplay: json.RawMessage(`{"Initialized":true,"Progression":{"GrowType":33},"Quests":{"Cleared":{"1234":3},"Active":{}}}`)}
	source := storage.Asset{UID: uid, Source: "item", CharacterID: 1, TemplateID: upperAwakeningTicket, Count: 1, Slot: 65,
		Template: resource(upperAwakeningTicket), Attributes: json.RawMessage(`{}`), Core: make([]byte, 119)}
	in := storage.Inventory{CharacterID: 1, Revision: 5, Items: []storage.Asset{source}}
	opts := TicketContext{CompleteContext: CompleteContext{ChangeContext: ChangeContext{Now: time.Unix(1700000000, 0)}}}
	req := QuestTicketRequest{Slot: 65, Action: 130, Sequence: 33}
	m, done, err := catalog.TicketUse(t.Context(), c, in, req, 0, false, opts)
	if err != nil || m.Reward == nil || len(done) != 1 || done[0] != 6413 {
		t.Fatalf("upper ticket: %v %+v", err, done)
	}
	p, err := playerOf(m.Reward.Character)
	if err != nil || p.grow != 33 || p.hist.Cleared[6413] != 1 || p.hist.Cleared[1234] != 3 || p.hist.Cleared[6412] != 0 {
		t.Fatalf("unexpected advancement state: %+v %v", p, err)
	}
	assets := m.Reward.Inventory.Items
	if len(assets) != 1 || assets[0].TemplateID != scroll || assets[0].Count != 1 || assets[0].UID == uid || len(m.Reward.Receipts) != 1 {
		t.Fatalf("unexpected awarded assets: %+v", assets)
	}
	if _, _, err = catalog.TicketUse(t.Context(), m.Reward.Character, in, req, 0, false, opts); !errors.Is(err, storage.ErrInvalid) {
		t.Fatalf("cooldown or completed target was bypassed: %v", err)
	}
}
