package ruby

import (
	"crypto/sha256"
	"fmt"
	"math"
	"slices"
)

func (l Ledger) Next(requested uint32) (uint32, error) {
	if l.Cleared >= 60 {
		return 0, ErrInvalid
	}
	next := FloorID(l.Cleared + 1)
	var last *Clear
	for _, clear := range l.Clears {
		final := clear.Floor
		if clear.Advance != nil {
			final = clear.Advance.Last
		}
		if clear.Week == l.View.Week && final == l.Cleared && (last == nil || Hidden(clear.Dungeon)) {
			last = &clear
		}
	}
	if last != nil && last.Advance != nil {
		next = last.Advance.Next
	}
	consecutive := next
	if last != nil {
		consecutive = FloorID(min(last.Floor+1, 60))
	}
	if requested != next && requested != 9700 && requested != consecutive && !(last != nil && last.Floor == 23 && requested == 9707) {
		return 0, ErrInvalid
	}
	return next, nil
}
func (l Ledger) Admission(char uint16, run string, requested, dungeon, seed uint32, resourceHash string) (Entry, error) {
	next, err := l.Next(requested)
	if err != nil || next != dungeon || !validID(run) || char == 0 || !hash(resourceHash) {
		return Entry{}, ErrInvalid
	}
	e := Entry{Policy: Policy, Character: char, Run: run, Requested: requested, Dungeon: dungeon, Floor: Floor(dungeon), Week: l.View.Week, Seed: seed, Hash: resourceHash}
	if Hidden(dungeon) {
		for key, c := range l.Clears {
			if c.Week == l.View.Week && c.Advance != nil && c.Advance.Next == dungeon && c.Advance.Last == l.Cleared {
				if e.FromClear != nil {
					return Entry{}, ErrInvalid
				}
				e.Floor = c.Advance.Last
				e.FromClear = &key
			}
		}
		if e.FromClear == nil {
			return Entry{}, ErrInvalid
		}
	}
	return e, nil
}
func (e Entry) draw(salt string, maximum uint64) uint64 {
	sum := sha256.Sum256([]byte(fmt.Sprintf("%s|%s|%d|%d|%s", RoutePolicy, e.Hash, e.Seed, e.Floor, salt)))
	return le.Uint64(sum[:]) % maximum
}
func (e Entry) pick(rows []Weight, salt string, bound uint64) (uint32, error) {
	if bound == 0 {
		for _, row := range rows {
			if math.MaxUint64-bound < row.Weight {
				return 0, ErrInvalid
			}
			bound += row.Weight
		}
	}
	if bound == 0 {
		return 0, ErrInvalid
	}
	roll := e.draw(salt, bound)
	for _, row := range rows {
		if roll < row.Weight {
			return row.Item, nil
		}
		roll -= row.Weight
	}
	return 0, nil
}
func (r Resources) Plan(l Ledger, e Entry) (Advance, error) {
	a := Advance{Policy: RoutePolicy, First: e.Floor, Last: e.Floor, Boxes: []uint32{}}
	floor := int(e.Floor)
	if floor < 1 || floor > 60 {
		return a, ErrInvalid
	}
	if Hidden(e.Dungeon) {
		pos := slices.Index(r.HiddenIds, e.Dungeon)
		if pos < 0 {
			return a, ErrInvalid
		}
		kind := pos % 2
		if pos == 10 {
			kind = 2
		}
		found := false
		for _, reward := range r.HiddenPrizes {
			if reward.Kind != kind || floor < reward.Minimum || floor > reward.Maximum {
				continue
			}
			if found {
				return a, ErrInvalid
			}
			found = true
			for i, roll := range reward.Rolls {
				v, err := e.pick(roll, fmt.Sprintf("hidden-box:%d", i), 100)
				if err != nil {
					return a, err
				}
				if v != 0 {
					a.Boxes = append(a.Boxes, v)
				}
			}
		}
		if !found {
			return a, ErrInvalid
		}
	} else {
		jump := slices.IndexFunc(r.Jumps, func(v struct {
			Floor, Rate int
			Distances   []Weight
		}) bool {
			return floor <= v.Floor
		})
		if jump < 0 {
			return a, ErrInvalid
		}
		j := r.Jumps[jump]
		if floor < 60 && int(e.draw("jump", 100)) < j.Rate {
			distance, err := e.pick(j.Distances, "jump-distance", 0)
			if err != nil || distance == 0 {
				return a, ErrInvalid
			}
			a.Last = byte(min(60, floor+int(distance)-1, (floor+4)/5*5))
		}
		if a.Last%5 == 0 {
			i := slices.IndexFunc(r.Prizes, func(v struct {
				Floor   int
				Weights []Weight
			}) bool {
				return v.Floor == int(a.Last)
			})
			if i < 0 {
				return a, ErrInvalid
			}
			v, err := e.pick(r.Prizes[i].Weights, "checkpoint-box", 0)
			if err != nil || v == 0 {
				return a, ErrInvalid
			}
			a.Boxes = append(a.Boxes, v)
		}
	}
	a.Next = FloorID(a.Last + 1)
	if !Hidden(e.Dungeon) && floor >= r.NamedMinimum && a.Next != 0 {
		previous := -r.Cooldown
		for _, c := range l.Clears {
			if c.Week == e.Week && Hidden(c.Dungeon) {
				previous = max(previous, int(c.Floor))
			}
		}
		i := slices.IndexFunc(r.HiddenRates, func(v struct{ Floor, Rate, Reserved int }) bool { return v.Floor == floor })
		if i < 0 {
			return a, ErrInvalid
		}
		if floor-previous > r.Cooldown && int(e.draw("hidden", 100000)) < r.HiddenRates[i].Rate {
			pos := 10
			if floor < r.BossMinimum || int(e.draw("hidden-boss", 10000)) >= r.BossRate {
				pos = int(e.draw("hidden-map", 5)) * 2
				if int(e.draw("hidden-upgrade", 10000)) < r.UpgradedRate {
					pos++
				}
			}
			if pos >= len(r.HiddenIds) {
				return a, ErrInvalid
			}
			a.Next = r.HiddenIds[pos]
		}
	}
	return a, nil
}
