package questwire

import (
	"encoding/json"
	"slices"
	"time"

	"s9a1/server/internal/storage"
)

const trainingManual = uint32(10158452)

func (catalog *Catalog) TrainingManual(c storage.Character, in storage.Inventory, rs map[string]json.RawMessage, now time.Time, qualifies func(DuelObjective) bool) (storage.TownQuestMutation, error) {
	var out storage.TownQuestMutation
	a, err := catalog.DuelObjective(c, 2674)
	if err != nil || a.ID != 2674 || c.Level < 50 {
		return out, storage.ErrInvalid
	}
	key := "duel-manual:2674:" + a.Acceptance
	if a.Remaining[2] == 0 {
		var prior receipt
		if json.Unmarshal(rs[key], &prior) != nil || prior.Key != key || prior.Revision <= 0 || prior.Revision > in.Revision || prior.Input != a.Hash {
			return out, storage.ErrInvalid
		}
		return out, nil
	}
	if !qualifies(a) || rs[key] != nil || !a.StageReady(2) {
		return out, storage.ErrInvalid
	}
	q := catalog.ordinary[a.ID]
	p, err := playerOf(c)
	if err != nil {
		return out, err
	}
	grant, _, err := catalog.rewardGrant(p, in, q, Material{trainingManual, 1}, now)
	if err != nil || grant.Kind != 2 || grant.Location != 0 {
		return out, storage.ErrInvalid
	}
	next := in
	if !slices.ContainsFunc(in.Items, func(a storage.Asset) bool { return a.Location == 0 && a.TemplateID == trainingManual && a.Count > 0 }) {
		next, err = storage.QuestLoot(in, nil, []storage.QuestGrant{grant}, in.Gold, now)
		if err != nil {
			return out, err
		}
	}
	activeRow, err := AdvanceDuelStage(c, next, q, 0x40, 0, true, func(d DuelObjective, stage byte) bool { return d == a && stage == 2 })
	if err != nil {
		return out, err
	}
	book, rows, err := questBook(c)
	if err != nil {
		return out, err
	}
	rows[q.Id] = activeRow
	book["Active"], _ = json.Marshal(rows)
	var doc map[string]json.RawMessage
	if err = json.Unmarshal(c.Gameplay, &doc); err != nil {
		return out, err
	}
	doc["Quests"], _ = json.Marshal(book)
	c.Gameplay, _ = json.Marshal(doc)
	result, _ := json.Marshal(struct {
		Quest      uint16
		Acceptance string
		Manual     uint32
	}{q.Id, a.Acceptance, trainingManual})
	raw, _ := json.Marshal(receipt{Key: key, Input: q.Hash, Result: string(result), Revision: c.Revision + 1, Grants: storage.QuestGranted(in, next)})
	out.Reward = &storage.QuestRewardMutation{Character: c, Inventory: next, Receipts: map[string]json.RawMessage{key: raw}}
	return out, nil
}
