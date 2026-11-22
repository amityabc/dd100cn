package questwire

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"maps"
	"math"
	"slices"

	"s9a1/server/internal/storage"
)

type questTicketItem struct {
	Template   json.RawMessage
	Definition questTicketDefinition
}

type questTicketDefinition struct {
	Kind, MinimumLevel, MaximumLevel               byte
	TemplateId, NativeAction, CooldownMilliseconds uint32
	ResourceHash                                   string
	UsableJobs, ImpossibleJobs                     uint16
	UsablePlaces                                   []string
	Arguments                                      []uint32
	UnableUseGrowTypes                             []struct{ Job, Grow byte }
}

type QuestTicketRequest struct {
	Slot                                   uint16
	List                                   byte
	Reserved, Action, Argument0, Argument1 uint32
	Sequence                               uint16
}

type TicketContext struct {
	CompleteContext
	SupportsBranch func(byte, byte) bool
}

func ReadQuestTicket(seq uint16, body []byte) (QuestTicketRequest, error) {
	if len(body) != 19 {
		return QuestTicketRequest{}, storage.ErrInvalid
	}
	return QuestTicketRequest{little.Uint16(body), body[2], little.Uint32(body[3:]), little.Uint32(body[7:]), little.Uint32(body[11:]), little.Uint32(body[15:]), seq}, nil
}

func (r QuestTicketRequest) Reply(ok bool) []byte {
	b := []byte{0, 0, 0}
	if ok {
		b = []byte{1}
	}
	b = little.AppendUint16(b, r.Slot)
	return little.AppendUint32(append(b, r.List), r.Action)
}

