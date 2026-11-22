package protocol

import (
	"encoding/json"
	"os"
	"testing"
)

func TestRegisteredRequestsMatchOriginalHost(t *testing.T) {
	data, err := os.ReadFile("../../testdata/routes-upstream.json")
	if err != nil {
		t.Fatal(err)
	}
	var ref struct {
		Routes []struct {
			Transport string
			Class     byte
			Opcode    uint16
		}
	}
	if err := json.Unmarshal(data, &ref); err != nil {
		t.Fatal(err)
	}
	seen := map[[2]uint16]bool{}
	for _, row := range ref.Routes {
		transport := Game
		if row.Transport == "Channel" {
			transport = Channel
		}
		f := Frame{Transport: transport, Direction: ClientToServer, Class: row.Class, Opcode: row.Opcode}
		if !RegisteredRequest(f, true) {
			t.Fatalf("missing %s class %d op %d", row.Transport, row.Class, row.Opcode)
		}
		seen[[2]uint16{uint16(transport), row.Opcode}] = true
	}
	count := 0
	for i := range 1 << 16 {
		for _, kind := range []Transport{Channel, Game} {
			f := Frame{Transport: kind, Class: byte(kind), Opcode: uint16(i)}
			if RegisteredRequest(f, true) != seen[[2]uint16{uint16(kind), uint16(i)}] {
				t.Fatalf("unexpected route for transport %d op %d", kind, i)
			}
			if kind == Game && RegisteredRequest(f, false) {
				count++
			}
		}
	}
	if count != 140 {
		t.Fatalf("original non-delegated game registry has 140 routes, got %d", count)
	}
}
