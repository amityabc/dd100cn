package questwire

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"slices"
	"time"

	"s9a1/server/internal/storage"
)

type ClassRequest struct {
	Opcode     uint16 `json:"-"`
	Slot       uint16
	Grow, Mode byte
}

func ReadClass(op uint16, b []byte) (ClassRequest, error) {
	r := ClassRequest{Opcode: op}
	bad := errors.New("native class change request boundary mismatch")
	switch op {
	case 494:
		if len(b) != 4 {
			return r, bad
		}
		r.Slot, r.Grow, r.Mode = little.Uint16(b), b[2], b[3]
	case 777:
		if len(b) != 15 {
			return r, bad
		}
		r.Grow, r.Mode = b[13], b[14]
	case 1880:
		if len(b) != 14 {
			return r, bad
		}
		r.Grow = b[13]
	default:
		return r, bad
	}
	return r, nil
}

type ClassChangeContext struct {
	Now     time.Time
	Tickets map[uint32]struct {
		Template     json.RawMessage
		MaximumLevel byte
	}
	SupportsBranch func(byte, byte) bool
	FreeSkills     func(byte, byte, byte) map[uint16]byte
	Cost           func(byte, byte) (uint32, error)
	Stats          func(storage.Character, byte) (json.RawMessage, error)
}

func (catalog *Catalog) ClassChange(c storage.Character, in storage.Inventory, req ClassRequest, opts ClassChangeContext) (storage.TownQuestMutation, error) {
	var out storage.TownQuestMutation
	p, err := playerOf(c)
	if err != nil || !p.initialized || c.Level == 0 || c.Level > 100 || c.Job > 15 || c.Job == 9 || c.Job == 10 || req.Grow < 1 || req.Grow > 5 || !opts.SupportsBranch(c.Job, req.Grow) {
		return out, storage.ErrInvalid
	}
	var current struct {
		ClassReplacementCount byte
		Progression           struct{ Hp uint32 }
	}
	if json.Unmarshal(c.Gameplay, &current) != nil {
		return out, storage.ErrInvalid
	}
	var source *storage.Asset
	var cost uint32
	target := req.Grow
	switch req.Opcode {
	case 1880:
		if p.grow == target {
			return out, nil
		}
		if p.grow != 0 {
			return out, storage.ErrInvalid
		}
	case 777:
		if req.Mode > 1 || p.grow > 5 || current.ClassReplacementCount > 127 {
			return out, storage.ErrInvalid
		}
		if p.grow == 0 {
			return out, skillError(19)
		}
		if p.grow == target {
			return out, nil
		}
		if current.ClassReplacementCount == 127 {
			return out, storage.ErrInvalid
		}
		cost, err = opts.Cost(c.Level, current.ClassReplacementCount)
		if err != nil {
			return out, err
		}
		if in.Gold < int64(cost) {
			return out, skillError(22)
		}
	case 494:
		if req.Mode > 1 || current.Progression.Hp == 0 || p.grow > 0x35 || p.grow&15 == 0 || p.grow&15 > 5 {
			return out, storage.ErrInvalid
		}
		i := slices.IndexFunc(in.Items, func(a storage.Asset) bool { return a.Location == 0 && a.Slot == req.Slot })
		if i < 0 {
			return out, storage.ErrInvalid
		}
		source = &in.Items[i]
		ticket, ok := opts.Tickets[source.TemplateID]
		var spec grantTemplate
		var attrs struct{ ExpiresAt *int64 }
		if !ok || !storage.QuestOrdinarySource(*source, ticket.Template) || json.Unmarshal(ticket.Template, &spec) != nil || !spec.valid() || spec.Kind != 2 || spec.FirstBagSlot != 65 || spec.LastBagSlot != 120 ||
			!(req.Slot >= 3 && req.Slot <= 8 || req.Slot >= 65 && req.Slot <= min(120, 96+in.BagExpansion)) || guidKey(source.UID) == "" || source.Count == 0 || json.Unmarshal(source.Attributes, &attrs) != nil ||
			c.Level < spec.MinimumLevel || c.Level > ticket.MaximumLevel || spec.UsableJobs&(1<<c.Job) == 0 {
			return out, storage.ErrInvalid
		}
		now := opts.Now.Unix()
		if now <= 0 || attrs.ExpiresAt != nil && *attrs.ExpiresAt <= now || spec.ResourceExpiration != nil && *spec.ResourceExpiration <= now ||
			(spec.ResourceExpiration != nil || spec.RelativeLifetimeSeconds != 0 || spec.DailyDeletion || spec.WeeklyDeletionDay != nil) && attrs.ExpiresAt == nil {
			return out, storage.ErrInvalid
		}
		for id := range p.hist.Active {
			q := catalog.ordinary[id]
			if q == nil || q.AdvancementChain != 0 || q.AwakeningAward != nil {
				return out, storage.ErrInvalid
			}
		}
		target = p.grow&0xf0 | req.Grow
		if p.grow == target {
			return out, nil
		}
	}
	if req.Opcode != 1880 && !opts.SupportsBranch(c.Job, p.grow&15) {
		return out, storage.ErrInvalid
	}
	next, err := catalog.classBuilds(c, p, target, req.Opcode != 1880, opts)
	if err != nil {
		return out, err
	}
	c.Stats, err = opts.Stats(c, target)
	if err != nil {
		return out, err
	}
	if req.Opcode == 777 {
		next["ClassReplacementCount"], _ = json.Marshal(current.ClassReplacementCount + 1)
	}
	c.Gameplay, _ = json.Marshal(next)
	newReceipts := map[string]json.RawMessage{}
	if source != nil {
		before := *source
		in, err = storage.QuestConsumeInstance(in, before.UID)
		if err != nil {
			return out, err
		}
		var spec grantTemplate
		_ = json.Unmarshal(opts.Tickets[before.TemplateID].Template, &spec)
		key := fmt.Sprintf("class-ticket:%s:%d", guidKey(before.UID), c.Revision+1)
		input, _ := json.Marshal(struct {
			Request  ClassRequest
			Source   string
			Template uint32
			Hash     string
		}{req, wireGUID(before.UID), before.TemplateID, spec.ResourceHash})
		result, _ := json.Marshal(struct {
			Version       uint16
			Before, After byte
		}{1, p.grow, target})
		newReceipts[key], _ = json.Marshal(receipt{Key: key, Input: string(input), Result: string(result), Revision: c.Revision + 1, Grants: json.RawMessage("[]")})
	}
	in.Gold -= int64(cost)
	out.Reward = &storage.QuestRewardMutation{Character: c, Inventory: in, Receipts: newReceipts}
	return out, nil
}

