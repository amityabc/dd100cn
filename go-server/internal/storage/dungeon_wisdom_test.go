package storage

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"
)

func TestWisdomDebitsFreezeNativePassAndStoneOrder(t *testing.T) {
	const passID = 900001
	const nextPass = 900002
	const uid1 = "3d737f2f-1db2-455f-9bb0-577a721e6f50"
	const uid2 = "3d737f2f-1db2-455f-9bb0-577a721e6f51"
	const uid3 = "3d737f2f-1db2-455f-9bb0-577a721e6f52"
	const uid4 = "3d737f2f-1db2-455f-9bb0-577a721e6f53"
	const uid5 = "3d737f2f-1db2-455f-9bb0-577a721e6f54"
	makeItem := func(uid string, id, count uint32, location byte, slot uint16, attrs string) Asset {
		t.Helper()
		template, err := json.Marshal(struct{ TemplateId uint32 }{id})
		if err != nil {
			t.Fatal(err)
		}
		return Asset{UID: strings.ReplaceAll(uid, "-", ""), TemplateID: id, Count: count, Location: location, Slot: slot,
			Template: template, Attributes: json.RawMessage(attrs), Core: make([]byte, 119), Detail: []byte{42}}
	}
	now := time.Unix(500, 0)
	entry := WisdomAdmission{TicketCount: 18, Passes: []WisdomPass{{passID, 2}, {nextPass, 1}}}
	stones := []Asset{makeItem(uid3, WisdomStone, 14, 0, 140, `{}`), makeItem(uid4, WisdomStone, 9, 0, 121, `{}`)}
	assets := append(append([]Asset(nil), stones...),
		makeItem(uid1, passID, 2, 0, 101, `{"ExpiresAt":500}`),
		makeItem(uid2, passID, 3, 0, 102, `{"ExpiresAt":501}`),
		makeItem(uid5, nextPass, 1, 0, 100, `{}`))
	got, err := wisdomDebits(Inventory{Items: assets}, entry.TicketCount, entry.Passes, now)
	if err != nil || len(got) != 1 || got[0].InstanceId != uid2 || got[0].Count != 2 ||
		got[0].Expected.Count != 3 || got[0].Expected.Detail[0] != 42 {
		t.Fatalf("pass debit = %#v, %v", got, err)
	}
	entry.Debits = got
	if !entry.validDebits() {
		t.Fatal("valid pass debit failed validation")
	}
	assets[3].Attributes = json.RawMessage(`{"Locked":true}`)
	got, err = wisdomDebits(Inventory{Items: assets}, entry.TicketCount, entry.Passes, now)
	if err != nil || len(got) != 1 || got[0].InstanceId != uid5 {
		t.Fatalf("second pass debit = %#v, %v", got, err)
	}
	got, err = wisdomDebits(Inventory{Items: stones}, entry.TicketCount, entry.Passes, now)
	if err != nil || len(got) != 2 || got[0].InstanceId != uid4 || got[0].Count != 9 ||
		got[1].InstanceId != uid3 || got[1].Count != 9 {
		t.Fatalf("stone debits = %#v, %v", got, err)
	}
	entry.Debits = got
	if !entry.validDebits() {
		t.Fatal("valid stone debits failed validation")
	}
	stones[1].Location = 2
	if _, err := wisdomDebits(Inventory{Items: stones}, entry.TicketCount, entry.Passes, now); !errors.Is(err, ErrWisdomTickets) {
		t.Fatalf("warehouse stones entered the preview: %v", err)
	}
}
