package gm

import (
	"strings"
	"testing"
)

// The original cash parser indexes price models by physical line. Gift-box
// socket rows and the flattened price blocks in this PVF use a different layout.
func TestCashAvatarPhysicalPriceModels(t *testing.T) {
	const rows = "7 0 0 45 0 0 0\n30 0 0 90 0 0 0\n0 0 0 400 0 0 0"
	const prefix = "[attach type]\n\x60[trade]\x60\n[equipment type]\n\x60[hat avatar]\x60 0\n[avatar type select]\n"
	const suffix = "\n[/avatar type select]\n"
	item := ItemSource{Id: 88, Equipment: true, Text: prefix + rows + suffix, Hash: strings.Repeat("0", 64)}
	for i, price := range []uint32{45, 90, 400} {
		part, actual, _, err := item.CashAvatar(byte(i+1), nil)
		if err != nil || actual != price || part.RelativeLifetimeSeconds != []uint32{7 * 86400, 30 * 86400, 0}[i] {
			t.Fatalf("model %d: price %d, lifetime %d, error %v", i+1, actual, part.RelativeLifetimeSeconds, err)
		}
	}
	item.Text = prefix + strings.ReplaceAll(rows, "\n", " ") + suffix
	if _, _, _, err := item.CashAvatar(1, nil); err == nil {
		t.Fatal("accepted extra price columns on one physical line")
	}
}
