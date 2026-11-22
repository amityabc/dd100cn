package storage

import (
	"encoding/binary"
	"encoding/json"
	"testing"
)

func TestDailyExpiryRequiresOriginalTemplateAndNewCore(t *testing.T) {
	const deadline = 1700000000
	template := json.RawMessage(`{"TemplateId":3033,"Kind":2,"PvfGiftItem":true,"DailyDeletion":true}`)
	templates := map[uint32]json.RawMessage{3033: template}
	a := Asset{Source: "item", TemplateID: 3033, Count: 2, Template: template,
		Attributes: json.RawMessage(`{"ExpiresAt":1700000000}`), Core: make([]byte, 119)}
	binary.LittleEndian.PutUint32(a.Core[56:], deadline)
	check := func(want bool, at int64) {
		t.Helper()
		got, err := dailyExpired(a, templates, at)
		if err != nil || got != want {
			t.Fatalf("location=%d attributes=%s at=%d expired=%t want=%t err=%v", a.Location, a.Attributes, at, got, want, err)
		}
	}
	check(false, deadline-1)
	for _, location := range []byte{0, 2, 12} {
		a.Location = location
		check(true, deadline)
	}
	a.Location = 7
	check(false, deadline)
	a.Location = 0
	a.Attributes = json.RawMessage(`{"ExpiresAt":1700000000,"Locked":true}`)
	check(false, deadline)
	a.Attributes = json.RawMessage(`{"ExpiresAt":1700000000}`)
	a.Template = json.RawMessage(`{"TemplateId":3033,"Kind":2,"PvfGiftItem":true,"DailyDeletion":true,"StackLimit":100}`)
	check(false, deadline)
	a.Template = template
	a.Core[56] ^= 1
	check(false, deadline)
}
