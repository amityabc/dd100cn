package questwire

import (
	"context"
	"encoding/json"
	"maps"
	"s9a1/server/internal/storage"
	"slices"
)

// Upgrade tickets choose old-level side quests using the independent ticket
// definitions. Their underlying combat or hand-in objective is never executed.
func (catalog *Catalog) levelSideQuests(ctx context.Context, c storage.Character) (json.RawMessage, []uint16, error) {
	p, err := playerOf(c)
	if err != nil || p.hist.Cleared == nil || catalog.extra == nil {
		return nil, nil, storage.ErrInvalid
	}
	book, rows, err := questBook(c)
	if err != nil {
		return nil, nil, err
	}
	var candidates []*Ticket
	selected := map[uint16]bool{}
	var queue []uint16
	for i := range catalog.extra {
		q := &catalog.extra[i]
		if q.Grade == "side" {
			candidates = append(candidates, q)
			selected[q.Id] = true
			queue = append(queue, q.Children...)
		}
	}
	for i := 0; i < len(queue); i++ {
		id := queue[i]
		if selected[id] {
			continue
		}
		selected[id] = true
		if q := catalog.tickets[id]; q != nil {
			queue = append(queue, q.Children...)
		}
	}
	slices.SortFunc(candidates, func(a, b *Ticket) int {
		if a.MinimumLevel != b.MinimumLevel {
			return int(a.MinimumLevel) - int(b.MinimumLevel)
		}
		return int(a.Id) - int(b.Id)
	})
	qualified := func(p player, q *Ticket) bool {
		if q == nil || q.Grade != "side" || !q.Supported || len(q.ProhibitedFlags) != 0 || q.AdvancementChain != 0 || q.AwakeningAward != nil || len(q.SkillEffects) != 0 || q.EquipmentSlotQuest || len(q.CascadeClear) != 0 || q.RewardType != "" && q.RewardType != "item" ||
			!p.character(q.Criterion, true) || !p.choices(q.Criterion) {
			return false
		}
		return requirement(q.Required, true, func(id uint16) bool {
			if p.hist.Cleared[id] != 0 {
				return true
			}
			prior := catalog.tickets[id]
			if selected[id] || prior == nil || !prior.Supported || len(prior.ProhibitedFlags) != 0 || prior.AdvancementChain != 0 || prior.AwakeningAward != nil {
				return false
			}
			q := prior.Criterion
			q.MaximumLevel = 255
			return p.character(q, true) && p.choices(q)
		})
	}
	var tree func(*player, *Ticket, map[uint16]bool) (bool, error)
	tree = func(p *player, q *Ticket, path map[uint16]bool) (bool, error) {
		if err := ctx.Err(); err != nil {
			return false, err
		}
		if path[q.Id] || len(path) >= 511 || !qualified(*p, q) {
			return false, nil
		}
		path[q.Id] = true
		defer delete(path, q.Id)
		for _, id := range q.Children {
			if p.hist.Cleared[id] != 0 {
				continue
			}
			child := catalog.tickets[id]
			if child == nil || child.Parent != q.Id {
				return false, nil
			}
			if ok, err := tree(p, child, path); !ok || err != nil {
				return false, err
			}
		}
		if !qualified(*p, q) {
			return false, nil
		}
		flag := byte(1)
		if answer := p.hist.Active[q.Id].Answer; answer != nil {
			if *answer >= uint16(q.QuestionChoices) {
				return false, storage.ErrInvalid
			}
			flag = byte(*answer) + 1
		} else if q.ObjectiveType == "question" && q.QuestionChoices == 0 {
			return false, storage.ErrInvalid
		}
		p.hist.Cleared[q.Id] = flag
		delete(p.hist.Active, q.Id)
		return true, nil
	}
	prior := p.hist.Cleared
	for {
		progress := false
		for _, q := range candidates {
			if err := ctx.Err(); err != nil {
				return nil, nil, err
			}
			if q.Parent != 0 || p.hist.Cleared[q.Id] != 0 || !qualified(p, q) {
				continue
			}
			trial := p
			trial.hist = history{maps.Clone(p.hist.Active), maps.Clone(p.hist.Cleared)}
			ok, err := tree(&trial, q, map[uint16]bool{})
			if err != nil {
				return nil, nil, err
			}
			if ok {
				p = trial
				progress = true
			}
		}
		if !progress {
			break
		}
	}
	var completed []uint16
	for id := range p.hist.Cleared {
		if prior[id] != 0 {
			continue
		}
		delete(rows, id)
		completed = append(completed, id)
	}
	if len(completed) == 0 {
		return nil, nil, nil
	}
	book["Active"], _ = json.Marshal(rows)
	book["Cleared"], _ = json.Marshal(p.hist.Cleared)
	b, err := json.Marshal(book)
	return b, completed, err
}
