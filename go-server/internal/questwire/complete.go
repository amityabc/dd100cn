package questwire

import (
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"slices"
	"time"

	"s9a1/server/internal/storage"
)

type CompleteRequest struct{ Id, Selection, Multiplier, Tail uint16 }

func ReadComplete(body []byte) (CompleteRequest, error) {
	if len(body) != 10 || little.Uint16(body) != 34 {
		return CompleteRequest{}, errors.New("quest completion boundary mismatch")
	}
	return CompleteRequest{little.Uint16(body[2:]), little.Uint16(body[4:]), little.Uint16(body[6:]), little.Uint16(body[8:])}, nil
}

var ErrRewardResource = errors.New("quest reward item is unsupported or not prepared")

type CompleteContext struct {
	ChangeContext
	GoldCarryLimits map[byte]int64
	Growth          func(storage.Character, uint32, uint16, byte, bool) (storage.Character, uint32, error)
	FreeSkills      func(byte, byte, byte) map[uint16]byte
	SkillMaster     bool
}

type CompleteAction struct {
	Action
	Awakening, Experience uint32
}

type savedGrant struct {
	Template, Attributes json.RawMessage
	Count                uint32
	Raw119, Detail       []uint16
	PreserveIdentity     string
}

type completion struct {
	QuestId                        uint16
	ObjectiveHash                  string
	ExperienceAward                uint32
	Level                          byte
	Sp                             uint16
	GrowType                       byte
	Rewards                        []savedGrant
	DungeonRunId                   string `json:",omitempty"`
	QuestionAnswer                 *byte  `json:",omitempty"`
	ParentQuestId, TerminalQuestId uint16 `json:",omitempty"`
	GoldAward                      uint32
	DiscardedGoldAward             uint32 `json:",omitempty"`
	DailyDay                       int    `json:",omitempty"`
}

func completeInput(r CompleteRequest) string {
	b, _ := json.Marshal(r)
	return string(b)
}

func completionKey(q *Quest, day int) string {
	if q.Operations.Daily {
		return fmt.Sprintf("quest-daily:%d:%d", q.Id, day)
	}
	return fmt.Sprintf("quest:%d", q.Id)
}

func completionReplay(p player, in storage.Inventory, q *Quest, receipts map[string]json.RawMessage, r CompleteRequest, day int) (completion, bool) {
	key := completionKey(q, day)
	var saved receipt
	var v completion
	if json.Unmarshal(receipts[key], &saved) != nil || saved.Key != key || saved.Revision <= 0 || saved.Revision > in.Revision || saved.Input != completeInput(r) || p.hist.Cleared[q.Id] == 0 ||
		json.Unmarshal([]byte(saved.Result), &v) != nil || v.QuestId != q.Id || v.ObjectiveHash != q.Hash || v.Rewards == nil || v.DailyDay != day {
		return v, false
	}
	if q.Kind == question {
		_, active := p.hist.Active[q.Id]
		if active || !q.AcceptanceSupported || v.QuestionAnswer == nil || *v.QuestionAnswer >= q.QuestionChoices || p.hist.Cleared[q.Id] != *v.QuestionAnswer+1 {
			return v, false
		}
	} else if v.QuestionAnswer != nil {
		return v, false
	}
	return v, true
}

func rewardRows(p player, q *Quest, selection uint16) ([]Material, error) {
	if p.job > 15 || p.grow > 0x3f || p.grow&15 > 5 {
		return nil, storage.ErrInvalid
	}
	matches := func(r RewardRow) bool {
		return r.Job == -1 || r.Job == int(p.job) && (r.Grow == -1 || r.Grow == int(p.grow&15))
	}
	rewards := slices.Clone(q.Rewards)
	if len(q.RewardRows) != 0 {
		rewards = nil
		for _, row := range q.RewardRows {
			if matches(row) {
				rewards = append(rewards, row.Item)
			}
		}
	}
	if len(q.SelectionRewardRows) == 0 {
		if selection != math.MaxUint16 {
			return nil, storage.ErrInvalid
		}
		return rewards, nil
	}
	if q.Id != 6405 || q.Kind != seeking || q.Operations.Grade != "side" || q.Parent != 0 || len(q.Children) != 0 || q.AdvancementChain != 0 || q.AwakeningAward != nil || len(q.CascadeClear) != 0 {
		return nil, storage.ErrInvalid
	}
	if selection == math.MaxUint16 {
		if slices.ContainsFunc(q.SelectionRewardRows, matches) {
			return nil, storage.ErrInvalid
		}
		return rewards, nil
	}
	if int(selection) >= len(q.SelectionRewardRows) || !matches(q.SelectionRewardRows[selection]) {
		return nil, storage.ErrInvalid
	}
	return append(rewards, q.SelectionRewardRows[selection].Item), nil
}

