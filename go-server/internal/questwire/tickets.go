package questwire

import (
	"maps"
	"slices"
)

func (c *Catalog) equipment(p player, q *Ticket) bool {
	return q.Equipment && p.initialized && p.job <= 15 && p.level > 0 && p.hist.Cleared[q.Id] == 0 && p.character(q.Criterion, true) && p.choices(q.Criterion) && requirement(q.Required, false, func(id uint16) bool { return p.hist.Cleared[id] != 0 })
}
func permitted(f []string) bool {
	for _, s := range f {
		if s != "exposed by npc" {
			return false
		}
	}
	return true
}
func (c *Catalog) dpl(start player, ids []uint16, level byte, upper bool) map[uint16]bool {
	if !start.initialized || start.level < level || start.job > 15 || start.grow>>4 > 3 || start.grow&15 > 5 {
		return nil
	}
	added := map[uint16]bool{}
	var scope []uint16
	for _, id := range ids {
		if c.tickets[id] != nil {
			scope = append(scope, id)
		}
	}
	slices.SortFunc(scope, func(a, b uint16) int {
		first, second := c.tickets[a], c.tickets[b]
		if first.MinimumLevel != second.MinimumLevel {
			return int(first.MinimumLevel) - int(second.MinimumLevel)
		}
		return int(a) - int(b)
	})
	selected := map[uint16]bool{}
	queue := slices.Clone(scope)
	for len(queue) > 0 {
		id := queue[0]
		queue = queue[1:]
		if selected[id] {
			continue
		}
		selected[id] = true
		if q := c.tickets[id]; q != nil {
			queue = append(queue, q.Children...)
		}
	}
	legacy := upper && c.tickets[6410] == nil && c.tickets[6411] == nil && c.tickets[6412] == nil && c.tickets[6413] != nil && c.tickets[6413].LegacyUpperAwakening
	qualify := func(p player, q *Ticket, nested bool) bool {
		checkStage := q.AdvancementChain == 0 && q.AwakeningAward == nil
		if !p.character(q.Criterion, checkStage) || !q.Supported || !permitted(q.ProhibitedFlags) || !slices.Contains([]string{"", "normal", "side", "sub", "epic", "achievement", "common unique", "realization", "system", "episode"}, q.Grade) || !p.choices(q.Criterion) || !p.advancement(q.Criterion) {
			return false
		}
		if !nested && q.Parent != 0 {
			parent := c.tickets[q.Parent]
			if parent == nil || !p.hist.preceding(&parent.Criterion, q.Id) {
				return false
			}
		}
		if legacy && q.Id == 6413 {
			return true
		}
		return requirement(q.Required, true, func(id uint16) bool {
			if p.hist.Cleared[id] != 0 {
				return true
			}
			x := c.tickets[id]
			if selected[id] || x == nil || !x.Supported || len(x.ProhibitedFlags) != 0 || x.AdvancementChain != 0 || x.AwakeningAward != nil {
				return false
			}
			criterion := x.Criterion
			criterion.MaximumLevel = 255
			return p.character(criterion, true) && p.choices(criterion)
		})
	}
	clone := func(p player) player {
		p.hist = history{maps.Clone(p.hist.Active), maps.Clone(p.hist.Cleared)}
		if p.hist.Cleared == nil {
			p.hist.Cleared = map[uint16]byte{}
		}
		return p
	}
	var tree func(*player, *Ticket, map[uint16]bool, *[]uint16, bool) bool
	tree = func(p *player, q *Ticket, visiting map[uint16]bool, plan *[]uint16, nested bool) bool {
		if p.hist.Cleared[q.Id] != 0 {
			return true
		}
		if visiting[q.Id] || len(visiting) >= 511 || !qualify(*p, q, nested) {
			return false
		}
		visiting[q.Id] = true
		for _, id := range q.Children {
			if p.hist.Cleared[id] != 0 {
				continue
			}
			x := c.tickets[id]
			if x == nil || x.Parent != q.Id || !tree(p, x, visiting, plan, true) {
				return false
			}
		}
		if !qualify(*p, q, nested) {
			return false
		}
		flag := byte(1)
		if a, ok := p.hist.Active[q.Id]; ok && a.Answer != nil {
			if *a.Answer >= uint16(q.QuestionChoices) || *a.Answer >= 255 {
				return false
			}
			flag = byte(*a.Answer + 1)
		} else if q.ObjectiveType == "question" && q.QuestionChoices == 0 {
			return false
		}
		p.hist.Cleared[q.Id] = flag
		delete(p.hist.Active, q.Id)
		*plan = append(*plan, q.Id)
		for _, id := range q.CascadeClear {
			if id == 0 || id >= 30000 || id == q.Id {
				return false
			}
			if p.hist.Cleared[id] == 0 {
				p.hist.Cleared[id] = 1
				*plan = append(*plan, id)
			}
			delete(p.hist.Active, id)
		}
		if a := q.AwakeningAward; a != nil {
			p.grow = p.grow&15 | a.Stage<<4
		}
		delete(visiting, q.Id)
		return true
	}
	current := clone(start)
	for {
		changed := false
		for _, id := range scope {
			if current.hist.Cleared[id] != 0 {
				continue
			}
			next := clone(current)
			var planned []uint16
			if tree(&next, c.tickets[id], map[uint16]bool{}, &planned, false) {
				current = next
				changed = true
				for _, n := range planned {
					added[n] = true
				}
			}
		}
		if !changed {
			break
		}
	}
	for _, id := range scope {
		q := c.tickets[id]
		if start.hist.Cleared[id] == 0 && start.character(q.Criterion, q.AdvancementChain == 0 && q.AwakeningAward == nil) && (!q.Supported || !permitted(q.ProhibitedFlags)) {
			return nil
		}
	}
	return added
}
