package mailwire

import (
	"bytes"
	"encoding/json"
	"os"
	"s9a1/server/internal/storage"
	"testing"
)

func TestLegacyMailbox(t *testing.T) {
	raw, err := os.ReadFile("testdata/mail.json")
	if err != nil {
		t.Fatal(err)
	}
	var fixture struct{ Packets []Packet }
	if err = json.Unmarshal(raw, &fixture); err != nil {
		t.Fatal(err)
	}
	got, err := Town(storage.Character{ID: 1, Gameplay: raw})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || len(fixture.Packets) != 2 || got[1].Opcode != 99 || le.Uint16(got[1].Body) != 22 {
		t.Fatal("incorrect number of pages or unread letters")
	}
	for i, p := range fixture.Packets {
		if p.Opcode != got[i].Opcode || !bytes.Equal(p.Body, got[i].Body) {
			t.Fatalf("N%d differs from the original C# mailbox", p.Opcode)
		}
	}
	empty, err := Town(storage.Character{Gameplay: []byte("{}")})
	if err != nil || len(empty) != 2 || !bytes.Equal(empty[0].Body, make([]byte, 6)) || !bytes.Equal(empty[1].Body, make([]byte, 2)) {
		t.Fatal("empty new character box differs")
	}
}
