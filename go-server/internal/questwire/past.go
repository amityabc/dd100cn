package questwire

import (
	"context"
	"encoding/json"
	"slices"

	"s9a1/server/internal/storage"
)

type pastQuest struct {
	Criterion
	Level           byte
	Awakening       int
	Epic            bool
	Hash            string
	QuestionChoices byte
}

func (q pastQuest) MarshalJSON() ([]byte, error) {
	// The past-quest directory exposes only the eligibility fields used when
	// skipping an older story tree.
	return json.Marshal(struct {
		Id, Parent           uint16
		Level                byte
		Job, Grow, Awakening int
		Epic                 bool
		Children, Collisions []uint16
		Required             [][]uint16
		Answers              []AnswerRequirement
		Targets, NonTargets  []Target
		Hash                 string
		QuestionChoices      byte
	}{
		q.Id, q.Parent, q.Level, q.Job, q.Grow, q.Awakening, q.Epic,
		q.Children, q.Collisions, q.Required, q.Answers, q.Targets, q.NonTargets, q.Hash, q.QuestionChoices,
	})
}
func (catalog *Catalog) Past(ctx context.Context, c storage.Character, preview bool) (storage.TownQuestMutation, []uint16, error) {
	var out storage.TownQuestMutation
	p, err := playerOf(c)
	if err != nil || p.level < 17 || !p.initialized || p.hist.Cleared == nil || catalog.past == nil || len(catalog.past) >= 30000 {
		return out, nil, storage.ErrInvalid
	}
	book, rows, err := questBook(c)
	if err != nil {
		return out, nil, err
	}
	defs := make(map[uint16]pastQuest, len(catalog.past))
	for _, q := range catalog.past {
		if q.Id == 0 || q.Id >= 30000 || defs[q.Id].Id != 0 {
			return out, nil, storage.ErrInvalid
		}
		defs[q.Id] = q
	}
	matches := func(q pastQuest) bool {
		return q.Level < p.level && p.matches(Target{q.Job, q.Grow, q.Awakening}) && (len(q.Targets) == 0 || slices.ContainsFunc(q.Targets, p.matches)) && !slices.ContainsFunc(q.NonTargets, p.matches)
	}
	flags := p.hist.Cleared
	var addTree func(q pastQuest, tree map[uint16]byte, path map[uint16]bool) (bool, error)
	addTree = func(q pastQuest, tree map[uint16]byte, path map[uint16]bool) (bool, error) {
		flag := func(id uint16) byte { return max(flags[id], tree[id]) }
		if q.Id == 0 || !matches(q) || path[q.Id] {
			return false, nil
		}
		path[q.Id] = true
		defer delete(path, q.Id)
		for _, id := range q.Collisions {
			if _, active := p.hist.Active[id]; flag(id) != 0 || active {
				return false, nil
			}
		}
		for _, answer := range q.Answers {
			if flag(answer.Quest) != answer.Answer+1 {
				return false, nil
			}
		}
		if !requirement(q.Required, true, func(id uint16) bool { return flag(id) != 0 }) {
			return false, nil
		}
		for _, id := range q.Children {
			if flag(id) != 0 {
				continue
			}
			child := defs[id]
			if child.Id == 0 || child.Parent != q.Id {
				return false, nil
			}
			if ok, err := addTree(child, tree, path); !ok || err != nil {
				return false, err
			}
		}
		clear := byte(1)
		if selected := p.hist.Active[q.Id].Answer; selected != nil {
			if *selected >= uint16(q.QuestionChoices) {
				return false, storage.ErrInvalid
			}
			clear = byte(*selected) + 1
		}
		tree[q.Id] = clear
		return true, nil
	}
	var changed bool
	var removed []uint16
	completed := map[uint16]bool{}
	for {
		advanced := false
		for _, q := range catalog.past {
			if err := ctx.Err(); err != nil {
				return out, nil, err
			}
			if !q.Epic || q.Parent != 0 || flags[q.Id] != 0 || !matches(q) || !requirement(q.Required, true, func(id uint16) bool { return flags[id] != 0 }) {
				continue
			}
			tree := map[uint16]byte{}
			ok, err := addTree(q, tree, map[uint16]bool{})
			if err != nil {
				return out, nil, err
			}
			if !ok {
				continue
			}
			for id, flag := range tree {
				flags[id] = flag
				completed[id] = true
			}
			advanced = true
		}
		if !advanced {
			break
		}
		changed = true
	}
	if !changed {
		return out, nil, nil
	}
	for id, a := range p.hist.Active {
		if !completed[id] {
			continue
		}
		if q := catalog.ordinary[id]; q == nil || !validActive(q, a) {
			return out, nil, storage.ErrInvalid
		}
		delete(rows, id)
		removed = append(removed, id)
	}
	if !preview {
		book["Active"], _ = json.Marshal(rows)
		book["Cleared"], _ = json.Marshal(flags)
		out.Quests, _ = json.Marshal(book)
	}
	return out, removed, nil
}
