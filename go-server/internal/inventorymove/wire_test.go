package inventorymove

import (
	"bytes"
	"encoding/hex"
	"testing"

	"s9a1/server/internal/storage"
)

func TestOriginalMoveVectors(t *testing.T) {
	vec := func(s string) []byte {
		b, err := hex.DecodeString(s)
		if err != nil {
			t.Fatal(err)
		}
		return b
	}
	// Serve100.Tests/AssetsTests.cs, manually authored independent vectors.
	b := vec("0009000000000001000000030C004433221100000000FFFFFFFF0000")
	r, err := Parse(30, b)
	if err != nil || r.SourceSlot != 9 || r.DestinationTemplate != 0x11223344 || r.ActorIndex != ^uint32(0) || !bytes.Equal(r.NativeBody(), b) {
		t.Fatal("request", err)
	}
	got := Accepted(storage.MoveOutcome{SourceList: 0, SourceSlot: 9, Count: 1, DestinationList: 3, DestinationSlot: 12})
	if !bytes.Equal(got, vec("0100090001000000030C00")) || !bytes.Equal(Rejected(r), vec("000400000300000000")) {
		t.Fatal("response")
	}
	if _, err = Parse(30, b[:27]); err == nil {
		t.Fatal("truncated request")
	}
}
