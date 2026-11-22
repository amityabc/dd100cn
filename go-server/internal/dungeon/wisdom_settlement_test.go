package dungeon

import (
	"bytes"
	"testing"
)

func TestWisdomResultHasNoCards(t *testing.T) {
	want := make([]byte, 261)
	want[139] = 1
	want[195] = 1
	if got := NoCardRewardNotice(); !bytes.Equal(want, got) {
		t.Fatalf("N35 %d-byte body differs from the zero-reward C# packet: %x", len(got), got)
	}
}
