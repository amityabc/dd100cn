package questwire

import (
	"context"
	"maps"
	"slices"
	"strings"

	"s9a1/server/internal/storage"
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
	if start.level < level {
		return nil
	}
	t := questTicketDefinition{Kind: ticketExplicit}
	for _, id := range ids {
		t.Arguments = append(t.Arguments, uint32(id))
	}
	if upper {
		t.TemplateId = upperAwakeningTicket
	}
	plan, err := c.planTicket(context.Background(), start, t, nil)
	if err != nil {
		return nil
	}
	return plan.added
}

const (
	ticketFirst byte = iota
	ticketSecond
	ticketThird
	ticketExplicit
	ticketSideGold
	ticketAchievement
	ticketAll
	ticketAssist
	ticketEpic
	ticketSideTitle
	ticketGM
)

const upperAwakeningTicket uint32 = 490016791

type ticketStep struct {
	quest      *Ticket
	flag, grow byte
}

type ticketPlan struct {
	steps []ticketStep
	added map[uint16]bool
	upper bool
}

func (c *Catalog) planTicket(ctx context.Context, start player, t questTicketDefinition, supports func(byte, byte) bool) (ticketPlan, error) {
	var out ticketPlan
	if !start.initialized || start.level < 1 || start.job > 15 || start.grow>>4 > 3 || start.grow&15 > 5 || len(c.extra) == 0 {
		return out, storage.ErrInvalid
	}
	capLevel, stage := start.level, byte(0)
	switch t.Kind {
	case ticketFirst, ticketSecond, ticketThird:
		stage = t.Kind + 1
		branch := start.grow & 15
		if start.grow>>4 != stage-1 || start.level < [...]byte{50, 75, 100}[t.Kind] ||
			(start.job == 9 || start.job == 10) && branch != 0 || (start.job != 9 && start.job != 10) && (branch == 0 || supports == nil || !supports(start.job, branch)) {
			return out, storage.ErrInvalid
		}
	case ticketExplicit:
		if len(t.Arguments) == 0 || len(t.Arguments) > 511 {
			return out, storage.ErrInvalid
		}
		seen := map[uint32]bool{}
		for _, id := range t.Arguments {
			if id == 0 || id >= 30000 || seen[id] {
				return out, storage.ErrInvalid
			}
			seen[id] = true
		}
	case ticketEpic:
		if len(t.Arguments) != 1 || t.Arguments[0] == 0 || t.Arguments[0] > 255 {
			return out, storage.ErrInvalid
		}
		capLevel = min(capLevel, byte(t.Arguments[0]))
		if t.Arguments[0] == 75 {
			capLevel = min(capLevel, 74)
		}
	case ticketGM:
		capLevel = min(capLevel, 90)
	case ticketAll:
		if t.TemplateId == 490017395 || t.TemplateId == 10319239 {
			return out, storage.ErrInvalid
		}
	case ticketSideGold, ticketAchievement, ticketSideTitle:
	default:
		return out, storage.ErrInvalid
	}
	var scope []*Ticket
	if t.Kind == ticketExplicit {
		for _, id := range t.Arguments {
			if q := c.tickets[uint16(id)]; q != nil {
				scope = append(scope, q)
			}
		}
	}
	for i := range c.extra {
		if t.Kind == ticketExplicit {
			break
		}
		q := &c.extra[i]
		match := false
		switch t.Kind {
		case ticketFirst, ticketSecond, ticketThird:
			match = q.AdvancementChain == int(stage)+1 || q.AwakeningAward != nil && q.AwakeningAward.Stage == stage
		case ticketSideGold:
			match = q.Grade == "side"
		case ticketAchievement, ticketSideTitle:
			match = q.Grade == "side" || q.Grade == "achievement"
		case ticketAll:
			match = slices.Contains([]string{"", "normal", "epic", "side", "achievement", "common unique"}, q.Grade)
		case ticketEpic, ticketGM:
			match = q.Grade == "epic" && q.MinimumLevel <= capLevel
		}
		if match {
			scope = append(scope, q)
		}
	}
	slices.SortFunc(scope, func(a, b *Ticket) int {
		if a.MinimumLevel != b.MinimumLevel {
			return int(a.MinimumLevel) - int(b.MinimumLevel)
		}
		return int(a.Id) - int(b.Id)
	})
	selected := map[uint16]bool{}
	queue := make([]uint16, 0, len(scope))
	for _, q := range scope {
		queue = append(queue, q.Id)
	}
	for len(queue) > 0 {
		if err := ctx.Err(); err != nil {
			return out, err
		}
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
	legacy := t.Kind == ticketExplicit && t.TemplateId == upperAwakeningTicket && slices.Equal(t.Arguments, []uint32{6410, 6411, 6412, 6413}) &&
		c.tickets[6410] == nil && c.tickets[6411] == nil && c.tickets[6412] == nil && c.tickets[6413] != nil && c.tickets[6413].LegacyUpperAwakening
	ignoreExternal := t.Kind == ticketExplicit || t.Kind == ticketSideGold || stage != 0
	qualify := func(p player, q *Ticket, nested bool) bool {
		checkStage := q.AdvancementChain == 0 && q.AwakeningAward == nil
		if q.MinimumLevel > capLevel || !p.character(q.Criterion, checkStage) || !q.Supported || !permitted(q.ProhibitedFlags) ||
			len(q.ProhibitedFlags) != 0 && t.Kind != ticketExplicit && stage == 0 ||
			!slices.Contains([]string{"", "normal", "side", "sub", "epic", "achievement", "common unique", "realization", "system", "episode"}, q.Grade) ||
			!p.choices(q.Criterion) || !p.advancement(q.Criterion) {
			return false
		}
		if (t.Kind == ticketAchievement || t.Kind == ticketSideTitle) &&
			(!checkStage || len(q.SkillEffects) != 0 || q.RewardType == "slot expansion" || ticketTitleExcluded(q.Name)) {
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
			if !ignoreExternal || selected[id] || x == nil || !x.Supported || len(x.ProhibitedFlags) != 0 || x.AdvancementChain != 0 || x.AwakeningAward != nil {
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
	var tree func(*player, *Ticket, map[uint16]bool, *[]ticketStep, map[uint16]bool, bool) (bool, error)
	tree = func(p *player, q *Ticket, visiting map[uint16]bool, plan *[]ticketStep, added map[uint16]bool, nested bool) (bool, error) {
		if err := ctx.Err(); err != nil {
			return false, err
		}
		if p.hist.Cleared[q.Id] != 0 {
			return true, nil
		}
		if visiting[q.Id] || len(visiting) >= 511 || !qualify(*p, q, nested) {
			return false, nil
		}
		visiting[q.Id] = true
		for _, id := range q.Children {
			if p.hist.Cleared[id] != 0 {
				continue
			}
			x := c.tickets[id]
			if x == nil || x.Parent != q.Id {
				return false, nil
			}
			if ok, err := tree(p, x, visiting, plan, added, true); err != nil || !ok {
				return false, err
			}
		}
		if !qualify(*p, q, nested) {
			return false, nil
		}
		flag := byte(1)
		if a, ok := p.hist.Active[q.Id]; ok && a.Answer != nil {
			if *a.Answer >= uint16(q.QuestionChoices) || *a.Answer >= 255 {
				return false, storage.ErrInvalid
			}
			flag = byte(*a.Answer + 1)
		} else if q.ObjectiveType == "question" && q.QuestionChoices == 0 {
			return false, storage.ErrInvalid
		}
		p.hist.Cleared[q.Id] = flag
		delete(p.hist.Active, q.Id)
		added[q.Id] = true
		for _, id := range q.CascadeClear {
			if id == 0 || id >= 30000 || id == q.Id {
				return false, storage.ErrInvalid
			}
			if p.hist.Cleared[id] == 0 {
				p.hist.Cleared[id] = 1
				added[id] = true
			}
			delete(p.hist.Active, id)
		}
		if a := q.AwakeningAward; a != nil {
			p.grow = p.grow&15 | a.Stage<<4
		}
		*plan = append(*plan, ticketStep{q, flag, p.grow})
		delete(visiting, q.Id)
		return true, nil
	}
	current := clone(start)
	out.added = map[uint16]bool{}
	for {
		changed := false
		for _, q := range scope {
			if err := ctx.Err(); err != nil {
				return out, err
			}
			if current.hist.Cleared[q.Id] != 0 || t.Kind != ticketExplicit && q.Parent != 0 || !qualify(current, q, false) {
				continue
			}
			next := clone(current)
			var planned []ticketStep
			added := map[uint16]bool{}
			ok, err := tree(&next, q, map[uint16]bool{}, &planned, added, false)
			if err != nil {
				return out, err
			}
			if ok {
				current = next
				changed = true
				out.steps = append(out.steps, planned...)
				maps.Copy(out.added, added)
			}
		}
		if !changed {
			break
		}
	}
	if t.Kind == ticketExplicit {
		for _, q := range scope {
			if start.hist.Cleared[q.Id] == 0 && start.character(q.Criterion, q.AdvancementChain == 0 && q.AwakeningAward == nil) && (!q.Supported || !permitted(q.ProhibitedFlags)) {
				return out, storage.ErrInvalid
			}
		}
	}
	if len(out.steps) == 0 || stage != 0 && current.grow>>4 != stage {
		return out, storage.ErrInvalid
	}
	out.upper = legacy && out.added[6413]
	return out, nil
}

func ticketTitleExcluded(name string) bool {
	for _, phrase := range []string{"向阿尔伯特学技能", "死神的气息", "穿戴辅助装备", "佩戴魔法石", "梅尔文的高科技", "班图族的角质项链", "深海项链", "狙击镜的威力", "不灭之王波罗丁", "二次觉醒 - 龙跃武器", "铁鳞海贼团", "开启远古地下城之门", "镇魂曲", "异界初体验", "[勇者]", "[异次元裂缝]", "[深渊派对]", "[盾牌]", "贪食之巨兽", "风卷流云"} {
		if strings.Contains(name, phrase) {
			return true
		}
	}
	return false
}
