package commerce

import (
	"fmt"
	"math"
	"slices"

	"s9a1/server/internal/storage"
)

type PurchasePolicy struct {
	Limit  uint32
	Period byte
}

func CashPurchasePolicies(offers map[uint32]storage.CashOffer) (account, character map[uint32]PurchasePolicy, accountCodes []uint32, err error) {
	account = map[uint32]PurchasePolicy{310: {1, 101}}
	character = make(map[uint32]PurchasePolicy)
	once := make(map[uint32]bool)
	for _, o := range offers {
		id := o.AccountRestriction
		if id == 0 {
			continue
		}
		if id > math.MaxInt32 || o.AccountLimit == 0 || o.AccountLimit > math.MaxUint8 || o.CharacterRestriction != 0 {
			return nil, nil, nil, fmt.Errorf("cash commodity %d has invalid account restriction", o.Commodity)
		}
		if o.AccountPeriod == 0 {
			if o.AccountLimit != 1 {
				return nil, nil, nil, fmt.Errorf("cash commodity %d has non-unit account-once quota", o.Commodity)
			}
			once[id] = true
			continue
		}
		p := PurchasePolicy{o.AccountLimit, o.AccountPeriod}
		if known, exists := account[id]; (exists && known != p) || (p.Period != 101 && p.Period != 102) {
			return nil, nil, nil, fmt.Errorf("cash restriction identity %d has incompatible account limits", id)
		}
		account[id] = p
	}
	accountCodes = make([]uint32, 0, len(once))
	for id := range once {
		if _, conflict := account[id]; conflict {
			return nil, nil, nil, fmt.Errorf("cash buy code %d collides with a periodic restriction", id)
		}
		accountCodes = append(accountCodes, id)
	}
	slices.Sort(accountCodes)
	for _, o := range offers {
		id := o.CharacterRestriction
		if id == 0 {
			continue
		}
		_, accountRestriction := account[id]
		p := PurchasePolicy{o.CharacterDailyLimit, o.CharacterPeriod}
		if prev, exists := character[id]; id > math.MaxInt32 || p.Limit == 0 || p.Limit > math.MaxUint8 || (p.Period != 0 && p.Period != 1) ||
			o.AccountRestriction != 0 || accountRestriction || once[id] || exists && prev != p {
			return nil, nil, nil, fmt.Errorf("cash restriction identity %d has incompatible character limits", id)
		}
		character[id] = p
	}
	return account, character, accountCodes, nil
}