func (catalog *Catalog) TicketUse(ctx context.Context, c storage.Character, in storage.Inventory, req QuestTicketRequest, preview uint32, room bool,
	opts TicketContext) (storage.TownQuestMutation, []uint16, error) {
	var out storage.TownQuestMutation
	p, err := playerOf(c)
	if err != nil || !p.initialized || p.job > 15 || in.CharacterID != c.ID || opts.Now.UnixMilli() <= 0 ||
		preview == 0 && (req.List != 0 || req.Reserved != 0 || req.Argument0 != 0 || req.Argument1 != 0) {
		return out, nil, storage.ErrInvalid
	}
	var source storage.Asset
	nows := opts.Now.Unix()
	for _, a := range in.Items {
		if a.Location != 0 || preview == 0 && a.Slot != req.Slot || preview != 0 && a.TemplateID != preview {
			continue
		}
		if preview != 0 {
			var attr struct {
				Locked    bool
				ExpiresAt *int64
			}
			if json.Unmarshal(a.Attributes, &attr) != nil || attr.Locked || attr.ExpiresAt != nil && *attr.ExpiresAt <= nows || source.UID != "" && a.Slot >= source.Slot {
				continue
			}
		}
		source = a
	}
	rule, found := catalog.growth.QuestTickets[source.TemplateID]
	t := rule.Definition
	if !found || source.TemplateID != t.TemplateId || source.Count == 0 || guidKey(source.UID) == "" || !storage.QuestOrdinarySource(source, rule.Template) ||
		preview == 0 && req.Action != t.NativeAction || preview != 0 && t.NativeAction != 77 && t.NativeAction != 101 ||
		p.level < t.MinimumLevel || p.level > t.MaximumLevel || t.UsableJobs&(1<<p.job) == 0 || t.ImpossibleJobs&(1<<p.job) != 0 ||
		slices.ContainsFunc(t.UnableUseGrowTypes, func(row struct{ Job, Grow byte }) bool { return row.Job == p.job && row.Grow == p.grow&15 }) ||
		len(t.UsablePlaces) != 0 && !slices.Contains(t.UsablePlaces, "village") && !(room && slices.Contains(t.UsablePlaces, "seria room")) {
		return out, nil, storage.ErrInvalid
	}
	var saved struct{ ExpiresAt *int64 }
	var spec grantTemplate
	var cooldowns struct{ QuestTicketCooldowns map[uint32]int64 }
	if len(t.ResourceHash) != 64 || json.Unmarshal(rule.Template, &spec) != nil || !spec.valid() || spec.Kind != 2 || spec.ResourceHash != t.ResourceHash ||
		json.Unmarshal(source.Attributes, &saved) != nil || saved.ExpiresAt != nil && *saved.ExpiresAt <= nows || spec.ResourceExpiration != nil && *spec.ResourceExpiration <= nows ||
		json.Unmarshal(c.Gameplay, &cooldowns) != nil || cooldowns.QuestTicketCooldowns[t.TemplateId] > opts.Now.UnixMilli() {
		return out, nil, storage.ErrInvalid
	}
	if _, err = hex.DecodeString(t.ResourceHash); err != nil {
		return out, nil, storage.ErrInvalid
	}
	plan, err := catalog.planTicket(ctx, p, t, opts.SupportsBranch)
	if err != nil {
		return out, nil, err
	}
	book, rows, err := questBook(c)
	if err != nil || p.hist.Cleared == nil {
		return out, nil, storage.ErrInvalid
	}
	var doc skillPage
	if json.Unmarshal(c.Gameplay, &doc) != nil {
		return out, nil, storage.ErrInvalid
	}
	next := c
	currentGrow := p.grow
	var gold int64
	titles := map[uint32]uint32{}
	for _, step := range plan.steps {
		q := step.quest
		markComplete(p.hist, rows, q.Id, step.flag)
		for _, id := range q.CascadeClear {
			flag := p.hist.Cleared[id]
			markComplete(p.hist, rows, id, max(1, flag))
		}
		book["Active"], _ = json.Marshal(rows)
		book["Cleared"], _ = json.Marshal(p.hist.Cleared)
		doc["Quests"], _ = json.Marshal(book)
		before := next
		next.Gameplay, _ = json.Marshal(doc)
		growChanged := currentGrow != step.grow
		if growChanged {
			next, _, err = opts.Growth(next, 0, 0, step.grow, false)
			if err != nil {
				return out, nil, storage.ErrInvalid
			}
			currentGrow = step.grow
		}
		if len(q.SkillEffects) != 0 {
			next, err = catalog.applySkillEffects(before, next, &Quest{Criterion: q.Criterion, SkillEffects: q.SkillEffects}, opts.CompleteContext)
			if err != nil {
				return out, nil, err
			}
		}
		if growChanged || len(q.SkillEffects) != 0 {
			if err = json.Unmarshal(next.Gameplay, &doc); err != nil {
				return out, nil, err
			}
		}
		if t.Kind == ticketSideGold {
			gold += int64(q.GoldAward)
		}
		if t.Kind == ticketAchievement || t.Kind == ticketSideTitle {
			for _, r := range q.RewardRows {
				if slices.Contains(q.TitleRewards, r.Item.Template) && (r.Job < 0 || r.Job == int(c.Job)) && (r.Grow < 0 || r.Grow == int(currentGrow&15)) {
					if math.MaxUint32-titles[r.Item.Template] < r.Item.Count {
						return out, nil, storage.ErrInvalid
					}
					titles[r.Item.Template] += r.Item.Count
				}
			}
		}
	}
	done := slices.Sorted(maps.Keys(plan.added))
	if preview != 0 {
		return out, done, nil
	}
	if t.CooldownMilliseconds > 0 {
		if cooldowns.QuestTicketCooldowns == nil {
			cooldowns.QuestTicketCooldowns = map[uint32]int64{}
		}
		if _, exists := cooldowns.QuestTicketCooldowns[t.TemplateId]; !exists && len(cooldowns.QuestTicketCooldowns) >= 32768 {
			return out, nil, storage.ErrInvalid
		}
		cooldowns.QuestTicketCooldowns[t.TemplateId] = opts.Now.UnixMilli() + int64(t.CooldownMilliseconds)
		doc["QuestTicketCooldowns"], _ = json.Marshal(cooldowns.QuestTicketCooldowns)
		next.Gameplay, _ = json.Marshal(doc)
	}
	functional, titleRows := []Material{}, []Material{}
	if plan.upper {
		functional = append(functional, catalog.tickets[6413].Rewards...)
	}
	for _, id := range slices.Sorted(maps.Keys(titles)) {
		titleRows = append(titleRows, Material{id, titles[id]})
	}
	grants := make([]storage.QuestGrant, 0, len(titleRows)+len(functional))
	granted := make([]savedGrant, 0, cap(grants))
	for _, m := range append(slices.Clone(titleRows), functional...) {
		g, frozen, err := rewardAsset(in, 0, t.ResourceHash, m, catalog.rewards[m.Template], opts.Now)
		if err != nil {
			return out, nil, err
		}
		grants, granted = append(grants, g), append(granted, frozen)
	}
	if gold > 0 {
		var profile struct{ GoldExpansion byte }
		if json.Unmarshal(c.Gameplay, &profile) != nil || profile.GoldExpansion > 8 {
			return out, nil, storage.ErrInvalid
		}
		limit := opts.GoldCarryLimits[c.Level]
		if profile.GoldExpansion != 0 {
			limit = (4 + int64(profile.GoldExpansion)) * 100000000
		}
		if limit <= 0 || in.Gold+gold > min(limit, math.MaxUint32) {
			return out, nil, storage.ErrCapacity
		}
	}
	assets, err := storage.QuestConsumeInstance(in, source.UID)
	if err != nil {
		return out, nil, err
	}
	assets, err = storage.QuestLoot(assets, nil, grants, in.Gold+gold, opts.Now)
	if err != nil {
		return out, nil, err
	}
	key := fmt.Sprintf("quest-ticket:%s:%d", guidKey(source.UID), c.Revision+1)
	input, _ := json.Marshal(struct {
		Request QuestTicketRequest
		Source  string
		Ticket  uint32
		Hash    string
	}{req, wireGUID(source.UID), t.TemplateId, t.ResourceHash})
	result, _ := json.Marshal(struct {
		Version       byte
		Quests        []uint16
		Gold          int64
		Titles, Items []Material
		Grow          byte
	}{2, done, gold, titleRows, functional, currentGrow})
	grantsJSON, _ := json.Marshal(granted)
	frozen, _ := json.Marshal(receipt{Key: key, Input: string(input), Result: string(result), Revision: c.Revision + 1, Grants: grantsJSON})
	out.Reward = &storage.QuestRewardMutation{Character: next, Inventory: assets, Receipts: map[string]json.RawMessage{key: frozen}}
	return out, done, nil
}
