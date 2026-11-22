package storage

import (
	"encoding/json"
	"math"
	"slices"
	"time"
)

func (x *CommerceTx) giftUse(id uint32, u *GiftUsage, now time.Time) ([]GiftReward, error) {
	if u == nil {
		return nil, nil
	}
	if u.WeeklyResetDay > 6 || len(u.AccumulatedRewards) > 255 {
		return nil, ErrInvalid
	}
	if u.CooldownMilliseconds == 0 && u.DailyLimit == 0 && u.WeeklyLimit == 0 && u.AccumulatedLimit == 0 && len(u.AccumulatedRewards) == 0 {
		return nil, nil
	}
	type use struct {
		Day, Week                                 int64
		DailyCount, WeeklyCount, AccumulatedCount uint32
		NextUseMilliseconds                       int64
	}
	var profile struct {
		GiftBoxUses           map[uint32]use
		GiftBoxCooldownGroups map[uint32]int64
	}
	if err := json.Unmarshal(x.Character.Gameplay, &profile); err != nil {
		return nil, err
	}
	uses, groups := profile.GiftBoxUses, profile.GiftBoxCooldownGroups
	if uses == nil {
		uses = map[uint32]use{}
	}
	if groups == nil {
		groups = map[uint32]int64{}
	}
	old, ok := uses[id]
	ms := now.UnixMilli()
	if old.NextUseMilliseconds > ms || u.CooldownGroup != 0 && groups[u.CooldownGroup] > ms || !ok && len(uses) >= 32768 {
		return nil, ErrInvalid
	}
	day, _ := CommerceWindow(1, now, CommercePurchase{})
	weekday := time.Unix(day, 0).In(commerceZone).Weekday()
	week := day - int64((7+int(weekday)-int(u.WeeklyResetDay))%7)*86400
	next := use{Day: day, Week: week, NextUseMilliseconds: ms + int64(u.CooldownMilliseconds), AccumulatedCount: old.AccumulatedCount}
	if u.DailyLimit > 0 {
		if old.Day == day {
			next.DailyCount = old.DailyCount
		}
		if next.DailyCount >= u.DailyLimit {
			return nil, ErrInvalid
		}
		next.DailyCount++
	}
	if u.WeeklyLimit > 0 {
		if old.Week == week {
			next.WeeklyCount = old.WeeklyCount
		}
		if next.WeeklyCount >= u.WeeklyLimit {
			return nil, ErrInvalid
		}
		next.WeeklyCount++
	}
	maximum := u.AccumulatedLimit
	seen := map[uint32]bool{}
	for _, b := range u.AccumulatedRewards {
		if b.Count == 0 || len(b.Rewards) == 0 || seen[b.Count] {
			return nil, ErrInvalid
		}
		seen[b.Count] = true
		maximum = max(maximum, b.Count)
	}
	if next.AccumulatedCount < maximum {
		next.AccumulatedCount++
	}
	var bonus []GiftReward
	for _, b := range u.AccumulatedRewards {
		if b.Count > old.AccumulatedCount && b.Count <= next.AccumulatedCount {
			bonus = append(bonus, b.Rewards...)
		}
	}
	uses[id] = next
	if u.CooldownGroup != 0 && u.CooldownMilliseconds > 0 {
		if groups[u.CooldownGroup] == 0 && len(groups) >= 32768 {
			return nil, ErrInvalid
		}
		groups[u.CooldownGroup] = next.NextUseMilliseconds
	}
	return bonus, replaceMailFields(&x.Character, map[string]any{"GiftBoxUses": uses, "GiftBoxCooldownGroups": groups})
}

func (x *CommerceTx) activateGiftContract(template, count uint32, grant GiftContract, now time.Time) error {
	seconds := uint64(grant.Days) * 86400 * uint64(count)
	if seconds == 0 || seconds > math.MaxInt32 {
		return ErrInvalid
	}
	fresh := map[string]bool{}
	for _, a := range x.before.Items {
		fresh[a.UID] = true
	}
	var targets []int
	for i, a := range x.Inventory.Items {
		if a.Location == 0 && a.TemplateID == template && a.Count != 0 {
			targets = append(targets, i)
		}
	}
	slices.SortFunc(targets, func(i, j int) int {
		a, b := x.Inventory.Items[i], x.Inventory.Items[j]
		if fresh[a.UID] != fresh[b.UID] {
			if fresh[a.UID] {
				return 1
			}
			return -1
		}
		return int(a.Slot) - int(b.Slot)
	})
	left := count
	for _, i := range targets {
		if left == 0 {
			break
		}
		n := min(left, x.Inventory.Items[i].Count)
		if err := x.debitAsset(i, n); err != nil {
			return err
		}
		left -= n
	}
	if left != 0 {
		return ErrInvalid
	}
	if err := x.extendPremium(grant.PremiumType, int64(seconds), now); err != nil {
		return err
	}
	x.gift.Premium = true
	return nil
}