func (catalog *Catalog) rewardGrant(p player, in storage.Inventory, q *Quest, m Material, now time.Time) (storage.QuestGrant, savedGrant, error) {
	return rewardAsset(in, q.Id, q.Hash, m, catalog.rewards[m.Template], now)
}

func rewardAsset(in storage.Inventory, id uint16, objectiveHash string, m Material, raw json.RawMessage, now time.Time) (storage.QuestGrant, savedGrant, error) {
	var t grantTemplate
	if json.Unmarshal(raw, &t) != nil || !t.valid() || m.Count == 0 || t.MythologyHash != nil || t.FateCollection {
		return storage.QuestGrant{}, savedGrant{}, ErrRewardResource
	}
	deadline := t.ResourceExpiration
	limit := func(ends int64) {
		if deadline == nil || ends < *deadline {
			deadline = &ends
		}
	}
	if t.RelativeLifetimeSeconds > 0 {
		limit(now.Unix() + int64(t.RelativeLifetimeSeconds))
	}
	local := now.In(time.FixedZone("UTC+8", 8*3600))
	reset := time.Date(local.Year(), local.Month(), local.Day(), 6, 0, 0, 0, local.Location())
	if t.DailyDeletion {
		if !reset.After(local) {
			reset = reset.AddDate(0, 0, 1)
		}
		limit(reset.Unix())
	} else if t.WeeklyDeletionDay != nil {
		reset = reset.AddDate(0, 0, (7+int(*t.WeeklyDeletionDay)-int(reset.Weekday()))%7)
		if !reset.After(local) {
			reset = reset.AddDate(0, 0, 7)
		}
		limit(reset.Unix())
	}
	if t.FatigueTimeMonthlyDeletion {
		reset = time.Date(local.Year(), local.Month(), 1, 6, 0, 0, 0, local.Location())
		if !reset.After(local) {
			reset = reset.AddDate(0, 1, 0)
		}
		limit(reset.Unix())
	}
	var attributes struct {
		QualitySeed, Enchantment                                              uint32
		Durability, AmplificationValue                                        uint16
		Reinforcement, Forge, AmplificationType, Binding, EnchantUpgradeCount byte
		ExpiresAt                                                             *int64
		Locked                                                                bool
	}
	core := make([]byte, 119)
	if deadline != nil {
		if *deadline <= now.Unix() || *deadline > math.MaxInt32 {
			return storage.QuestGrant{}, savedGrant{}, storage.ErrInvalid
		}
		attributes.ExpiresAt = deadline
		little.PutUint32(core[56:], uint32(*deadline))
	}
	if t.InitialSealed {
		attributes.Binding, core[13] = 1, 1
	}
	if t.Kind != 2 {
		h := sha256.Sum256([]byte(fmt.Sprintf("quest:%d:%s:%d", id, objectiveHash, m.Template)))
		attributes.QualitySeed, attributes.Durability = 1+little.Uint32(h[:])%999999997, t.MaximumDurability
	}
	attr, _ := json.Marshal(attributes)
	g := storage.QuestGrant{
		Asset: storage.Asset{CharacterID: in.CharacterID, Source: "item", TemplateID: m.Template, Count: m.Count, Template: raw, Attributes: attr, Core: core, Detail: []byte{}},
		Kind:  t.Kind, First: t.FirstBagSlot, Last: min(t.LastBagSlot, t.FirstBagSlot+31+in.BagExpansion), StackLimit: t.StackLimit,
		QuickSlots: t.Kind == 2 && t.FirstBagSlot == 65 && t.LastBagSlot == 120 && !t.AccountCommonInventory && !t.PetConsumable,
	}
	switch {
	case t.Kind == 0:
		g.Source = "weapon"
		f := storage.WeaponFacts{QualitySeed: attributes.QualitySeed, Durability: attributes.Durability, MaximumDurability: t.MaximumDurability, MinimumLevel: t.MinimumLevel, UsableJobs: t.UsableJobs, ResourceHash: t.ResourceHash}
		f.Qualification, _ = json.Marshal(t.WeaponQualification)
		g.Weapon = &f
	case t.Kind == 1 && t.WornSlotIsNative && t.WornSlot == 32:
		g.Location, g.First, g.Last = 38, 0, 55
	case t.AccountCommonInventory:
		g.CharacterID, g.Location, g.First, g.Last = 0, 45, 0, 49
		g.StackLimit = min(g.StackLimit, math.MaxInt32)
		if g.Count > math.MaxInt32 {
			return storage.QuestGrant{}, savedGrant{}, storage.ErrInvalid
		}
	case t.PetConsumable:
		g.Location, g.Last = 7, 271
	case t.Kind == 2 && t.FirstBagSlot == 289 && t.LastBagSlot == 360:
		g.Last = 360
	}
	if slot, shared := storage.SharedMaterialSlot(m.Template); shared {
		g.CharacterID, g.Source, g.First, g.Last, g.QuickSlots = 0, "shared", slot, slot, false
	}
	grant := savedGrant{Template: raw, Attributes: attr, Count: g.Count, Raw119: make([]uint16, len(core)), Detail: []uint16{}, PreserveIdentity: "00000000-0000-0000-0000-000000000000"}
	for i, b := range core {
		grant.Raw119[i] = uint16(b)
	}
	return g, grant, nil
}