func (catalog *Catalog) classBuilds(c storage.Character, p player, target byte, replace bool, opts ClassChangeContext) (skillPage, error) {
	var doc, progress skillPage
	if json.Unmarshal(c.Gameplay, &doc) != nil || json.Unmarshal(doc["Progression"], &progress) != nil || progress == nil {
		return nil, storage.ErrInvalid
	}
	initial, ok := catalog.initialSkills[c.Job]
	if !ok || !validSkills(initial) {
		return nil, storage.ErrInvalid
	}
	oldRanks := opts.FreeSkills(c.Job, c.Level, p.grow)
	newRanks := opts.FreeSkills(c.Job, c.Level, target)
	common := map[uint16]byte{}
	for _, row := range initial {
		common[row.Id] = row.Level
	}
	build := func(points skillPage, rows json.RawMessage) (skillBuild, error) {
		var b skillBuild
		if json.Unmarshal(rows, &b.Rows) != nil || !validSkills(b.Rows) || json.Unmarshal(points["Sp"], &b.Sp) != nil || json.Unmarshal(points["Tp"], &b.Tp) != nil {
			return b, storage.ErrInvalid
		}
		if replace {
			sp, tp := int64(b.Sp), int64(b.Tp)
			retained := make([]questSkill, 0, len(b.Rows))
			for _, row := range b.Rows {
				d, ok := catalog.definition(c.Job, row.Id)
				if !ok || len(d.Costs) == 0 || d.QuestDiscount != nil && len(d.QuestDiscount.Amounts) == 0 {
					return b, storage.ErrInvalid
				}
				shared := max(d.FreeLevel, common[row.Id])
				oldFree := max(shared, oldRanks[row.Id])
				if row.Level > oldFree && !d.OrdinarySp && len(d.TpCosts) == 0 {
					return b, storage.ErrInvalid
				}
				for rank := int(oldFree) + 1; rank <= int(row.Level); rank++ {
					if len(d.TpCosts) > 0 {
						tp += int64(d.TpCosts[min(rank, len(d.TpCosts))-1])
					} else {
						sp += int64(d.cost(rank, p.hist.Cleared))
					}
				}
				row.Level = min(row.Level, shared)
				if row.Level != 0 {
					retained = append(retained, row)
				}
			}
			if sp > math.MaxUint16 || tp > math.MaxUint16 {
				return b, skillError(18)
			}
			b = skillBuild{retained, uint16(sp), uint16(tp)}
		}
		used := map[uint16]bool{}
		for _, row := range b.Rows {
			used[row.Slot] = true
		}
		merge := func(id uint16, level byte) error {
			d, ok := catalog.definition(c.Job, id)
			if !ok || level == 0 {
				return storage.ErrInvalid
			}
			i := slices.IndexFunc(b.Rows, func(row questSkill) bool { return row.Id == id })
			if i >= 0 {
				b.Rows[i].Level = max(b.Rows[i].Level, level)
				return nil
			}
			slot := nextSkillSlot(used, d.Active)
			if slot > 254 {
				return storage.ErrInvalid
			}
			used[slot] = true
			b.Rows = append(b.Rows, questSkill{Id: id, Slot: slot, Level: level})
			return nil
		}
		if replace {
			for _, row := range initial {
				if err := merge(row.Id, row.Level); err != nil {
					return b, err
				}
			}
		}
		for _, id := range sortedKeys(newRanks) {
			if err := merge(id, newRanks[id]); err != nil {
				return b, err
			}
		}
		slices.SortFunc(b.Rows, func(a, z questSkill) int { return int(a.Id) - int(z.Id) })
		if !validSkills(b.Rows) {
			return b, storage.ErrInvalid
		}
		return b, nil
	}
	main, err := build(progress, doc["Skills"])
	if err != nil {
		return nil, err
	}
	progress["GrowType"], _ = json.Marshal(target)
	applyBuild(doc, progress, main, false)
	for _, key := range []string{"DuelSkills", "SkillStyles"} {
		if doc[key] == nil || string(doc[key]) == "null" {
			continue
		}
		var raw skillPage
		if json.Unmarshal(doc[key], &raw) != nil || raw == nil {
			return nil, storage.ErrInvalid
		}
		page := raw
		if key == "SkillStyles" {
			if page, _, err = validateSkillStyle(raw, doc["CommandLocks"]); err != nil {
				return nil, err
			}
		}
		b, err := build(page, page["Skills"])
		if err != nil {
			return nil, err
		}
		page["Skills"], _ = json.Marshal(b.Rows)
		page["Sp"], _ = json.Marshal(b.Sp)
		page["Tp"], _ = json.Marshal(b.Tp)
		if key == "SkillStyles" {
			raw["Other"], _ = json.Marshal(page)
		}
		doc[key], _ = json.Marshal(raw)
	}
	return doc, nil
}
