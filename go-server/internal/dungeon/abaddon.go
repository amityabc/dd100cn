package dungeon

import (
	"maps"
	"math"
	"slices"
	"time"

	"s9a1/server/internal/storage"
)

type abaddonMechanics struct {
	RoomTimes, KillTimes map[uint32]uint32
	HpTimes              []struct {
		Monster, Milliseconds uint32
		Percent               byte
	}
	Symbols                            []struct{ X, Y, Bit byte }
	Maximum, TimeoutGrace, ReturnDelay uint32
}

type AbaddonClock struct {
	X, Y             byte
	Map              uint32
	Started, Updated time.Time
	Remaining        uint32
	Hp               map[uint16]int64
	Stopped          bool
	Failed           time.Time
}

func (r *Run) AbaddonDeadline() time.Time {
	if r == nil || r.AbaddonClock == nil || r.Definition.Dungeon.AbaddonMechanics == nil {
		return time.Time{}
	}
	clock, rules := r.AbaddonClock, r.Definition.Dungeon.AbaddonMechanics
	if !clock.Failed.IsZero() {
		return clock.Failed.Add(time.Duration(rules.ReturnDelay) * time.Millisecond)
	}
	if r.Cleared || clock.Stopped {
		return time.Time{}
	}
	return clock.Updated.Add(time.Duration(uint64(clock.Remaining)+uint64(rules.TimeoutGrace)) * time.Millisecond)
}

func (r *Run) AbaddonFailed() bool {
	return r.AbaddonClock != nil && !r.AbaddonClock.Failed.IsZero()
}

func (r *Run) ApplyAbaddonKill(d Death, result DeathResult, now time.Time) {
	rules := r.Definition.Dungeon.AbaddonMechanics
	if rules == nil || d.Prior != nil {
		return
	}
	if result.RoomCleared {
		for _, symbol := range rules.Symbols {
			if symbol.X == r.Room.X && symbol.Y == r.Room.Y {
				r.AbaddonSymbols |= symbol.Bit
			}
		}
	}
	if clock := r.AbaddonClock; clock != nil && !clock.Stopped && clock.Failed.IsZero() {
		left := max(0, int64(clock.Remaining)-max(0, now.Sub(clock.Updated).Milliseconds()))
		clock.Remaining = uint32(min(uint64(rules.Maximum), uint64(left)+uint64(rules.KillTimes[d.Monster.Code])))
		clock.Updated, clock.Stopped = now, result.RoomCleared
	}
}

func (r *Run) StartAbaddonClock(now time.Time) {
	rules := r.Definition.Dungeon.AbaddonMechanics
	if rules == nil || r.AbaddonClock != nil || r.Cleared || !r.Ready || r.PlayerDeath != nil || r.clearedRooms[cell{r.Room.X, r.Room.Y}] {
		return
	}
	if limit, ok := rules.RoomTimes[r.Room.MapID]; ok {
		r.AbaddonClock = &AbaddonClock{X: r.Room.X, Y: r.Room.Y, Map: r.Room.MapID, Started: now, Updated: now, Remaining: limit, Hp: make(map[uint16]int64)}
	}
}

func (r *Run) ObserveAbaddonHP(body []byte, now time.Time) {
	clock, rules := r.AbaddonClock, r.Definition.Dungeon.AbaddonMechanics
	if len(body) != 18 || !r.Ready || r.PlayerDeath != nil || r.Cleared || clock == nil || clock.Stopped || !clock.Failed.IsZero() ||
		rules == nil || le.Uint16(body) != 1 {
		return
	}
	actor, code, hp := le.Uint32(body[2:]), le.Uint32(body[6:]), int64(le.Uint64(body[10:]))
	index := int64(actor) - int64(r.actorBase())
	if hp < 0 || actor >= math.MaxUint16 || index < 0 || index >= int64(len(r.Room.Monsters)) || r.Room.Monsters[index].Code != code ||
		!slices.ContainsFunc(rules.HpTimes, func(row struct {
			Monster, Milliseconds uint32
			Percent               byte
		}) bool {
			return row.Monster == code
		}) {
		return
	}
	if _, dead := r.Dead[uint16(actor)]; dead {
		return
	}
	if previous, exists := clock.Hp[uint16(actor)]; exists && previous == hp {
		return
	}
	clock.Updated, clock.Remaining, clock.Hp[uint16(actor)] = now, rules.Maximum, hp
}

func (r *Run) FreezeAbaddonTimeout() (AbaddonClock, bool) {
	clock := r.AbaddonClock
	if r.Cleared || clock == nil || clock.Stopped || r.clearedRooms[cell{r.Room.X, r.Room.Y}] {
		return AbaddonClock{}, false
	}
	frozen := *clock
	frozen.Hp = maps.Clone(clock.Hp)
	return frozen, true
}

func (r *Run) ApplyAbaddonTimeout(now time.Time) {
	r.AbaddonClock.Failed, r.AbaddonClock.Stopped = now, true
}

func (r *Run) AbaddonFloorReward() *storage.AbaddonMailReward {
	if r.AbaddonEntry == nil || r.AbaddonEntry.Dungeon > 100000158 || r.Room.Y != 0 || r.Room.X < 1 || r.Room.X > 10 {
		return nil
	}
	for _, mail := range r.Definition.Dungeon.AbaddonMailRewards {
		if mail.Floor == r.Room.X {
			return &mail
		}
	}
	return nil
}

func (r *Run) AbaddonFinalReward() *storage.AbaddonMailReward {
	if r.AbaddonEntry == nil || r.AbaddonEntry.Dungeon <= 100000158 {
		return nil
	}
	for _, mail := range r.Definition.Dungeon.AbaddonMailRewards {
		if mail.Floor == 0 {
			return &mail
		}
	}
	return nil
}