func (catalog *Catalog) Complete(c storage.Character, in storage.Inventory, receipts map[string]json.RawMessage, r CompleteRequest, opts CompleteContext) (CompleteAction, error) {
	var out CompleteAction
	q := catalog.ordinary[r.Id]
	if q == nil || !q.Supported || q.Operations == nil || !q.Operations.Available || r.Multiplier != 1 || r.Tail != math.MaxUint16 || r.Selection != math.MaxUint16 && len(q.SelectionRewardRows) == 0 {
		return out, storage.ErrInvalid
	}
	p, err := playerOf(c)
	if err != nil || !p.initialized || p.hist.Cleared == nil {
		return out, storage.ErrInvalid
	}
	book, rows, err := questBook(c)
	if err != nil {
		return out, err
	}
	if _, err = catalog.refresh(book, &p, q.Operations.Daily, opts.Now); err != nil {
		return out, err
	}
	day := 0
	if q.Operations.Daily {
		if json.Unmarshal(book["DailyDay"], &day) != nil || day == 0 {
			return out, storage.ErrInvalid
		}
	}
	v := projectionView(catalog, p, in, receipts)
	d := opts.Dungeon
	v.inDungeon = d != nil
	if d != nil && (!d.living(c) || !catalog.dungeonObjective(q, d)) {
		return out, storage.ErrInvalid
	}
	if q.DungeonCinematicTarget != nil && !movieReceipt(v, q, d, c, p.hist.Active[q.Id], p.hist.Cleared[q.Id] != 0) {
		return out, storage.ErrInvalid
	}
	if q.Kind == communicator && !communicatorUsed(c, v, q, p.hist.Cleared[q.Id] != 0) {
		return out, storage.ErrInvalid
	}
	if raw := receipts[completionKey(q, day)]; raw != nil {
		frozen, ok := completionReplay(p, in, q, receipts, r, day)
		if !ok || d != nil && guidKey(frozen.DungeonRunId) != "" && guidKey(frozen.DungeonRunId) != guidKey(d.RunID) || !catalog.checkJointReplay(p, in, q, receipts, frozen, raw) {
			return out, storage.ErrInvalid
		}
		return completeAck(q, c, frozen.ExperienceAward)
	}
	a := p.hist.Active[q.Id]
	if !validActive(q, a) || p.hist.Cleared[q.Id] != 0 {
		return out, storage.ErrInvalid
	}
	if q.Kind == communicator {
		if !a.NpcMet {
			return out, storage.ErrInvalid
		}
		if err := communicatorScene(c, v, q, opts.ChangeContext); err != nil {
			return out, err
		}
	}
	if q.Kind == question {
		if a.Answer == nil || !catalog.ordinaryAvailable(p, q) {
			return out, storage.ErrInvalid
		}
		if err := requireQuestionScene(c, q, opts.ChangeContext); err != nil {
			return out, err
		}
	}
	claims := []*Quest{q}
	var claimRun string
	if d != nil {
		claimRun = wireGUID(d.RunID)
		if q.Kind == questClear {
			last, _, ok := v.orderedTerminal(q, d, opts.Lease, c)
			if !ok {
				return out, storage.ErrInvalid
			}
			if last != nil {
				claims = []*Quest{last, q}
			}
		} else if !v.dungeonCompleted(q, d, opts.Lease, c) {
			return out, storage.ErrInvalid
		}
	} else if q.TownHandIn {
		if !v.handInReady(q, map[uint16]bool{}) {
			return out, storage.ErrInvalid
		}
		last := q.Children[len(q.Children)-1]
		if p.hist.Cleared[last] == 0 {
			claims = []*Quest{catalog.ordinary[last], q}
		}
		// The C# town hand-in branch deliberately leaves DungeonRunId empty,
		// even when the first child was an in-dungeon objective (3851).
	} else if q.OrderedDungeonParent {
		last, run, ok := v.orderedTerminal(q, nil, opts.Lease, c)
		if !ok {
			return out, storage.ErrInvalid
		}
		claimRun = run
		if last != nil {
			claims = []*Quest{last, q}
		}
	} else if parent := catalog.ordinary[q.Parent]; q.Parent != 0 && parent != nil && parent.MixedDungeonParent && len(parent.Children) > 0 && parent.Children[len(parent.Children)-1] == q.Id {
		_, run, ok := v.orderedTerminal(parent, nil, opts.Lease, c)
		if !ok {
			return out, storage.ErrInvalid
		}
		claimRun = run
	} else if q.TownPassive {
		n, e := v.remaining(q, map[uint16]bool{})
		claimRun = v.passiveProof(q, a, opts.Lease, nil, false)
		if e != nil || n != 0 || claimRun == "" {
			return out, storage.ErrInvalid
		}
	} else if q.Id == 3275 || slices.Contains([]uint16{12301, 6413, 6422, 3213}, q.Id) {
		n, e := v.remaining(q, map[uint16]bool{})
		if q.Id == 3275 {
			claimRun = v.noireProof(q, a, opts.Lease)
		} else {
			claimRun = v.roomProof(q, a, opts.Lease.Account, nil, true)
		}
		if e != nil || n != 0 || claimRun == "" {
			return out, storage.ErrInvalid
		}
	} else {
		if q.Kind == questClear && q.Subtype == 2 {
			for _, id := range q.Children {
				child := catalog.ordinary[id]
				if child == nil || p.hist.Cleared[id] != 0 || child.Kind != seeking && child.Kind != clearMap || len(child.Materials) != 0 || len(child.Rewards) != 0 || child.GoldAward != 0 || child.TpAward != 0 || child.AwakeningAward != nil {
					continue
				}
				if n, err := v.remaining(child, map[uint16]bool{}); err == nil && n == 0 {
					markComplete(p.hist, rows, id, 1)
				}
			}
		}
		if n, err := v.remaining(q, map[uint16]bool{}); err != nil || n != 0 {
			return out, storage.ErrInvalid
		}
	}
	return catalog.rewardClaims(c, v, book, rows, in, claims, r, day, claimRun, opts)
}

