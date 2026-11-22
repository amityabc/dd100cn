package storage

import (
	"errors"
	"testing"
	"time"
)

func TestCashCartTotalsFailBeforeAnyDebit(t *testing.T) {
	check := func(lines []CashLine, want uint16) {
		t.Helper()
		x := CommerceTx{Inventory: Inventory{Cera: 5, Gold: 5}}
		store := CommerceCatalog{Cash: map[uint32]CashOffer{
			10: {Commodity: 10, Count: 1, Cera: 10},
			11: {Commodity: 11, Count: 1, Gold: 10},
			12: {Commodity: 12, Count: 1, Cera: 10, TargetBagExpansion: 8},
			13: {Commodity: 13, Count: 1, Cera: 10, TargetAvatarExpansion: 8},
		}}
		var got CommerceRejection
		err := x.BuyCash(lines, store, time.Unix(500, 0), nil, nil)
		if !errors.As(err, &got) || uint16(got) != want || x.Inventory.Cera != 5 || x.Inventory.Gold != 5 {
			t.Fatalf("result %v want code %d, new wallet %+v", err, want, x.Inventory)
		}
	}
	check([]CashLine{{Commodity: 11}, {Commodity: 10}}, 11)
	check([]CashLine{{Commodity: 12}}, 11)
	check([]CashLine{{Commodity: 13}}, 20)
}