func (catalog *Catalog) checkJointReplay(p player, in storage.Inventory, q *Quest, rs map[string]json.RawMessage, f completion, parent json.RawMessage) bool {
	if f.TerminalQuestId == 0 {
		return true
	}
	sub := catalog.ordinary[f.TerminalQuestId]
	if q.Kind != questClear || q.Subtype != 2 || len(q.Children) == 0 || q.Children[len(q.Children)-1] != f.TerminalQuestId || sub == nil || sub.Parent != q.Id {
		return false
	}
	r, ok := completionReplay(p, in, sub, rs, CompleteRequest{sub.Id, 65535, 1, 65535}, 0)
	var a, b receipt
	return ok && r.ParentQuestId == q.Id && r.DungeonRunId == f.DungeonRunId && json.Unmarshal(parent, &a) == nil && json.Unmarshal(rs[completionKey(sub, 0)], &b) == nil && a.Revision == b.Revision
}

func markComplete(h history, raw map[uint16]json.RawMessage, id uint16, flag byte) {
	h.Cleared[id] = flag
	delete(h.Active, id)
	delete(raw, id)
}

func completeAck(q *Quest, c storage.Character, xp uint32) (CompleteAction, error) {
	out := CompleteAction{Experience: xp}
	out.Full = true
	b := little.AppendUint16([]byte{1}, q.Id)
	b = little.AppendUint32(append(b, 0), xp)
	b = append(b, 0)
	if q.AwakeningAward != nil {
		var profile struct {
			Skills []struct {
				Slot, Id uint16
				Level    byte
			}
		}
		stage := q.AwakeningAward.Stage
		if json.Unmarshal(c.Gameplay, &profile) != nil || stage < 1 || stage > 3 || len(profile.Skills) > 255 {
			return out, storage.ErrInvalid
		}
		out.Awakening = uint32(stage)
		b = append(b, 2, stage)
		slices.SortFunc(profile.Skills, func(a, b struct {
			Slot, Id uint16
			Level    byte
		}) int {
			return int(a.Id) - int(b.Id)
		})
		slots := map[uint16]bool{}
		for i, row := range profile.Skills {
			if row.Slot > 254 || row.Id == 0 || row.Level == 0 || slots[row.Slot] || i > 0 && profile.Skills[i-1].Id == row.Id {
				return out, storage.ErrInvalid
			}
			slots[row.Slot] = true
		}
		for range 2 {
			b = append(b, byte(len(profile.Skills)))
			for _, row := range profile.Skills {
				b = append(little.AppendUint16(append(b, byte(row.Slot)), row.Id), row.Level)
			}
		}
	} else if q.Id == 2674 && q.Kind == duel && q.TpAward == 2 {
		b = append(b, 29)
	} else {
		b = append(b, 0, 0)
	}
	out.Ack = b
	return out, nil
}

func (catalog *Catalog) rewardClaims(c storage.Character, v view, book map[string]json.RawMessage, rows map[uint16]json.RawMessage, in storage.Inventory, claims []*Quest, req CompleteRequest, day int, claimRun string, opts CompleteContext) (CompleteAction, error) {
	var out CompleteAction
	var doc map[string]json.RawMessage
	if json.Unmarshal(c.Gameplay, &doc) != nil || opts.Growth == nil {
		return out, storage.ErrInvalid
	}
	type plannedClaim struct {
		quest  *Quest
		grants []storage.QuestGrant
		completion
	}
	plans := make([]plannedClaim, 0, len(claims))
	var total uint64
	for _, q := range claims {
		p := v.p
		if (q.AdvancementChain != 0 || q.AwakeningAward != nil) && !catalog.ordinaryAvailable(p, q) {
			return out, storage.ErrInvalid
		}
		if int(c.Level) > len(q.ExperienceByLevel) || c.Level == 0 || q.ExperienceByLevel[c.Level-1] == nil {
			return out, storage.ErrInvalid
		}
		sel := uint16(math.MaxUint16)
		if q.Id == req.Id {
			sel = req.Selection
		}
		rewards, err := rewardRows(p, q, sel)
		if err != nil {
			return out, err
		}
		plan := plannedClaim{quest: q, completion: completion{QuestId: q.Id, ObjectiveHash: q.Hash, Rewards: []savedGrant{}}}
		for _, material := range rewards {
			g, saved, err := catalog.rewardGrant(p, in, q, material, opts.Now)
			if err != nil {
				return out, err
			}
			plan.grants = append(plan.grants, g)
			plan.Rewards = append(plan.Rewards, saved)
		}
		flag := byte(1)
		if q.Kind == question {
			answer := p.hist.Active[q.Id].Answer
			if answer == nil || *answer >= uint16(q.QuestionChoices) {
				return out, storage.ErrInvalid
			}
			chosen := byte(*answer)
			plan.QuestionAnswer, flag = &chosen, chosen+1
		}
		beforeGrowth := c
		markComplete(v.p.hist, rows, q.Id, flag)
		for _, linked := range q.CascadeClear {
			if linked == 0 || linked >= 30000 || linked == q.Id {
				return out, storage.ErrInvalid
			}
			markComplete(v.p.hist, rows, linked, 1)
		}
		book["Active"], _ = json.Marshal(rows)
		book["Cleared"], _ = json.Marshal(v.p.hist.Cleared)
		doc["Quests"], _ = json.Marshal(book)
		c.Gameplay, _ = json.Marshal(doc)
		grow := p.grow
		if q.AwakeningAward != nil {
			grow = grow&15 | q.AwakeningAward.Stage<<4
		}
		c, plan.ExperienceAward, err = opts.Growth(c, *q.ExperienceByLevel[c.Level-1], q.TpAward, grow, len(q.SkillEffects) != 0 || len(q.CascadeClear) != 0)
		if err != nil {
			return out, fmt.Errorf("%w: quest growth: %v", storage.ErrInvalid, err)
		}
		if len(q.SkillEffects) != 0 {
			c, err = catalog.applySkillEffects(beforeGrowth, c, q, opts)
			if err != nil {
				return out, err
			}
		}
		if err = json.Unmarshal(c.Gameplay, &doc); err != nil {
			return out, err
		}
		v.p.level, v.p.grow = c.Level, grow
		total += uint64(plan.ExperienceAward)
		plans = append(plans, plan)
	}
	var progress struct {
		Sp       uint16
		GrowType byte
	}
	var settings struct{ GoldExpansion byte }
	if json.Unmarshal(doc["Progression"], &progress) != nil || json.Unmarshal(c.Gameplay, &settings) != nil || settings.GoldExpansion > 8 {
		return out, storage.ErrInvalid
	}
	limit, ok := opts.GoldCarryLimits[c.Level]
	if !ok || limit < 0 {
		return out, storage.ErrInvalid
	}
	if settings.GoldExpansion != 0 {
		limit = int64(4+settings.GoldExpansion) * 100000000
	}
	xp := uint32(min(total, math.MaxUint32))
	var next = in
	newReceipts := map[string]json.RawMessage{}
	for i, claim := range plans {
		q := claim.quest
		if q.Id == req.Id {
			claim.ExperienceAward = xp
		}
		if len(plans) == 2 {
			if i == 0 {
				claim.ParentQuestId = req.Id
			} else {
				claim.TerminalQuestId = plans[0].QuestId
			}
		}
		if q.Operations.Daily {
			claim.DailyDay = day
		}
		claim.DungeonRunId = claimRun
		claim.Level, claim.Sp, claim.GrowType = c.Level, progress.Sp, progress.GrowType
		if next.Gold < int64(q.GoldRequirement) {
			return out, storage.ErrFunds
		}
		claim.GoldAward = uint32(min(int64(q.GoldAward), max(0, min(limit, math.MaxUint32)-next.Gold)))
		claim.DiscardedGoldAward = q.GoldAward - claim.GoldAward
		materials := make([]storage.QuestMaterial, 0, len(q.Materials))
		for _, m := range q.Materials {
			materials = append(materials, storage.QuestMaterial{Template: m.Template, Count: m.Count})
		}
		var err error
		next, err = storage.QuestLoot(next, materials, claim.grants, next.Gold+int64(claim.GoldAward)-int64(q.GoldRequirement), opts.Now)
		if err != nil {
			return out, err
		}
		result, _ := json.Marshal(claim.completion)
		input := req
		if q.Id != req.Id {
			input = CompleteRequest{q.Id, 65535, 1, 65535}
		}
		key := completionKey(q, claim.DailyDay)
		if v.receipts[key] != nil {
			return out, storage.ErrInvalid
		}
		newReceipts[key], _ = json.Marshal(receipt{Key: key, Input: completeInput(input), Result: string(result), Revision: c.Revision + 1})
	}
	granted := storage.QuestGranted(in, next)
	for key, raw := range newReceipts {
		var r receipt
		_ = json.Unmarshal(raw, &r)
		r.Grants = granted
		newReceipts[key], _ = json.Marshal(r)
	}
	out, err := completeAck(claims[len(claims)-1], c, xp)
	if err != nil {
		return out, err
	}
	out.Mutation.Reward = &storage.QuestRewardMutation{Character: c, Inventory: next, Receipts: newReceipts}
	return out, nil
}
